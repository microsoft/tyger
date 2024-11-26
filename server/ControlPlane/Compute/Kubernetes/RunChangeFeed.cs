// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Collections.Immutable;
using System.Threading.Channels;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Compute.Kubernetes;

/// <summary>
/// Publishes a feed of run state changes to observers. The data is sourced from the database.
/// Updates can arrive out of order and may be duplicated.
/// </summary>
public class RunChangeFeed : BackgroundService
{
    private readonly Repository _repository;
    private readonly ILogger<RunChangeFeed> _logger;

    private ImmutableArray<ChannelWriter<ObservedRunState>> _unfilteredObservers = [];

    private ImmutableDictionary<long, ImmutableArray<ChannelWriter<ObservedRunState>>> _filteredObservers = ImmutableDictionary<long, ImmutableArray<ChannelWriter<ObservedRunState>>>.Empty;

    public RunChangeFeed(Repository repository, ILogger<RunChangeFeed> logger)
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
        DateTimeOffset? latestModifiedAt = null;
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                await _repository.ListenForRunUpdates(
                    latestModifiedAt?.Add(TimeSpan.FromSeconds(-10)), // changes can come out of order, so we restart the query a bit before the last observed change
                    async (run, ct) =>
                    {
                        if (run.DatabaseUpdatedAt is not null && (latestModifiedAt is null || run.DatabaseUpdatedAt > latestModifiedAt))
                        {
                            latestModifiedAt = run.DatabaseUpdatedAt;
                        }

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
                    },
                    stoppingToken);
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
