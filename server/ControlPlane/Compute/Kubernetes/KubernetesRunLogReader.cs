// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Globalization;
using System.Net;
using k8s;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Logging;
using Tyger.ControlPlane.Model;
using static Tyger.ControlPlane.Compute.Kubernetes.KubernetesMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public class KubernetesRunLogReader : ILogSource
{
    private readonly k8s.Kubernetes _client;
    private readonly IRepository _repository;
    private readonly ILogArchive _logArchive;
    private readonly ILoggerFactory _loggerFactory;
    private readonly ILogger<KubernetesRunLogReader> _logger;
    private readonly KubernetesApiOptions _k8sOptions;

    public KubernetesRunLogReader(
        k8s.Kubernetes client,
        IRepository repository,
        IOptions<KubernetesApiOptions> k8sOptions,
        ILogArchive logArchive,
        ILoggerFactory loggerFactory,
        ILogger<KubernetesRunLogReader> logger)
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
        var run = await _repository.GetRun(runId, cancellationToken);
        switch (run)
        {
            case null:
                return null;
            case { LogsArchivedAt: null }:
                if (!options.Follow || run.Status == RunStatus.Canceling)
                {
                    return await GetLogsSnapshot(run, options, cancellationToken);
                }

                var jobs = await _client.BatchV1.ListNamespacedJobAsync(_k8sOptions.Namespace, fieldSelector: $"metadata.name={JobNameFromRunId(run.Id!.Value)}", cancellationToken: cancellationToken);
                if (jobs.Items.Count == 0)
                {
                    return null;
                }

                run = await run.GetPartiallyUpdatedRun(_client, _k8sOptions, cancellationToken, jobs.Items.Single());

                if (run.Status is RunStatus.Succeeded or RunStatus.Failed or RunStatus.Canceled)
                {
                    return await GetLogsSnapshot(run, options, cancellationToken);
                }

                return await FollowLogs(run, jobs, options, cancellationToken);

            default:
                return await _logArchive.GetLogs(runId, options, cancellationToken);
        }
    }

    private async Task<Pipeline> FollowLogs(Run run, V1JobList jobList, GetLogsOptions options, CancellationToken cancellationToken)
    {
        var job = jobList.Items.Single();
        var podList = await _client.CoreV1.ListNamespacedPodAsync(_k8sOptions.Namespace, labelSelector: $"{RunLabel}={run.Id}", cancellationToken: cancellationToken);

        var followingContainers = new HashSet<(string podName, string containerName)>();
        var initialPipelines = new List<IPipelineSource>();
        var terminablePipelineElements = new List<TerminablePipelineElement>();

        var hasSocket = job.GetAnnotation(HasSocketAnnotation) == "true";

        void TrackPipelineIfNeedsTermination(V1Pod pod, string containerName, Pipeline podPipeline)
        {
            if (pod.GetLabel(WorkerLabel) != null || (hasSocket && pod.GetLabel(JobLabel) != null && containerName == "main"))
            {
                var terminableElement = new TerminablePipelineElement();
                terminablePipelineElements.Add(terminableElement);
                podPipeline.AddElement(terminableElement);
            }
        }

        run = await run.GetPartiallyUpdatedRun(_client, _k8sOptions, cancellationToken, jobList.Items.Single(), podList.Items.ToList());

        var terminableSocketContainers = new Dictionary<(V1Pod, string), TerminablePipelineElement>();

        foreach (var pod in podList.Items)
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
                    TrackPipelineIfNeedsTermination(pod, container.Name, containerPipeline);
                }
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

        var cts = new CancellationTokenSource();
        using var combinedCts = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, cts.Token);
        var jobWatchStream = _client.WatchNamespacedJobsWithRetry(_logger, _k8sOptions.Namespace, fieldSelector: $"metadata.name={JobNameFromRunId(run.Id!.Value)}", resourceVersion: jobList.ResourceVersion(), cancellationToken: combinedCts.Token).Select(t => (t.Item1, (IKubernetesObject)t.Item2));
        var podWatchStream = _client.WatchNamespacedPodsWithRetry(_logger, _k8sOptions.Namespace, labelSelector: $"{RunLabel}={run.Id}", resourceVersion: podList.ResourceVersion(), cancellationToken: combinedCts.Token).Select(t => (t.Item1, (IKubernetesObject)t.Item2));
        var combinedStream = AsyncEnumerableEx.Merge(jobWatchStream, podWatchStream).WithCancellation(combinedCts.Token);

        var combinedEnumerator = combinedStream.GetAsyncEnumerator();

        var pods = podList.Items.ToDictionary(p => p.Name());

        _ = WatchResources();

        return pipeline;

        async Task WatchResources()
        {
            bool updateRunFromRepository = false;
            try
            {
                while (await combinedEnumerator.MoveNextAsync())
                {
                    (WatchEventType watchEventType, IKubernetesObject k8sObject) = combinedEnumerator.Current;
                    switch (k8sObject)
                    {
                        case V1Job updatedJob:
                            switch (watchEventType)
                            {
                                case WatchEventType.Modified:
                                    job = updatedJob;
                                    break;
                                case WatchEventType.Deleted:
                                    updateRunFromRepository = true;
                                    cts.Cancel();
                                    goto Finished;
                            }

                            break;
                        case V1Pod updatedPod:
                            switch (watchEventType)
                            {
                                case WatchEventType.Added:
                                case WatchEventType.Modified:
                                    pods[updatedPod.Name()] = updatedPod;

                                    foreach (var container in updatedPod.Spec.Containers)
                                    {
                                        if (IsContainerRunningOrTerminated(updatedPod, container) &&
                                            !followingContainers.Contains((updatedPod.Name(), container.Name)))
                                        {
                                            var podPipeline = new Pipeline(
                                                new ResumablePipelineSource(
                                                    async opts => await GetLogsFromPod(updatedPod.Name(), container.Name, GetPrefix(run, updatedPod, container), opts, cancellationToken),
                                                    options with { IncludeTimestamps = true },
                                                    _loggerFactory.CreateLogger<ResumablePipelineSource>()));

                                            followingContainers.Add((updatedPod.Name(), container.Name));

                                            TrackPipelineIfNeedsTermination(updatedPod, container.Name, podPipeline);

                                            // a new pod has started. Merge its log in by starting the existing waiting leaf merger
                                            // and create a new leaf merger for the next pod.
                                            var newLeaf = new LiveLogMerger();
                                            leafMerger.Activate(cancellationToken, newLeaf, podPipeline);
                                            leafMerger = newLeaf;
                                        }
                                    }

                                    break;
                                case WatchEventType.Deleted:
                                    pods.Remove(updatedPod.Name());
                                    updateRunFromRepository = true;
                                    break;
                            }

                            break;
                    }

                    if (updateRunFromRepository)
                    {
                        var currentRun = await _repository.GetRun(run.Id!.Value, cancellationToken);
                        if (currentRun is null)
                        {
                            cts.Cancel();
                            goto Finished;
                        }

                        run = currentRun;
                        updateRunFromRepository = false;
                    }

                    run = await run.GetPartiallyUpdatedRun(_client, _k8sOptions, cancellationToken, job, pods.Values.ToList());

                    if (run.Final || run.Status is RunStatus.Succeeded or RunStatus.Failed or RunStatus.Canceled)
                    {
                        cts.Cancel();
                        goto Finished;
                    }
                }

Finished:
                ;
            }
            finally
            {
                // The job is finished or we encountered a failure.
                // Release the leaf merger that keeps the stream open
                leafMerger.Activate(cancellationToken);

                // These streams never end on their own, so terminate them.
                foreach (var terminableElements in terminablePipelineElements)
                {
                    terminableElements.Terminate();
                }

                try
                {
                    await combinedEnumerator.DisposeAsync();
                }
                catch (AggregateException e) when (cts.IsCancellationRequested && e.InnerExceptions.Any(ex => ex is OperationCanceledException))
                {
                }
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
                    pipeline.AddElement(new KubernetesTimestampedLogReformatter());
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
