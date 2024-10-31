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
    private const string LeaseName = "run-state-observer";

    private readonly IKubernetes _kubernetesClient;
    private readonly IRepository _repository;
    private readonly ILoggerFactory _loggingFactory;
    private readonly KubernetesApiOptions _kubernetesOptions;
    private readonly Dictionary<long, RunObjects> _cache = [];
    private readonly Dictionary<long, List<ChannelWriter<(RunObjects, WatchEventType, V1Pod)>>> _listeners = [];
    private Task? _podInformerTask;
    private readonly CancellationTokenSource _cancellationTokenSource = new();
    private readonly Channel<(WatchEventType eventType, V1Pod resource)> _podUpdatesChannel = Channel.CreateBounded<(WatchEventType, V1Pod)>(new BoundedChannelOptions(10240));
    private readonly Channel<int> _onLeaseOwnershipAcquiredChannel = Channel.CreateUnbounded<int>();

    private int _latestLeaseToken = 1;
    private int _acquiredLeaseToken;
    private Task? _acquireAndHoldLeaseTask;
    private readonly string _thisLeaseHolderId = Environment.MachineName;

    public RunStateObserver(IKubernetes kubernetesClient, IOptions<KubernetesApiOptions> kubernetesOptions, IRepository repository, ILoggerFactory loggingFactory)
    {
        _kubernetesClient = kubernetesClient;
        _repository = repository;
        _kubernetesOptions = kubernetesOptions.Value;
        _loggingFactory = loggingFactory;
    }

    public override async Task StartAsync(CancellationToken cancellationToken)
    {
        _acquireAndHoldLeaseTask = _repository.AcquireAndHoldLease(LeaseName, _thisLeaseHolderId, async hasLease =>
            {
                var incrementedLeaseToken = Interlocked.Increment(ref _latestLeaseToken);
                if (hasLease)
                {
                    await _onLeaseOwnershipAcquiredChannel.Writer.WriteAsync(incrementedLeaseToken);
                }
            }, _cancellationTokenSource.Token);

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
                while (_onLeaseOwnershipAcquiredChannel.Reader.TryRead(out var acquiredLeaseToken))
                {
                    await OnAcquireLease(acquiredLeaseToken, stoppingToken);
                }

                while (!_onLeaseOwnershipAcquiredChannel.Reader.TryPeek(out _) && _podUpdatesChannel.Reader.TryRead(out var update))
                {
                    await OnPodUpdated(update.eventType, update.resource, stoppingToken);
                }

                await Task.WhenAny(_onLeaseOwnershipAcquiredChannel.Reader.WaitToReadAsync(stoppingToken).AsTask(), _podUpdatesChannel.Reader.WaitToReadAsync(stoppingToken).AsTask());
            }
        }

        var processUpdatesTask = ProcessUpdates();

        // fail if any fail
        await await Task.WhenAny(_podInformerTask!, processUpdatesTask);

        await _podInformerTask!;
        await processUpdatesTask;
        await _acquireAndHoldLeaseTask!;
    }

    private async Task OnAcquireLease(int acquiredLeaseToken, CancellationToken cancellationToken)
    {
        await Parallel.ForEachAsync(
            _cache.Values,
            new ParallelOptions { MaxDegreeOfParallelism = 10, CancellationToken = cancellationToken },
            async (runObjects, ct) =>
            {
                if (acquiredLeaseToken == _latestLeaseToken)
                {
                    var observedState = runObjects.GetObservedState();
                    await _repository.UpdateRunFromObservedState(observedState, (LeaseName, _thisLeaseHolderId), ct);
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

        if (_acquiredLeaseToken == _latestLeaseToken)
        {
            var previousState = runObjects.CachedMetadata;
            var currentState = runObjects.GetObservedState();

            if (!previousState.Equals(currentState))
            {
                await _repository.UpdateRunFromObservedState(currentState, (LeaseName, _thisLeaseHolderId), stoppingToken);
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
