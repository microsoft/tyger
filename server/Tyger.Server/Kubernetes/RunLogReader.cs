using System.Globalization;
using System.Net;
using k8s;
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
    private readonly ILoggerFactory _loggerFactory;
    private readonly ILogger<RunLogReader> _logger;
    private readonly KubernetesOptions _k8sOptions;

    public RunLogReader(
        k8s.Kubernetes client,
        IRepository repository,
        IOptions<KubernetesOptions> k8sOptions,
        ILogArchive logArchive,
        ILoggerFactory loggerFactory,
        ILogger<RunLogReader> logger)
    {
        _client = client;
        _repository = repository;
        _logArchive = logArchive;
        _loggerFactory = loggerFactory;
        _logger = logger;
        _k8sOptions = k8sOptions.Value;
    }

    public async Task<Pipeline?> GetLogs(long runId, GetLogsOptions options, CancellationToken cancellationToken)
    {
        switch (await _repository.GetRun(runId, cancellationToken))
        {
            case null:
                return null;
            case (Run run, _, null):
                if (!options.Follow || run.Status == "Canceling")
                {
                    return await GetLogsSnapshot(run, options, cancellationToken);
                }

                var jobs = await _client.BatchV1.ListNamespacedJobAsync(_k8sOptions.Namespace, fieldSelector: $"metadata.name={JobNameFromRunId(run.Id!.Value)}", cancellationToken: cancellationToken);
                if (jobs.Items.Count == 0)
                {
                    return null;
                }

                if (RunReader.UpdateRunFromJobAndPods(run, jobs.Items.Single(), Array.Empty<V1Pod>()).Status is "Succeeded" or "Failed")
                {
                    return await GetLogsSnapshot(run, options, cancellationToken);
                }

                return await FollowLogs(run, jobs.ResourceVersion(), options, cancellationToken);

            default:
                return await _logArchive.GetLogs(runId, options, cancellationToken);
        }
    }

    private async Task<Pipeline> FollowLogs(Run run, string resourceVersion, GetLogsOptions options, CancellationToken cancellationToken)
    {
        var pods = await _client.EnumeratePodsInNamespace(_k8sOptions.Namespace, labelSelector: $"{RunLabel}={run.Id}", cancellationToken: cancellationToken)
            .ToListAsync(cancellationToken);

        var followingContainers = new HashSet<(string podName, string containerName)>();
        var initialPipelines = new List<IPipelineSource>();
        var terminableWorkerElements = new List<TerminablePipelineElement>();

        void TrackPipelineIfWorkerPod(V1Pod pod, Pipeline podPipeline)
        {
            if (pod.GetLabel(WorkerLabel) != null)
            {
                var terminableElement = new TerminablePipelineElement();
                terminableWorkerElements.Add(terminableElement);
                podPipeline.AddElement(terminableElement);
            }
        }

        foreach (var pod in pods)
        {
            foreach (var container in pod.Spec.Containers)
            {
                if (IsContainerRunningOrTerminated(pod, container))
                {
                    var containerPipeline = new Pipeline(
                        new ResumablePipelineSource(
                            async opts => await GetLogsFromPod(pod.Name(), container.Name, GetPrefix(run, pod, container), opts, cancellationToken),
                            options with { IncludeTimestamps = true },
                            _loggerFactory.CreateLogger<ResumablePipelineSource>()));

                    initialPipelines.Add(containerPipeline);
                    followingContainers.Add((pod.Name(), container.Name));
                    TrackPipelineIfWorkerPod(pod, containerPipeline);
                }
            }
        }

        var syncRoot = new object();
        bool finished = false;
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
            var jobWatchResourceVersion = resourceVersion;
            try
            {
                var watchEnumerable = _client.WatchNamespacedJobsWithRetry(
                    _logger,
                    _k8sOptions.Namespace,
                    fieldSelector: $"metadata.name={JobNameFromRunId(run.Id!.Value)}",
                    resourceVersion: resourceVersion,
                    cancellationToken: cancellationToken);

                await foreach (var (type, item) in watchEnumerable.WithCancellation(cancellationToken))
                {
                    switch (type)
                    {
                        case WatchEventType.Bookmark:
                            continue;
                        case WatchEventType.Deleted:
                            return;
                        case WatchEventType.Modified:
                            var status = RunReader.UpdateRunFromJobAndPods(run, item, Array.Empty<V1Pod>()).Status;
                            if (status is "Succeeded" or "Failed" or "Canceled")
                            {
                                return;
                            }

                            continue;
                        default:
                            throw new InvalidDataException($"Unexpected watch event type {type}");
                    }
                }
            }
            catch (Exception e) when (e is not OperationCanceledException || !cancellationToken.IsCancellationRequested)
            {
                _logger.UnexpectedExceptionDuringWatch(e);
                throw;
            }
            finally
            {
                lock (syncRoot)
                {
                    finished = true;

                    // The job is finished or we encountered a failure.
                    // Release the leaf merger that keeps the stream open
                    leafMerger.Activate(cancellationToken);

                    // The worker streams never end on their own, so terminate them.
                    foreach (var terminableElements in terminableWorkerElements)
                    {
                        terminableElements.Terminate();
                    }
                }
            }
        }

        async Task WatchPods()
        {
            try
            {
                var watchEnumerable = _client.WatchNamespacedPodsWithRetry(
                    _logger,
                    _k8sOptions.Namespace,
                    labelSelector: $"{RunLabel}={run.Id}",
                    resourceVersion: resourceVersion,
                    cancellationToken: cancellationToken);

                await foreach (var (type, pod) in watchEnumerable.WithCancellation(cancellationToken))
                {
                    switch (type)
                    {
                        case WatchEventType.Added:
                        case WatchEventType.Modified:
                            foreach (var container in pod.Spec.Containers)
                            {
                                if (IsContainerRunningOrTerminated(pod, container) &&
                                    !followingContainers.Contains((pod.Name(), container.Name)))
                                {
                                    var podPipeline = new Pipeline(
                                        new ResumablePipelineSource(
                                            async opts => await GetLogsFromPod(pod.Name(), container.Name, GetPrefix(run, pod, container), opts, cancellationToken),
                                            options with { IncludeTimestamps = true },
                                            _loggerFactory.CreateLogger<ResumablePipelineSource>()));

                                    followingContainers.Add((pod.Name(), container.Name));

                                    lock (syncRoot)
                                    {
                                        if (finished)
                                        {
                                            return;
                                        }

                                        TrackPipelineIfWorkerPod(pod, podPipeline);

                                        // a new pod has started. Merge its log in by starting the existing waiting leaf merger
                                        // and create a new leaf merger for the next pod.
                                        var newLeaf = new LiveLogMerger();
                                        leafMerger.Activate(cancellationToken, newLeaf, podPipeline);
                                        leafMerger = newLeaf;
                                    }
                                }
                            }

                            continue;

                        default:
                            continue;
                    }
                }
            }
            catch (Exception e) when (e is not OperationCanceledException || !cancellationToken.IsCancellationRequested)
            {
                _logger.UnexpectedExceptionDuringWatch(e);
                throw;
            }
        }
    }
    private static bool IsContainerRunningOrTerminated(V1Pod pod, V1Container container)
    {
        return pod.Status.ContainerStatuses?.SingleOrDefault(s => s.Name == container.Name) is { State.Waiting: null };
    }
    private async Task<Pipeline?> GetLogsSnapshot(Run run, GetLogsOptions options, CancellationToken cancellationToken)
    {
        var podLogOptions = options with { Follow = false };
        var pods = await _client.EnumeratePodsInNamespace(_k8sOptions.Namespace, labelSelector: $"{RunLabel}={run.Id}", cancellationToken: cancellationToken).ToArrayAsync(cancellationToken);

        if (pods is [var singlePod] && pods[0].Spec.Containers is [var singleContainer])
        {
            // simple case where no merging or transforming is required.
            return await GetLogsFromPod(singlePod.Name(), singleContainer.Name, GetPrefix(run, singlePod, singleContainer), podLogOptions, cancellationToken);
        }

        podLogOptions = podLogOptions with { IncludeTimestamps = true };

        var pipelines = await pods
            .SelectMany(p => p.Spec.Containers.Select(c => (pod: p, container: c)))
            .ToAsyncEnumerable()
            .SelectAwait(async p => (await GetLogsFromPod(p.pod.Name(), p.container.Name, GetPrefix(run, p.pod, p.container), podLogOptions, cancellationToken))!)
            .Where(p => p != null)
            .ToArrayAsync(cancellationToken);

        var pipeline = new Pipeline(new FixedLogMerger(cancellationToken, pipelines));
        if (!options.IncludeTimestamps)
        {
            pipeline.AddElement(new LogLineFormatter(false, null));
        }

        return pipeline;
    }

    private static string? GetPrefix(Run run, V1Pod pod, V1Container container)
    {
        static string? PodPrefix(Run run, V1Pod pod)
        {
            var totalReplicas = run.Job.Replicas + (run.Worker?.Replicas ?? 0);
            if (totalReplicas == 1)
            {
                return null;
            }

            if (pod.GetAnnotation("batch.kubernetes.io/job-completion-index") is string indexString)
            {
                return run.Job.Replicas > 1 ? $"[job-{indexString}]" : "[job]";
            }

            return run.Worker?.Replicas is > 1 ? $"[worker-{pod.Spec.Hostname[pod.Spec.Hostname.LastIndexOf('-') + 1]}]" : "[worker]";
        }

        static string? ContainerPrefix(V1Container container, bool singleContainer)
        {
            return singleContainer ? null : $"[{container?.Name}]";
        }

        return string.Concat(PodPrefix(run, pod), ContainerPrefix(container, pod.Spec.Containers.Count == 1));
    }

    // We need to do the HTTP request ourselves because the sinceTime parameter is missing https://github.com/kubernetes-client/csharp/issues/829
    private async Task<Pipeline?> GetLogsFromPod(string podName, string containerName, string? prefix, GetLogsOptions options, CancellationToken cancellationToken)
    {
        var qs = QueryString.Empty;
        qs = qs.Add("container", containerName);

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
