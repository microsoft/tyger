// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Globalization;
using System.Net;
using System.Threading.Channels;
using k8s;
using k8s.Autorest;
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
    private readonly Repository _repository;
    private readonly ILogArchive _logArchive;
    private readonly ILoggerFactory _loggerFactory;
    private readonly RunStateObserver _runStateObserver;
    private readonly KubernetesApiOptions _k8sOptions;

    public KubernetesRunLogReader(
        k8s.Kubernetes client,
        Repository repository,
        IOptions<KubernetesApiOptions> k8sOptions,
        ILogArchive logArchive,
        ILoggerFactory loggerFactory,
        RunStateObserver runStateObserver)
    {
        _client = client;
        _repository = repository;
        _logArchive = logArchive;
        _loggerFactory = loggerFactory;
        _runStateObserver = runStateObserver;
        _k8sOptions = k8sOptions.Value;
    }

    public async Task<Pipeline?> GetLogs(long runId, GetLogsOptions options, CancellationToken cancellationToken)
    {
        if (await _repository.GetRun(runId, cancellationToken) is not var (run, _, logsArchivedAt, _, _))
        {
            return null;
        }

        async Task<Pipeline?> InnerGetLogs()
        {
            if (logsArchivedAt is not null)
            {
                return await _logArchive.GetLogs(runId, options, cancellationToken);
            }

            if (!options.Follow || run.Status.IsTerminal())
            {
                return await GetLogsSnapshot(run, options, cancellationToken);
            }

            return await FollowLogs(run, options, cancellationToken);
        }

        return (await InnerGetLogs()) ?? new([]);
    }

    private Task<Pipeline> FollowLogs(Run run, GetLogsOptions options, CancellationToken cancellationToken)
    {
        var channel = Channel.CreateUnbounded<(RunObjects, WatchEventType, V1Pod)>();
        RunObjects? currentRunObjects = _runStateObserver.RegisterRunObjectsListener(run.Id!.Value, channel.Writer);

        var followingContainers = new HashSet<(string podName, string containerName)>();
        var initialPipelines = new List<IPipelineSource>();
        var terminablePipelineElements = new List<TerminablePipelineElement>();

        void TrackPipelineIfNeedsTermination(V1Pod pod, string containerName, Pipeline podPipeline)
        {
            if (pod.GetLabel(WorkerLabel) != null || (pod.GetLabel(JobLabel) != null && containerName == MainContainerName && pod.GetAnnotation(HasSocketAnnotation) == "true"))
            {
                var terminableElement = new TerminablePipelineElement();
                terminablePipelineElements.Add(terminableElement);
                podPipeline.AddElement(terminableElement);
            }
        }

        var terminableSocketContainers = new Dictionary<(V1Pod, string), TerminablePipelineElement>();
        var pods = new Dictionary<string, V1Pod>();

        if (currentRunObjects is not null)
        {
            foreach (var pod in currentRunObjects.JobPods.Concat(currentRunObjects.WorkerPods))
            {
                if (pod == null)
                {
                    continue;
                }

                pods[pod!.Name()] = pod;
                foreach (var container in pod!.Spec.Containers)
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
        }

        var syncRoot = new object();
        var leafMerger = new LiveLogMerger();
        LiveLogMerger rootMerger;
        if (initialPipelines.Count > 0)
        {
            rootMerger = new LiveLogMerger();
            rootMerger.Activate(cancellationToken, [leafMerger, .. initialPipelines]);
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

        var podWatchEnumerator = channel.Reader.ReadAllAsync(cancellationToken).GetAsyncEnumerator(cancellationToken);

        _ = WatchResources();

        return Task.FromResult(pipeline);

        async Task WatchResources()
        {
            try
            {
                while (await podWatchEnumerator.MoveNextAsync())
                {
                    (RunObjects runObjects, WatchEventType watchEventType, V1Pod pod) = podWatchEnumerator.Current;

                    switch (watchEventType)
                    {
                        case WatchEventType.Added:
                        case WatchEventType.Modified:
                            pods[pod.Name()] = pod;

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

                                    TrackPipelineIfNeedsTermination(pod, container.Name, podPipeline);

                                    // a new pod has started. Merge its log in by starting the existing waiting leaf merger
                                    // and create a new leaf merger for the next pod.
                                    var newLeaf = new LiveLogMerger();
                                    leafMerger.Activate(cancellationToken, newLeaf, podPipeline);
                                    leafMerger = newLeaf;
                                }
                            }

                            break;
                        case WatchEventType.Deleted:
                            pods.Remove(pod.Name());
                            break;
                    }

                    var observedState = runObjects.GetObservedState();
                    if (observedState.Status.IsTerminal())
                    {
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
                    await podWatchEnumerator.DisposeAsync();
                }
                catch (AggregateException e) when (cancellationToken.IsCancellationRequested && e.InnerExceptions.Any(ex => ex is OperationCanceledException))
                {
                }
            }
        }
    }

    private static bool IsContainerRunningOrTerminated(V1Pod pod, V1Container container)
    {
        return pod.Status?.ContainerStatuses?.SingleOrDefault(s => s.Name == container.Name) is { State.Waiting: null };
    }

    private async Task<Pipeline?> GetLogsSnapshot(Run run, GetLogsOptions options, CancellationToken cancellationToken)
    {
        var jobPodNames = Enumerable.Range(0, run.Job.Replicas).Select(i => JobPodName(run.Id!.Value, i)).ToList();
        var workerPodNames = run.Worker is not null ? Enumerable.Range(0, run.Worker.Replicas).Select(i => WorkerPodName(run.Id!.Value, i)).ToList() : [];

        List<string>? jobContainerNames = null;
        List<string>? workerContainerNames = null;

        if (_runStateObserver.TryGetRunObjectSnapshot(run.Id!.Value, out var runObjects))
        {
            jobContainerNames = runObjects!.JobPods.FirstOrDefault(p => p != null)?.Spec.Containers.Select(c => c.Name).ToList();
            workerContainerNames = runObjects!.WorkerPods?.FirstOrDefault(p => p != null)?.Spec.Containers.Select(c => c.Name).ToList();
        }

        if (jobContainerNames is null)
        {
            foreach (var jobPodName in jobPodNames)
            {
                try
                {
                    var jobPod = await _client.CoreV1.ReadNamespacedPodAsync(jobPodName, _k8sOptions.Namespace, cancellationToken: cancellationToken);
                    jobContainerNames = jobPod.Spec.Containers.Select(c => c.Name).ToList();
                    break;
                }
                catch (HttpOperationException e) when (e.Response.StatusCode == HttpStatusCode.NotFound)
                {
                }
            }
        }

        if (workerContainerNames is null && workerPodNames.Count > 0)
        {
            foreach (var workerPodName in workerPodNames)
            {
                try
                {
                    var workerPod = await _client.CoreV1.ReadNamespacedPodAsync(workerPodName, _k8sOptions.Namespace, cancellationToken: cancellationToken);
                    workerContainerNames = workerPod.Spec.Containers.Select(c => c.Name).ToList();
                    break;
                }
                catch (HttpOperationException e) when (e.Response.StatusCode == HttpStatusCode.NotFound)
                {
                }
            }
        }

        var jobsAndContainers = jobContainerNames is not null ? jobPodNames.SelectMany(j => jobContainerNames.Select(c => (j, c))) : [];
        var workersAndContainers = workerContainerNames is not null ? workerPodNames.SelectMany(j => workerContainerNames.Select(c => (j, c))) : [];

        List<(string pod, string container)> podAndContainers = [.. jobsAndContainers, .. workersAndContainers];

        var podLogOptions = options with { Follow = false };

        if (podAndContainers is [(var singlePod, var singleContainer)])
        {
            // simple case where no merging or transforming is required.
            return await GetLogsFromPod(singlePod, singleContainer, GetPrefix(run, singlePod, singleContainer, podAndContainers), podLogOptions, cancellationToken);
        }

        podLogOptions = podLogOptions with { IncludeTimestamps = true };

        var pipelines = await podAndContainers
            .ToAsyncEnumerable()
            .Select(async (pc, ct) => (await GetLogsFromPod(pc.pod, pc.container, GetPrefix(run, pc.pod, pc.container, podAndContainers), podLogOptions, ct))!)
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
        static string? ContainerPrefix(V1Container container, bool singleContainer)
        {
            return singleContainer ? null : $"[{container?.Name}]";
        }

        return string.Concat(PodPrefix(run, pod.Name()), ContainerPrefix(container, pod.Spec.Containers.Count == 1));
    }

    private static string? GetPrefix(Run run, string podName, string containerName, List<(string pod, string container)> podAndContainers)
    {
        static string? ContainerPrefix(string podName, string containerName, List<(string pod, string container)> podAndContainers)
        {
            return podAndContainers.Count(pc => pc.pod == podName) switch
            {
                1 => null,
                _ => $"[{containerName}]"
            };
        }

        return string.Concat(PodPrefix(run, podName), ContainerPrefix(podName, containerName, podAndContainers));
    }

    private static string? PodPrefix(Run run, string podName)
    {
        var totalReplicas = run.Job.Replicas + (run.Worker?.Replicas ?? 0);
        if (totalReplicas == 1)
        {
            return null;
        }

        if (IsWorkerPodName(podName))
        {
            return run.Worker!.Replicas is > 1 ? $"[{RemoveRunPrefix(podName)}]" : "[worker]";
        }

        return run.Job.Replicas is > 1 ? $"[{RemoveRunPrefix(podName)}]" : "[job]";
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

        var url = new Uri(_client.BaseUri, $"api/v1/namespaces/{_k8sOptions.Namespace}/pods/{podName}/log{qs.ToUriComponent()}");

        var requestMessage = new HttpRequestMessage(HttpMethod.Get, url);
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
