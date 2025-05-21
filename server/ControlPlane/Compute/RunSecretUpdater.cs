// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Runs;

namespace Tyger.ControlPlane.Compute;

/// <summary>
/// Manages background Tasks that refresh the buffer SAS URLs for active Runs.
/// </summary>
public class RunSecretUpdater : BackgroundService
{
    private readonly Repository _repository;
    private readonly IRunCreator _runCreator;
    private readonly ILogger<RunSecretUpdater> _logger;

    public RunSecretUpdater(IRunCreator runCreator, Repository repository, ILogger<RunSecretUpdater> logger)
    {
        _runCreator = runCreator;
        _repository = repository;
        _logger = logger;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                await Task.Delay(TimeSpan.FromSeconds(5), stoppingToken);
                await RefreshRunSecrets(stoppingToken);
            }
            catch (TaskCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception e)
            {
                _logger.ErrorInRunSecretUpdater(e);
            }
        }
    }

    protected async Task RefreshRunSecrets(CancellationToken stoppingToken)
    {
        var runs = await _repository.GetRunsReadyForSecretRefresh(stoppingToken);
        foreach (var run in runs)
        {
            try
            {
                if (!await _runCreator.UpdateRunSecret(run, stoppingToken))
                {
#pragma warning disable
                    _logger.LogInformation("Unable to update secret for run {runId}", run.Id!.Value);
#pragma warning enable
                    // Not updated, e.g. secret has been deleted
                    continue;
                }

                _logger.UpdatedRunSecret(run.Id!.Value);
            }
            catch (Exception e)
            {
                _logger.ErrorUpdatingRunSecret(e, run.Id!.Value);
            }
        }
    }
}
