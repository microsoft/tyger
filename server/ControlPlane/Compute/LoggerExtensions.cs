// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Compute;

public static partial class LoggerExtensions
{

    [LoggerMessage(LogLevel.Information, "Created new run {runId}. Container: {containerImage}, CPU: {cpuRequests}, GPU: {gpuRequests}, Memory: {memRequests}")]
    public static partial void CreatedRun(this ILogger logger, long runId, string? containerImage, string? cpuRequests, string? gpuRequests, string? memRequests);

    [LoggerMessage(LogLevel.Information, "Created run {runId} resources")]
    public static partial void CreatedRunResources(this ILogger logger, long runId);

    [LoggerMessage(LogLevel.Information, "Canceling run {runId}")]
    public static partial void CancelingRun(this ILogger logger, long runId);

    [LoggerMessage(LogLevel.Information, "Canceled run {runId}")]
    public static partial void CanceledRun(this ILogger logger, long runId);

    [LoggerMessage(LogLevel.Information, "Starting background sweep")]
    public static partial void StartingBackgroundSweep(this ILogger logger);

    [LoggerMessage(LogLevel.Information, "Background sweep completed")]
    public static partial void BackgroundSweepCompleted(this ILogger logger);

    [LoggerMessage(LogLevel.Information, "Error during background sweep.")]
    public static partial void ErrorDuringBackgroundSweep(this ILogger logger, Exception exception);

    [LoggerMessage(LogLevel.Warning, "Deleting run {run} that never created resources")]
    public static partial void DeletingRunThatNeverCreatedResources(this ILogger logger, long run);

    [LoggerMessage(LogLevel.Information, "Finalizing run {runId} with status {status}")]
    public static partial void FinalizingTerminatedRun(this ILogger logger, long runId, RunStatus status);

    [LoggerMessage(LogLevel.Warning, "Failed to finalize run {runId}")]
    public static partial void ErrorDuringFinalization(this ILogger logger, long runId, Exception exception);

    [LoggerMessage(LogLevel.Information, "Watch reached terminal status {status} for run {runId}")]
    public static partial void WatchReachedTerminalState(this ILogger logger, RunStatus status, long runId);

    [LoggerMessage(LogLevel.Warning, "RunStateObserver channel {partition} has high count of {count}")]
    public static partial void RunStateObserverHighQueueLength(this ILogger logger, int partition, int count);

    [LoggerMessage(LogLevel.Information, "Updated secret for run {runId}")]
    public static partial void UpdatedRunSecret(this ILogger logger, long runId);

    [LoggerMessage(LogLevel.Error, "Error updating secret for run {runId}")]
    public static partial void ErrorUpdatingRunSecret(this ILogger logger, Exception e, long runId);

    [LoggerMessage(LogLevel.Error, "Error during secret update")]
    public static partial void ErrorInRunSecretUpdater(this ILogger logger, Exception e);
}
