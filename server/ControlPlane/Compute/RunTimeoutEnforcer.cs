// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;

namespace Tyger.ControlPlane.Compute;

/// <summary>
/// Periodically scans the database for runs that are still in the
/// <see cref="Model.RunStatus.Pending"/> state and whose
/// <c>timeoutSeconds</c> has elapsed (measured from <c>created_at</c>), and
/// cancels them.
///
/// This backstops the underlying compute platform for the case where it cannot
/// enforce the timeout itself. In particular, a Kubernetes Pod that is stuck
/// in <c>Pending</c> (e.g. unschedulable due to insufficient capacity) never
/// gets a <c>StartTime</c>, so the kubelet's <c>activeDeadlineSeconds</c>
/// never begins counting and reserved worker resources can be held
/// indefinitely. Once a Pod is admitted by the kubelet, the kubelet enforces
/// the deadline, so Running runs are intentionally not handled here.
///
/// The database is the source of truth: the next-due cancellation time is
/// recomputed from stored fields on every poll rather than tracked in memory.
/// </summary>
public class RunTimeoutEnforcer : BackgroundService
{
    private static readonly TimeSpan s_pollInterval = TimeSpan.FromSeconds(60);
    private const string TimeoutStatusReason = "Run exceeded its timeout while pending";
    private const int BatchSize = 100;

    private readonly Repository _repository;
    private readonly IRunUpdater _runUpdater;
    private readonly ILogger<RunTimeoutEnforcer> _logger;

    public RunTimeoutEnforcer(Repository repository, IRunUpdater runUpdater, ILogger<RunTimeoutEnforcer> logger)
    {
        _repository = repository;
        _runUpdater = runUpdater;
        _logger = logger;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                await Task.Delay(s_pollInterval, stoppingToken);
                await CancelExpiredPendingRuns(stoppingToken);
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception e)
            {
                _logger.ErrorInRunTimeoutEnforcer(e);
            }
        }
    }

    private async Task CancelExpiredPendingRuns(CancellationToken cancellationToken)
    {
        while (true)
        {
            var expiredIds = await _repository.GetExpiredPendingRunIds(BatchSize, cancellationToken);
            if (expiredIds.Count == 0)
            {
                return;
            }

            foreach (var id in expiredIds)
            {
                try
                {
                    // CancelRun returns the run unchanged if it is already terminal/final,
                    // so the loop may race with another cancellation path between the
                    // GetExpiredPendingRunIds query and this call. Only log a successful
                    // cancellation when the returned run actually reflects our update
                    // (Canceled with our reason); otherwise some other path got there
                    // first and there is nothing for us to report.
                    var canceled = await _runUpdater.CancelRun(id, TimeoutStatusReason, cancellationToken);
                    if (canceled is { Status: RunStatus.Canceled, StatusReason: TimeoutStatusReason })
                    {
                        _logger.CanceledExpiredRun(id);
                    }
                }
                catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
                {
                    return;
                }
                catch (Exception e)
                {
                    _logger.ErrorCancelingExpiredRun(e, id);
                }
            }

            if (expiredIds.Count < BatchSize)
            {
                return;
            }
        }
    }
}
