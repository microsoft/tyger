// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Database;

public static partial class LoggerExtensions
{
    [LoggerMessage(LogLevel.Warning, "Retrying database operation. SqlState: {sqlState}, Message: {message}")]
    public static partial void RetryingDatabaseOperation(this ILogger logger, string? sqlState, string? message);

    [LoggerMessage(LogLevel.Information, "Refreshed database credentials")]
    public static partial void RefreshedDatabaseCredentials(this ILogger logger);

    [LoggerMessage(LogLevel.Information, "Failed to refresh database credentials")]
    public static partial void FailedToRefreshDatabaseCredentials(this ILogger logger, Exception exception);

    [LoggerMessage(LogLevel.Information, "Duplicate idempotency key {idempotencyKey} received")]
    public static partial void DuplicateIdempotencyKeyReceived(this ILogger logger, string idempotencyKey);

    [LoggerMessage(LogLevel.Information, "Terminal status {status} recorded for {runId} with latency {timeToDetect}")]
    public static partial void TerminalStateRecorded(this ILogger logger, RunStatus status, long runId, TimeSpan timeToDetect);

    [LoggerMessage(LogLevel.Warning, "Exception occured while acquiring or renewing lease {leaseName}")]
    public static partial void LeaseException(this ILogger logger, string leaseName, Exception exception);

    [LoggerMessage(LogLevel.Warning, "Exception occured while releasing lease {leaseName}")]
    public static partial void LeaseReleaseException(this ILogger logger, string leaseName, Exception exception);

    [LoggerMessage(LogLevel.Information, "Acquired lease {leaseName}")]
    public static partial void LeaseAcquired(this ILogger logger, string leaseName);

    [LoggerMessage(LogLevel.Information, "Lost lease {leaseName}")]
    public static partial void LeaseLost(this ILogger logger, string leaseName);

    [LoggerMessage(LogLevel.Information, "Released lease {leaseName}")]
    public static partial void LeaseReleased(this ILogger logger, string leaseName);

    [LoggerMessage(LogLevel.Information, "Update run from observed run state {durationMilliseconds}ms")]
    public static partial void UpdateRunFromObservedState(this ILogger logger, int durationMilliseconds);
}
