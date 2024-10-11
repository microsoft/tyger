using System.Diagnostics;
using System.Globalization;
using System.Text.RegularExpressions;
using System.Threading.Channels;
using k8s;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using static Tyger.ControlPlane.Compute.Kubernetes.KubernetesMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public partial class RunStateObserver : BackgroundService
{
    private readonly IKubernetes _kubernetesClient;
    private readonly IRepository _repository;
    private readonly ILoggerFactory _loggingFactory;
    private readonly KubernetesApiOptions _kubernetesOptions;
    private readonly ILogger<RunStateObserver> _logger;
    private readonly Dictionary<long, RunObjects> _runObjects = [];
    private Task? _jobInformerTask;
    private Task? _podInformerTask;
    private readonly CancellationTokenSource _cancellationTokenSource = new();
    private readonly Channel<(WatchEventType eventType, IKubernetesObject<V1ObjectMeta> resource)> _resourceUpdatesChannel = Channel.CreateBounded<(WatchEventType, IKubernetesObject<V1ObjectMeta>)>(new BoundedChannelOptions(1024));

    public RunStateObserver(IKubernetes kubernetesClient, IOptions<KubernetesApiOptions> kubernetesOptions, IRepository repository, ILoggerFactory loggingFactory)
    {
        _kubernetesClient = kubernetesClient;
        _repository = repository;
        _kubernetesOptions = kubernetesOptions.Value;
        _loggingFactory = loggingFactory;
        _logger = loggingFactory.CreateLogger<RunStateObserver>();
    }

    public override async Task StartAsync(CancellationToken cancellationToken)
    {
        var initialJobChannel = Channel.CreateBounded<V1Job>(new BoundedChannelOptions(1024));
        var initialPodChannel = Channel.CreateBounded<V1Pod>(new BoundedChannelOptions(1024));

        var jobUpdatesChannel = Channel.CreateBounded<(WatchEventType eventType, V1Job resource)>(new BoundedChannelOptions(1024));
        var podUpdatesChannel = Channel.CreateBounded<(WatchEventType eventType, V1Pod resource)>(new BoundedChannelOptions(1024));

        _ = Task.Run(async () =>
        {
            await foreach ((var id, var resource) in jobUpdatesChannel.Reader.ReadAllAsync(_cancellationTokenSource.Token))
            {
                await _resourceUpdatesChannel.Writer.WriteAsync((id, resource));
            }
        }, CancellationToken.None);

        _ = Task.Run(async () =>
        {
            await foreach ((var id, var resource) in podUpdatesChannel.Reader.ReadAllAsync(_cancellationTokenSource.Token))
            {
                await _resourceUpdatesChannel.Writer.WriteAsync((id, resource), _cancellationTokenSource.Token);
            }
        }, CancellationToken.None);

        var jobInformer = new JobInformer(_kubernetesClient, _kubernetesOptions.Namespace, RunLabel, initialJobChannel.Writer, jobUpdatesChannel.Writer, _loggingFactory.CreateLogger<JobInformer>());
        var podInformer = new PodInformer(_kubernetesClient, _kubernetesOptions.Namespace, RunLabel, initialPodChannel.Writer, podUpdatesChannel.Writer, _loggingFactory.CreateLogger<PodInformer>());

        _jobInformerTask = jobInformer.ExecuteAsync(_cancellationTokenSource.Token);
        _podInformerTask = podInformer.ExecuteAsync(_cancellationTokenSource.Token);

        var initialPopulationTask = Task.Run(async () =>
        {
            await foreach (var job in initialJobChannel.Reader.ReadAllAsync(_cancellationTokenSource.Token))
            {
                var id = GetRunId(job);
                if (id == null)
                {
                    continue;
                }

                _runObjects[id.Value] = new RunObjects(id.Value) { Job = job };
            }

            await foreach (var pod in initialPodChannel.Reader.ReadAllAsync(_cancellationTokenSource.Token))
            {
                var runId = GetRunId(pod);
                if (runId == null)
                {
                    continue;
                }

                if (!_runObjects.TryGetValue(runId.Value, out var runObjects))
                {
                    continue;
                }

                if (pod.GetLabel(WorkerLabel) is not null)
                {
                    runObjects.WorkerPods ??= [];
                    runObjects.WorkerPods[pod.Metadata.Name] = pod;
                }
                else
                {
                    runObjects.JobPods[pod.Metadata.Name] = pod;
                }
            }

            await Parallel.ForEachAsync(
                _runObjects.Values,
                new ParallelOptions { MaxDegreeOfParallelism = 10, CancellationToken = cancellationToken },
                async (runObjects, ct) =>
                {
                    var observedState = runObjects.GetObservedState();
                    await _repository.UpdateRunFromObservedState(observedState, ct); // TODO: handle failure
                });

        }, CancellationToken.None);

        await Task.WhenAny(_jobInformerTask, _podInformerTask, initialPopulationTask);
        await base.StartAsync(cancellationToken);
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        stoppingToken.Register(_cancellationTokenSource.Cancel);

        async Task ProcessUpdates()
        {
            await foreach ((var eventType, var resource) in _resourceUpdatesChannel.Reader.ReadAllAsync(stoppingToken))
            {
                var runId = GetRunId(resource);
                if (runId == null)
                {
                    continue;
                }

                if (!_runObjects.TryGetValue(runId.Value, out var runObjects))
                {
                    runObjects = new RunObjects(runId.Value);
                    _runObjects[runId.Value] = runObjects;
                }

                if (resource is V1Job job)
                {
                    runObjects.Job = job;
                    if (eventType == WatchEventType.Deleted)
                    {
                        _runObjects.Remove(runId.Value);
                    }
                }
                else if (resource is V1Pod pod)
                {
                    if (pod.GetLabel(WorkerLabel) is not null)
                    {
                        runObjects.WorkerPods ??= [];
                        runObjects.WorkerPods[pod.Metadata.Name] = pod;
                    }
                    else
                    {
                        runObjects.JobPods[pod.Metadata.Name] = pod;
                    }
                }

                var previousState = runObjects.CachedMetadata;
                var currentState = runObjects.GetObservedState();
                if (!previousState.Equals(currentState))
                {
                    await _repository.UpdateRunFromObservedState(currentState, stoppingToken); // TODO: handle failure
                }
            }
        }

        var processUpdatesTask = ProcessUpdates();

        // fail if any fail
        await await Task.WhenAny(_jobInformerTask!, _podInformerTask!, processUpdatesTask);

        await _jobInformerTask!;
        await _podInformerTask!;
        await processUpdatesTask;
    }

    private static long? GetRunId(IKubernetesObject<V1ObjectMeta> job)
    {
        if (job.GetLabel(RunLabel) is string runIdString && long.TryParse(runIdString, out var runId))
        {
            return runId;
        }

        return null;
    }

    private sealed partial class RunObjects
    {
        public RunObjects(long id)
        {
            Id = id;
        }

        public long Id { get; private init; }

        public V1Job? Job { get; set; }

        public Dictionary<string, V1Pod> JobPods { get; set; } = [];
        public Dictionary<string, V1Pod>? WorkerPods { get; set; }

        public ObservedRunState CachedMetadata { get; private set; }

        public ObservedRunState GetObservedState()
        {
            var metadata = GetStatus();
            var (jobNodePool, workerNodePool) = GetNodePools();
            if (jobNodePool != null || workerNodePool != null)
            {
                metadata = metadata with { JobNodePool = jobNodePool, WorkerNodePool = workerNodePool };
            }

            return CachedMetadata = metadata;
        }

        private ObservedRunState GetStatus()
        {
            if (Job == null)
            {
                return new(Id, RunStatus.Failed) { StatusReason = "Job not found" };
            }

            if (GetFailureTimeAndReason() is (var failureTime, var reason))
            {
                return new(Id, RunStatus.Failed)
                {
                    StatusReason = reason,
                    FinishedAt = failureTime,
                };
            }

            if (GetSuccessTime() is DateTimeOffset successTime)
            {
                return new(Id, RunStatus.Succeeded)
                {
                    FinishedAt = successTime,
                };
            }

            var runningCount = JobPods.Values.Count(p => p.Status?.Phase == "Running");

            // Note that the job object may not yet reflect the status of the pods.
            // It could be that pods have succeeeded or failed without the job reflecting this.
            // We want to avoid returning a pending state if no pods are running because they have
            // all exited but the job hasn't been updated yet.
            var isRunning = JobPods.Values.Any(p => p.Status?.Phase is "Running" or "Succeeded" or "Failed");
            if (isRunning)
            {
                return new(Id, RunStatus.Running) { RunningCount = runningCount };
            }

            return new(Id, RunStatus.Pending);
        }

        private (DateTimeOffset, string)? GetFailureTimeAndReason()
        {
            Debug.Assert(Job != null);

            var failureCondition = Job.Status?.Conditions?.FirstOrDefault(c => c.Type == "Failed" && c.Status == "True");
            if (failureCondition != null)
            {
                return (failureCondition.LastTransitionTime!.Value, failureCondition.Reason);
            }

            if (Job.GetAnnotation(HasSocketAnnotation) == "true")
            {
                var containerStatus = JobPods.Values
                    .Where(p => p.Status?.ContainerStatuses != null)
                    .SelectMany(p => p.Status.ContainerStatuses)
                    .Where(cs => cs.State?.Terminated?.ExitCode is not null and not 0)
                    .MinBy(cs => cs.State.Terminated.FinishedAt!.Value);

                if (containerStatus != null)
                {
                    var reason = $"{(containerStatus.Name == "main" ? "Main" : "Sidecar")} exited with code {containerStatus.State.Terminated.ExitCode}";
                    return (containerStatus.State.Terminated.FinishedAt!.Value, reason);
                }
            }

            return null;
        }

        private DateTimeOffset? GetSuccessTime()
        {
            Debug.Assert(Job != null);

            bool succeeeded = false;
            if (Job.Status?.Conditions?.Any(c => c.Type == "Complete" && c.Status == "True") == true)
            {
                succeeeded = true;
            }
            else
            {
                succeeeded = Enumerable.Range(0, Job.Spec.Completions ??= 1)
                    .GroupJoin(JobPods.Values, i => i, GetJobCompletionIndex, (i, p) => (i, p))
                    .All(g => g.p.Any(p => p.Status?.Phase == "Succeeded"));
            }

            if (succeeeded)
            {
                var finishedTime = JobPods.Values
                        .Where(p => p.Status.Phase == "Succeeded")
                        .Select(p => p.Status.ContainerStatuses?.Single(c => c.Name == "main").State.Terminated?.FinishedAt)
                        .Where(t => t != null)
                        .Max();

                return finishedTime ?? Job.CreationTimestamp();
            }

            if (Job.GetAnnotation(HasSocketAnnotation) == "true")
            {
                // the main container may still be running but if all sidecars have exited successfully, then we consider it complete.
                if (JobPods.Values.All(pod =>
                        pod.Spec.Containers.All(c =>
                            pod.Status?.ContainerStatuses?.Any(cs =>
                                cs.Name == c.Name &&
                                (cs.Name == "main"
                                    ? cs.State.Running != null
                                    : cs.State.Terminated?.ExitCode == 0)) == true)))
                {
                    var finishedTime = JobPods.Values.SelectMany(p => p.Status.ContainerStatuses).Select(cs => cs.State.Terminated?.FinishedAt).Where(t => t != null).Max();
                    return finishedTime ?? Job.CreationTimestamp();
                }
            }

            return null;
        }

        private static int GetJobCompletionIndex(V1Pod pod)
        {
            if (!int.TryParse(pod.Metadata.Annotations?["batch.kubernetes.io/job-completion-index"], CultureInfo.InvariantCulture, out var index))
            {
                throw new InvalidOperationException($"Pod {pod.Metadata.Name} is missing the job-completion-index annotation");
            }

            return index;
        }

        private (string? jobNodePool, string? workerNodePool) GetNodePools()
        {
            static string GetNodePoolFromNodeName(string nodeName)
            {
                var match = NodePoolFromNodeNameRegex().Match(nodeName);
                if (!match.Success)
                {
                    throw new InvalidOperationException($"Node name in unexpected format: '{nodeName}'");
                }

                return match.Groups[1].Value;
            }

            static string GetNodePool(IReadOnlyCollection<V1Pod> pods)
            {
                return string.Join(
                    ",",
                    pods.Select(p => p.Spec.NodeName).Where(n => !string.IsNullOrEmpty(n)).Select(GetNodePoolFromNodeName).Distinct());
            }

            var jobNodePool = GetNodePool(JobPods.Values);
            var workerNodePool = WorkerPods != null ? GetNodePool(WorkerPods.Values) : null;

            return (jobNodePool, workerNodePool);
        }

        // Used to extract "gpunp" from an AKS node named "aks-gpunp-23329378-vmss000007"
        [GeneratedRegex(@"^aks-([^\-]+)-", RegexOptions.Compiled)]
        private static partial Regex NodePoolFromNodeNameRegex();
    }
}
