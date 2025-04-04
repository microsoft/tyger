// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Threading.Channels;
using k8s;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Database;
using static Tyger.ControlPlane.Compute.Kubernetes.KubernetesMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

/// <summary>
/// Observes the state of runs in the Kubernetes cluster and updates the database with the observed state.
/// </summary>
public class RunStateObserver : BackgroundService
{
    private const int PartitionCount = 128;
    private const int ParitionChannelSize = 1024;

    private readonly IKubernetes _kubernetesClient;
    private readonly Repository _repository;
    private readonly LeaseManager _leaseManager;
    private readonly ILoggerFactory _loggingFactory;
    private readonly ILogger<RunStateObserver> _logger;
    private readonly KubernetesApiOptions _kubernetesOptions;
    private readonly Dictionary<long, RunObjects> _cache = [];
    private readonly Dictionary<long, List<ChannelWriter<(RunObjects, WatchEventType, V1Pod)>>> _listeners = [];
    private readonly List<Channel<(WatchEventType, V1Pod)>> _partitionedPodUpdateChannels = [.. Enumerable.Range(0, PartitionCount).Select(_ => Channel.CreateBounded<(WatchEventType, V1Pod)>(ParitionChannelSize))];
    private Task? _podInformerTask;
    private readonly CancellationTokenSource _cancellationTokenSource = new();
    private readonly Channel<(WatchEventType eventType, V1Pod resource)> _podUpdatesChannel = Channel.CreateBounded<(WatchEventType, V1Pod)>(PartitionCount * ParitionChannelSize);
    private readonly Channel<(bool leaseHeld, int token)> _onLeaseOwnershipAcquiredChannel = Channel.CreateUnbounded<(bool, int)>();
    private int _acquiredLeaseToken;
    private readonly string _thisLeaseHolderId = Environment.MachineName;

    public RunStateObserver(IKubernetes kubernetesClient, IOptions<KubernetesApiOptions> kubernetesOptions, Repository repository, LeaseManager leaseManager, ILoggerFactory loggingFactory)
    {
        _kubernetesClient = kubernetesClient;
        _repository = repository;
        _leaseManager = leaseManager;
        _kubernetesOptions = kubernetesOptions.Value;
        _loggingFactory = loggingFactory;
        _logger = loggingFactory.CreateLogger<RunStateObserver>();

        leaseManager.RegisterListener(_onLeaseOwnershipAcquiredChannel.Writer);
    }

    public override async Task StartAsync(CancellationToken cancellationToken)
    {
        var initialPodChannel = Channel.CreateBounded<V1Pod>(new BoundedChannelOptions(1024));

        var podInformer = new PodInformer(_kubernetesClient, _kubernetesOptions.Namespace, RunLabel, initialPodChannel.Writer, _podUpdatesChannel.Writer, _loggingFactory.CreateLogger<PodInformer>());

        _podInformerTask = podInformer.ExecuteAsync(_cancellationTokenSource.Token);

        var initialPopulationTask = Task.Run(async () =>
        {
            // Not locking here because we are in the startup phase

            await foreach (var pod in initialPodChannel.Reader.ReadAllAsync(_cancellationTokenSource.Token))
            {
                var runId = GetRunId(pod);
                if (runId == null)
                {
                    continue;
                }

                if (!_cache.TryGetValue(runId.Value, out var runObjects))
                {
                    var (jobReplicaCount, workerReplicaCount) = pod.GetReplicaCounts();
                    runObjects = new RunObjects(runId.Value, jobReplicaCount, workerReplicaCount);
                    _cache[runId.Value] = runObjects;
                }

                var index = IndexFromPodName(pod.Name());
                if (pod.GetLabel(WorkerLabel) is not null)
                {
                    runObjects.WorkerPods[index] = pod;
                }
                else
                {
                    runObjects.JobPods[index] = pod;
                }
            }
        }, CancellationToken.None);

        await Task.WhenAny(_podInformerTask, initialPopulationTask);
        await base.StartAsync(cancellationToken);
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        stoppingToken.Register(_cancellationTokenSource.Cancel);

        async Task ProcessUpdates()
        {
            while (!stoppingToken.IsCancellationRequested)
            {
                while (_onLeaseOwnershipAcquiredChannel.Reader.TryRead(out var leaseHeldAndToken))
                {
                    if (leaseHeldAndToken.leaseHeld)
                    {
                        await OnAcquireLease(leaseHeldAndToken.token, stoppingToken);
                    }
                }

                while (!_onLeaseOwnershipAcquiredChannel.Reader.TryPeek(out _) && _podUpdatesChannel.Reader.TryRead(out var update))
                {
                    var runIdString = update.resource.GetLabel(RunLabel);
                    int partitionIndex = int.Parse(runIdString) % PartitionCount;
                    await _partitionedPodUpdateChannels[partitionIndex].Writer.WriteAsync(update, stoppingToken);
                }

                await Task.WhenAny(_onLeaseOwnershipAcquiredChannel.Reader.WaitToReadAsync(stoppingToken).AsTask(), _podUpdatesChannel.Reader.WaitToReadAsync(stoppingToken).AsTask());
            }
        }

        var processUpdatesTask = ProcessUpdates();

        var partitionedProcessors = Enumerable.Range(0, PartitionCount).Select(async partitionIndex =>
        {
            var channel = _partitionedPodUpdateChannels[partitionIndex];
            await foreach (var (eventType, pod) in channel.Reader.ReadAllAsync(stoppingToken))
            {
                var count = channel.Reader.Count;
                if (count > 100)
                {
                    _logger.RunStateObserverHighQueueLength(partitionIndex, count);
                }

                await OnPodUpdated(eventType, pod, stoppingToken);
            }
        }).ToList();

        var allTasks = new List<Task>(partitionedProcessors) { processUpdatesTask, _podInformerTask! };

        // fail if any fail
        await await Task.WhenAny(allTasks);

        foreach (var task in allTasks)
        {
            await task;
        }
    }

    private async Task OnAcquireLease(int acquiredLeaseToken, CancellationToken cancellationToken)
    {
        await Parallel.ForEachAsync(
            _cache.Values,
            new ParallelOptions { MaxDegreeOfParallelism = 10, CancellationToken = cancellationToken },
            async (runObjects, ct) =>
            {
                if (acquiredLeaseToken == _leaseManager.GetCurrentLeaseToken())
                {
                    var observedState = runObjects.GetObservedState();
                    await _repository.UpdateRunFromObservedState(observedState, (_leaseManager.LeaseName, _thisLeaseHolderId), ct);
                }
            });

        _acquiredLeaseToken = acquiredLeaseToken;
    }

    private async Task OnPodUpdated(WatchEventType eventType, V1Pod pod, CancellationToken stoppingToken)
    {
        var runId = GetRunId(pod);
        if (runId == null)
        {
            return;
        }

        List<ChannelWriter<(RunObjects, WatchEventType, V1Pod)>>? listeners;
        RunObjects? runObjects;
        lock (_cache)
        {
            _cache.TryGetValue(runId.Value, out runObjects);
            _listeners.TryGetValue(runId.Value, out listeners);
        }

        if (eventType == WatchEventType.Deleted)
        {
            if (runObjects != null)
            {
                if (pod.GetLabel(WorkerLabel) is not null)
                {
                    runObjects.WorkerPods[IndexFromPodName(pod.Name())] = null;
                }
                else
                {
                    runObjects.JobPods[IndexFromPodName(pod.Name())] = null;
                }

                if (runObjects.JobPods.All(p => p == null) &&
                    runObjects.WorkerPods.All(p => p == null))
                {
                    lock (_cache)
                    {
                        _cache.Remove(runId.Value);
                    }
                }

                if (listeners != null)
                {
                    foreach (var listener in listeners)
                    {
                        await listener.WriteAsync((runObjects, eventType, pod), stoppingToken);
                    }
                }
            }

            return;
        }

        if (runObjects == null)
        {
            var (jobReplicaCount, workerReplicaCount) = pod.GetReplicaCounts();
            runObjects = new RunObjects(runId.Value, jobReplicaCount, workerReplicaCount);
            lock (_cache)
            {
                _cache[runId.Value] = runObjects;
            }
        }

        if (pod.GetLabel(WorkerLabel) is not null)
        {
            runObjects.WorkerPods[IndexFromPodName(pod.Name())] = pod;
        }
        else
        {
            runObjects.JobPods[IndexFromPodName(pod.Name())] = pod;
        }

        if (_acquiredLeaseToken == _leaseManager.GetCurrentLeaseToken())
        {
            var previousState = runObjects.CachedMetadata;
            var currentState = runObjects.GetObservedState();

            // Pending is state when the run is created, so no need to update it.
            if (currentState.Status != Model.RunStatus.Pending && !previousState.Equals(currentState))
            {
                await _repository.UpdateRunFromObservedState(currentState, (_leaseManager.LeaseName, _thisLeaseHolderId), stoppingToken);
            }
        }

        if (listeners != null)
        {
            foreach (var listener in listeners)
            {
                await listener.WriteAsync((runObjects!, eventType, pod), stoppingToken);
            }
        }
    }

    public bool TryGetRunObjectSnapshot(long runId, out RunObjects? runObjects)
    {
        bool res;
        lock (_cache)
        {
            res = _cache.TryGetValue(runId, out runObjects);
        }

        if (res)
        {
            runObjects = runObjects!.Clone();
        }

        return res;
    }

    public RunObjects? RegisterRunObjectsListener(long runId, ChannelWriter<(RunObjects, WatchEventType, V1Pod)> listener)
    {
        RunObjects? runObjects;
        lock (_cache)
        {
            _cache.TryGetValue(runId, out runObjects);
            if (!_listeners.TryGetValue(runId, out var listeners))
            {
                listeners = [];
                _listeners[runId] = listeners;
            }

            listeners.Add(listener);
        }

        return runObjects;
    }

    public void UnregisterRunObjectsListener(long runId, ChannelWriter<(RunObjects, WatchEventType, V1Pod)> listener)
    {
        lock (_cache)
        {
            if (_listeners.TryGetValue(runId, out var listeners))
            {
                if (listeners.Remove(listener) && listeners.Count == 0)
                {
                    _listeners.Remove(runId);
                }
            }
        }
    }

    private static long? GetRunId(IKubernetesObject<V1ObjectMeta> job)
    {
        if (job.GetLabel(RunLabel) is string runIdString && long.TryParse(runIdString, out var runId))
        {
            return runId;
        }

        return null;
    }
}
