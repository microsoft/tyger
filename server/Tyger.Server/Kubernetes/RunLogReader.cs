using System.Globalization;
using System.Net;
using k8s;
using k8s.Autorest;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.Server.Database;
using Tyger.Server.Logging;
using Tyger.Server.Model;
using static Tyger.Server.Kubernetes.KubernetesMetadata;

namespace Tyger.Server.Kubernetes;

public class RunLogReader : ILogSource
{
    private readonly k8s.Kubernetes _client;
    private readonly IRepository _repository;
    private readonly ILogArchive _logArchive;
    private readonly KubernetesOptions _k8sOptions;

    public RunLogReader(
        k8s.Kubernetes client,
        IRepository repository,
        IOptions<KubernetesOptions> k8sOptions,
        ILogArchive logArchive)
    {
        _client = client;
        _repository = repository;
        _logArchive = logArchive;
        _k8sOptions = k8sOptions.Value;
    }

    public async Task<Pipeline?> GetLogs(long runId, GetLogsOptions options, CancellationToken cancellationToken)
    {
        switch (await _repository.GetRun(runId, cancellationToken))
        {
            case null:
                return null;
            case (Run run, _, null):
                if (!options.Follow)
                {
                    return await GetLogsSnapshot(run, options, cancellationToken);
                }

                V1Job job;
                try
                {
                    job = await _client.ReadNamespacedJobAsync(JobNameFromRunId(runId), _k8sOptions.Namespace, cancellationToken: cancellationToken);
                }
                catch (HttpOperationException e) when (e.Response.StatusCode == HttpStatusCode.NotFound)
                {
                    return null;
                }

                if (RunReader.UpdateRunFromJobAndPods(run, job, Array.Empty<V1Pod>()).Status is "Succeeded" or "Failed")
                {
                    return await GetLogsSnapshot(run, options, cancellationToken);
                }

                return await FollowLogs(run, job, options, cancellationToken);

            default:
                return await _logArchive.GetLogs(runId, options, cancellationToken);
        }
    }

    private async Task<Pipeline> FollowLogs(Run run, V1Job job, GetLogsOptions options, CancellationToken cancellationToken)
    {
        var singleReplica = run.Job.Replicas + (run.Worker?.Replicas ?? 0) == 1;
        var pods = await _client.EnumeratePodsInNamespace(_k8sOptions.Namespace, labelSelector: $"{RunLabel}={run.Id}", cancellationToken: cancellationToken)
            .ToListAsync(cancellationToken);

        var followingPods = new HashSet<string>();
        var initialPipelines = new List<IPipelineSource>();

        foreach (var pod in pods)
        {
            if (IsPodRunningOrTerminated(pod) && await GetLogsFromPod(pod.Name(), GetPodPrefix(pod, singleReplica), options with { IncludeTimestamps = true }, cancellationToken) is Pipeline podPipeline)
            {
                followingPods.Add(pod.Name());
                initialPipelines.Add(podPipeline);
            }
        }

        var syncRoot = new object();
        var leafMerger = new LiveLogMerger();
        LiveLogMerger rootMerger;
        if (initialPipelines.Count > 0)
        {
            rootMerger = new LiveLogMerger();
            rootMerger.Activate(cancellationToken, new IPipelineSource[] { leafMerger }.Concat(initialPipelines).ToArray());
        }
        else
        {
            rootMerger = leafMerger;
        }

        var pipeline = new Pipeline(rootMerger);
        if (!options.IncludeTimestamps)
        {
            pipeline.AddElement(new LogLineFormatter(false, null));
        }

        _ = WatchJob();
        _ = WatchPods();

        return pipeline;

        async Task WatchJob()
        {
            try
            {
                var jobList = _client.ListNamespacedJobWithHttpMessagesAsync(
                    _k8sOptions.Namespace,
                    fieldSelector: $"metadata.name={JobNameFromRunId(run.Id)}",
                    watch: true,
                    resourceVersion: job.ResourceVersion(),
                    cancellationToken: cancellationToken);

                await foreach (var (type, item) in jobList.WatchAsync<V1Job, V1JobList>().WithCancellation(cancellationToken))
                {
                    switch (type)
                    {
                        case WatchEventType.Bookmark:
                            continue;
                        case WatchEventType.Deleted:
                            break;
                        case WatchEventType.Modified:
                            var status = RunReader.UpdateRunFromJobAndPods(run, item, Array.Empty<V1Pod>()).Status;
                            if (status is "Succeeded" or "Failed")
                            {
                                break;
                            }

                            continue;
                        default:
                            throw new InvalidDataException($"Unexpected watch event type {type}");
                    }

                    break;
                }
            }
            finally
            {
                lock (syncRoot)
                {
                    // The job is finished or we encountered a failure.
                    // Release the leaf merger that keeps the stream open
                    leafMerger.Activate(cancellationToken);

                    // Signal that the root merger should not keep waiting
                    // for data from worker streams.
                    rootMerger.Complete();
                }
            }
        }

        async Task WatchPods()
        {
            var podList = _client.ListNamespacedPodWithHttpMessagesAsync(
                _k8sOptions.Namespace,
                labelSelector: $"{RunLabel}={run.Id}",
                watch: true,
                resourceVersion: job.ResourceVersion(),
                cancellationToken: cancellationToken);

            await foreach (var (type, item) in podList.WatchAsync<V1Pod, V1PodList>().WithCancellation(cancellationToken))
            {
                switch (type)
                {
                    case WatchEventType.Added:
                    case WatchEventType.Modified:
                        if (IsPodRunningOrTerminated(item) &&
                            !followingPods.Contains(item.Name()) &&
                            await GetLogsFromPod(item.Name(), GetPodPrefix(item, singleReplica), options with { IncludeTimestamps = true }, cancellationToken) is Pipeline podPipeline)
                        {
                            lock (syncRoot)
                            {
                                // a new pod has started. Merge its log in my starting the existing waiting leaf merger
                                // and create a new leaf merger for the next pod.
                                var newLeaf = new LiveLogMerger();
                                leafMerger.Activate(cancellationToken, newLeaf, podPipeline);
                                leafMerger = newLeaf;
                            }
                        }

                        continue;

                    default:
                        continue;
                }
            }
        }
    }

    private static bool IsPodRunningOrTerminated(V1Pod pod)
    {
        return pod.Status.ContainerStatuses is { Count: > 0 } && pod.Status.ContainerStatuses[0].State.Waiting == null;
    }

    private async Task<Pipeline?> GetLogsSnapshot(Run run, GetLogsOptions options, CancellationToken cancellationToken)
    {
        var singleReplica = run.Job.Replicas + (run.Worker?.Replicas ?? 0) == 1;
        var podLogOptions = options with { IncludeTimestamps = true, Follow = false };
        var pipelines = await _client.EnumeratePodsInNamespace(_k8sOptions.Namespace, labelSelector: $"{RunLabel}={run.Id}", cancellationToken: cancellationToken)
            .SelectAwait(async p => (await GetLogsFromPod(p.Name(), GetPodPrefix(p, singleReplica), podLogOptions, cancellationToken))!)
            .Where(p => p != null)
            .ToArrayAsync(cancellationToken);

        var pipeline = new Pipeline(new FixedLogMerger(cancellationToken, pipelines));
        if (!options.IncludeTimestamps)
        {
            pipeline.AddElement(new LogLineFormatter(false, null));
        }

        return pipeline;
    }

    private static string? GetPodPrefix(V1Pod pod, bool singleReplica)
    {
        if (singleReplica)
        {
            return null;
        }

        if (pod.GetAnnotation("batch.kubernetes.io/job-completion-index") is string indexString)
        {
            return $"[job-{indexString}]";
        }

        return $"[worker-{pod.Spec.Hostname[pod.Spec.Hostname.LastIndexOf('-') + 1]}]";
    }

    // We need to do the HTTP request ourselves because the sinceTime parameter is missing https://github.com/kubernetes-client/csharp/issues/829
    private async Task<Pipeline?> GetLogsFromPod(string podName, string? prefix, GetLogsOptions options, CancellationToken cancellationToken)
    {
        var qs = QueryString.Empty;
        if (options.IncludeTimestamps)
        {
            qs = qs.Add("timestamps", "true");
        }

        if (options.TailLines.HasValue)
        {
            qs = qs.Add("tailLines", options.TailLines.Value.ToString(CultureInfo.InvariantCulture));
        }

        if (options.Since.HasValue)
        {
            qs = qs.Add("sinceTime", options.Since.Value.ToString("o"));
        }

        if (options.Follow)
        {
            qs = qs.Add("follow", "true");
        }

        var uri = new Uri(_client.BaseUri, $"api/v1/namespaces/{_k8sOptions.Namespace}/pods/{podName}/log{qs.ToUriComponent()}");

        var requestMessage = new HttpRequestMessage(HttpMethod.Get, uri);
        if (_client.Credentials != null)
        {
            cancellationToken.ThrowIfCancellationRequested();
            await _client.Credentials.ProcessHttpRequestAsync(requestMessage, cancellationToken).ConfigureAwait(false);
        }

        var response = await _client.HttpClient.SendAsync(requestMessage, HttpCompletionOption.ResponseHeadersRead, cancellationToken);

        switch (response.StatusCode)
        {
            case HttpStatusCode.OK:
                var logs = await response.Content.ReadAsStreamAsync(cancellationToken);
                var pipeline = new Pipeline(new SimplePipelineSource(logs));
                if (options.IncludeTimestamps)
                {
                    pipeline.AddElement(new TimestampedLogReformatter());
                }

                if (!string.IsNullOrEmpty(prefix))
                {
                    pipeline.AddElement(new LogLineFormatter(options.IncludeTimestamps, prefix));
                }

                return pipeline;
            case HttpStatusCode.NoContent:
                return null;
            case HttpStatusCode.NotFound:
                return null;
            case HttpStatusCode.BadRequest:
                // likely means the pod has not started yet.
                return null;
            default:
                throw new InvalidOperationException($"Unexpected status code '{response.StatusCode} from cluster. {await response.Content.ReadAsStringAsync(cancellationToken)}");
        }
    }
}
