using System.Threading.Channels;
using k8s;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using static Tyger.ControlPlane.Compute.Kubernetes.KubernetesMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public class RunStateObserver : BackgroundService
{
    private readonly IKubernetes _kubernetesClient;
    private readonly IRepository _repository;
    private readonly ILoggerFactory _loggingFactory;
    private readonly KubernetesApiOptions _kubernetesOptions;
    private readonly ILogger<RunStateObserver> _logger;
    private readonly Dictionary<long, RunObjects> _cache = [];
    private readonly Dictionary<long, List<ChannelWriter<(RunObjects, WatchEventType, V1Pod)>>> _listeners = [];
    private Task? _podInformerTask;
    private readonly CancellationTokenSource _cancellationTokenSource = new();
    private readonly Channel<(WatchEventType eventType, V1Pod resource)> _podUpdatesChannel = Channel.CreateBounded<(WatchEventType, V1Pod)>(new BoundedChannelOptions(1024));

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

            await Parallel.ForEachAsync(
                _cache.Values,
                new ParallelOptions { MaxDegreeOfParallelism = 10, CancellationToken = cancellationToken },
                async (runObjects, ct) =>
                {
                    var observedState = runObjects.GetObservedState();
                    await _repository.UpdateRunFromObservedState(observedState, ct); // TODO: handle failure
                });
        }, CancellationToken.None);

        await Task.WhenAny(_podInformerTask, initialPopulationTask);
        await base.StartAsync(cancellationToken);
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

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        stoppingToken.Register(_cancellationTokenSource.Cancel);

        async Task ProcessUpdates()
        {
            await foreach ((var eventType, var pod) in _podUpdatesChannel.Reader.ReadAllAsync(stoppingToken))
            {
                var runId = GetRunId(pod);
                if (runId == null)
                {
                    continue;
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

                    continue;
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

                var previousState = runObjects.CachedMetadata;
                var currentState = runObjects.GetObservedState();
                if (!previousState.Equals(currentState))
                {
                    await _repository.UpdateRunFromObservedState(currentState, stoppingToken); // TODO: handle failure
                }

                if (listeners != null)
                {
                    foreach (var listener in listeners)
                    {
                        await listener.WriteAsync((runObjects!, eventType, pod), stoppingToken);
                    }
                }
            }
        }

        var processUpdatesTask = ProcessUpdates();

        // fail if any fail
        await await Task.WhenAny(_podInformerTask!, processUpdatesTask);

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
}
