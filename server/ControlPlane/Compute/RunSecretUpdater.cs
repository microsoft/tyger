// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
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
                await RefreshRunSecrets(stoppingToken);
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception e)
            {
                _logger.ErrorInRunSecretUpdater(e);
                return;
            }

            await Task.Delay(5000, stoppingToken);
        }
    }

    protected async Task RefreshRunSecrets(CancellationToken stoppingToken)
    {
        var updates = await _repository.GetRunBufferSecretUpdates(stoppingToken);

        List<long> runIdsToDelete = [];
        foreach (var (runId, run, final, updatedAt, expiresAt) in updates)
        {
            if (run == null || final)
            {
                runIdsToDelete.Add(runId);
                continue;
            }

            if (run.Status is not RunStatus.Running)
            {
                continue;
            }

            var secretLifetime = expiresAt - updatedAt;
            var timeToRefresh = updatedAt + TimeSpan.FromTicks((long)(secretLifetime.Ticks * 0.70));

            if (timeToRefresh > DateTime.UtcNow)
            {
                continue;
            }

            try
            {
                await _runCreator.UpdateRunSecret(run, stoppingToken);
            }
            catch (Exception e)
            {
                _logger.ErrorInRunSecretUpdate(e, run.Id!.Value);
            }
        }

        await _repository.DeleteRunBufferSecretUpdates(runIdsToDelete, stoppingToken);
    }
    //     {
    //         var options = new GetRunsOptions(2000)
    //         {
    //             Statuses = ["Running"]
    //         };

    //         do
    //         {
    //             var (running, nextContinuationToken) = await _repository.GetRuns(options, stoppingToken);
    //             options = options with { ContinuationToken = nextContinuationToken };
    //             foreach (var (run, final) in running)
    //             {
    //                 if (final)
    //                 {
    //                     continue;
    //                 }

    //                 if (run.Job.Codespec is not JobCodespec jobCodespec)
    //                 {
    //                     continue;
    //                 }

    //                 var bufferParams = jobCodespec.Buffers;
    //                 var buffers = run.Job.Buffers!;
    //                 var bufferAccessTtl = run.BufferAccessTtl;
    // #pragma warning disable
    //                 var bufferParamString = $"Inputs: {String.Join(", ", bufferParams.Inputs)}, Outputs: {String.Join(", ", bufferParams.Outputs)}";
    //                 var buffersString = String.Join(", ", buffers.Select(kvp => $"{kvp.Key}: {kvp.Value}"));
    //                 _logger.LogWarning("Would refresh using ({Params}) and ({Buffers})", bufferParamString, buffersString);
    // #pragma warning enable

    //                 try
    //                 {
    //                     _runCreator.RefreshBufferAccessUrls(run, stoppingToken);
    //                 }
    //                 catch (Exception e)
    //                 {
    //                     _logger.ErrorInBufferAccessRefreshTask(e, run.Id.Value);
    //                 }
    //             }
    //         } while (options.ContinuationToken != null);
    //     }

    // {
    //     while (_refreshTaskFuncs.TryTake(out var pair))
    //         {
    //             var (runId, taskFunc) = pair;
    //             var task = Task.Run(() => taskFunc(stoppingToken), stoppingToken);
    //             _logger.StartingBufferAccessRefreshTask(runId);
    //             refreshTasks.Add(runId, task);
    //         }

    //     List<long> completedRunIds = [];
    //     foreach (var (runId, task) in refreshTasks)
    //     {
    //         if (task.IsCompleted)
    //         {
    //             completedRunIds.Add(runId);

    //             try
    //             {
    //                 await task;
    //             }
    //             catch (Exception e)
    //             {
    //                 _logger.ErrorInBufferAccessRefreshTask(e, runId);
    //             }
    //         }
    //     }

    //     foreach (var runId in completedRunIds)
    //     {
    //         refreshTasks.Remove(runId);
    //         _logger.FinishedBufferAccessRefreshTask(runId);
    //     }
    // }
}
