// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.


using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public sealed class RunController : BackgroundService
{
    private readonly IRepository _repository;
    private readonly IRunCreator _runCreator;
    private readonly ILogger<RunController> _logger;

    public RunController(IRepository repository, IRunCreator runCreator, ILogger<RunController> logger)
    {
        _repository = repository;
        _runCreator = runCreator;
        _logger = logger;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                await _repository.ListenForNewRuns(ProcessPageOfNewRuns, stoppingToken);
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception ex)
            {
                _logger.ErrorListeningForNewRuns(ex);
            }
        }
    }

    private async Task ProcessPageOfNewRuns(IReadOnlyList<Run> runs, CancellationToken cancellationToken)
    {
        await Parallel.ForEachAsync(runs, cancellationToken, async (run, ct) =>
        {
            try
            {
                await _runCreator.CreateRun(run, ct);
            }
            catch (OperationCanceledException) when (ct.IsCancellationRequested)
            {
            }
            catch (Exception ex)
            {
                _logger.ErrorCreatingRunResources(run.Id!.Value, ex);
            }
        });
    }
}
