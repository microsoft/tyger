using System.Collections.Immutable;
using System.Threading.Channels;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public class RunChangeFeed : BackgroundService
{
    private readonly IRepository _repository;
    private readonly ILogger<RunChangeFeed> _logger;

    private ImmutableArray<ChannelWriter<ObservedRunState>> _unfilteredObservers = [];

    private ImmutableDictionary<long, ImmutableArray<ChannelWriter<ObservedRunState>>> _filteredObservers = ImmutableDictionary<long, ImmutableArray<ChannelWriter<ObservedRunState>>>.Empty;

    public RunChangeFeed(IRepository repository, ILogger<RunChangeFeed> logger)
    {
        _repository = repository;
        _logger = logger;
    }

    public void RegisterObserver(ChannelWriter<ObservedRunState> observer)
    {
        ImmutableInterlocked.Update(ref _unfilteredObservers, list => list.Add(observer));
    }

    public void UnregisterObserver(ChannelWriter<ObservedRunState> observer)
    {
        ImmutableInterlocked.Update(ref _unfilteredObservers, list => list.Remove(observer));
    }

    public void RegisterRunObserver(long runId, ChannelWriter<ObservedRunState> observer)
    {
        ImmutableInterlocked.AddOrUpdate(ref _filteredObservers, runId, _ => [observer], (_, list) => list.Add(observer));
    }

    public void UnregisterRunObserver(long runId, ChannelWriter<ObservedRunState> observer)
    {
        ImmutableInterlocked.Update(ref _filteredObservers, d =>
        {
            if (d.TryGetValue(runId, out var arr))
            {
                var newArr = arr.Remove(observer);
                return newArr.IsEmpty ? d.Remove(runId) : d.SetItem(runId, newArr);
            }

            return d;
        });
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                await _repository.ListenForRunUpdates(async (run, ct) =>
                {
                    foreach (var observer in _unfilteredObservers)
                    {
                        await observer.WriteAsync(run, ct);
                    }

                    if (_filteredObservers.TryGetValue(run.Id, out var observers))
                    {
                        foreach (var observer in observers)
                        {
                            await observer.WriteAsync(run, ct);
                        }
                    }
                }, stoppingToken);
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
            }
            catch (Exception e)
            {
                _logger.ErrorListeningForRunCanges(e);
            }
        }
    }
}
