// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.


using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public sealed class RunController : IHostedService, IDisposable
{
    private CancellationTokenSource? _backgroundCancellationTokenSource;
    private Task? _backgroundTask;
    private readonly IRepository _repository;
    private readonly IRunCreator _runCreator;
    private readonly ILogger<RunController> _logger;

    public RunController(IRepository repository, IRunCreator runCreator, ILogger<RunController> logger)
    {
        _repository = repository;
        _runCreator = runCreator;
        _logger = logger;
    }

    public Task StartAsync(CancellationToken cancellationToken)
    {
        _backgroundCancellationTokenSource = new CancellationTokenSource();
        _backgroundTask = BackgroundLoop(_backgroundCancellationTokenSource.Token);
        return Task.CompletedTask;
    }

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        if (_backgroundCancellationTokenSource == null || _backgroundTask == null)
        {
            return;
        }

        _backgroundCancellationTokenSource.Cancel();

        // wait for the background task to complete, but give up once the cancellation token is canceled.
        var tcs = new TaskCompletionSource();
        cancellationToken.Register(s => ((TaskCompletionSource)s!).SetResult(), tcs);
        await Task.WhenAny(_backgroundTask, tcs.Task);
    }

    private async Task BackgroundLoop(CancellationToken cancellationToken)
    {
        while (!cancellationToken.IsCancellationRequested)
        {
            try
            {
                await _repository.ListenForNewRuns(ProcessPageOfNewRuns, cancellationToken);
            }
            catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
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

    public void Dispose()
    {
        if (_backgroundTask is { IsCompleted: true })
        {
            _backgroundTask.Dispose();
        }
    }
}
