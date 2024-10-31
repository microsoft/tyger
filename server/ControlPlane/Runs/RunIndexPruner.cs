// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Database;

namespace Tyger.ControlPlane.Runs;

/// <summary>
/// Sets modified_at to null for runs that have been finalized for some time to reduce the size of the index.
/// </summary>
public class RunIndexPruner : BackgroundService
{
    private readonly IRepository _repository;
    private readonly ILogger<RunIndexPruner> _logger;

    public RunIndexPruner(IRepository repository, ILogger<RunIndexPruner> logger)
    {
        _repository = repository;
        _logger = logger;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                var timestamp = DateTimeOffset.UtcNow;
                await Task.Delay(TimeSpan.FromMinutes(5), stoppingToken);
                await _repository.PruneRunModifedAtIndex(timestamp, stoppingToken);
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception e)
            {
                _logger.ErrorDuringBackgroundIndexPrune(e);
            }
        }
    }
}
