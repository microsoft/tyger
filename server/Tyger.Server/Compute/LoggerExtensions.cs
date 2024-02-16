// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.Server.Model;

namespace Tyger.Server.Compute;

public static partial class LoggerExtensions
{

    [LoggerMessage(2, LogLevel.Information, "Created run {runId}")]
    public static partial void CreatedRun(this ILogger logger, long runId);

    [LoggerMessage(3, LogLevel.Information, "Canceling run {runId}")]
    public static partial void CancelingRun(this ILogger logger, long runId);

    [LoggerMessage(4, LogLevel.Information, "Canceled run {runId}")]
    public static partial void CanceledRun(this ILogger logger, long runId);

    [LoggerMessage(6, LogLevel.Information, "Starting background sweep")]
    public static partial void StartingBackgroundSweep(this ILogger logger);

    [LoggerMessage(7, LogLevel.Information, "Background sweep completed")]
    public static partial void BackgroundSweepCompleted(this ILogger logger);

    [LoggerMessage(8, LogLevel.Information, "Error during background sweep.")]
    public static partial void ErrorDuringBackgroundSweep(this ILogger logger, Exception exception);

    [LoggerMessage(11, LogLevel.Warning, "Deleting run {run} that never created resources")]
    public static partial void DeletingRunThatNeverCreatedResources(this ILogger logger, long run);

    [LoggerMessage(12, LogLevel.Information, "Finalizing run {run} with status {status}")]
    public static partial void FinalizingTerminatedRun(this ILogger logger, long run, RunStatus status);

    [LoggerMessage(15, LogLevel.Error, "Unhandled background exception when following logs")]
    public static partial void UnexpectedExceptionDuringWatch(this ILogger logger, Exception exception);

    [LoggerMessage(16, LogLevel.Information, "Restarting watch after exception")]
    public static partial void RestartingWatchAfterException(this ILogger logger, Exception exception);

}
