// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Collections.Concurrent;

namespace Tyger.ControlPlane.Compute;

/// <summary>
/// Manages background Tasks that refresh the buffer SAS URLs for active Runs.
/// </summary>
public class RunBufferAccessRefresher : BackgroundService
{
    private readonly ILogger<RunBufferAccessRefresher> _logger;

    private readonly ConcurrentBag<(long runId, Func<CancellationToken, Task> taskFunc)> _refreshTaskFuncs = [];

    public RunBufferAccessRefresher(ILogger<RunBufferAccessRefresher> logger)
    {
        _logger = logger;
    }

    public void Add(long runId, Func<CancellationToken, Task> taskFunc)
    {
        _refreshTaskFuncs.Add((runId, taskFunc));
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        Dictionary<long, Task> refreshTasks = [];

        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {

                while (_refreshTaskFuncs.TryTake(out var pair))
                {
                    var (runId, taskFunc) = pair;
                    var task = Task.Run(() => taskFunc(stoppingToken), stoppingToken);
                    _logger.StartingBufferAccessRefreshTask(runId);
                    refreshTasks.Add(runId, task);
                }

                List<long> completedRunIds = [];
                foreach (var (runId, task) in refreshTasks)
                {
                    if (task.IsCompleted)
                    {
                        completedRunIds.Add(runId);

                        try
                        {
                            await task;
                        }
                        catch (Exception e)
                        {
                            _logger.ErrorInBufferAccessRefreshTask(e, runId);
                        }
                    }
                }

                foreach (var runId in completedRunIds)
                {
                    refreshTasks.Remove(runId);
                    _logger.FinishedBufferAccessRefreshTask(runId);
                }

                await Task.Delay(2000, stoppingToken);
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception e)
            {
                _logger.ErrorInBufferAccessRefresher(e);
                return;
            }
        }
    }
}
