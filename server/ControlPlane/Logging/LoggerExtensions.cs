// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Logging;

public static partial class LoggerExtensions
{
    [LoggerMessage(LogLevel.Information, "Archived logs for run {run}")]
    public static partial void ArchivedLogsForRun(this ILogger logger, long run);

    [LoggerMessage(LogLevel.Information, "Retrieving archived logs for run {run}")]
    public static partial void RetrievingArchivedLogsForRun(this ILogger logger, long run);

    [LoggerMessage(LogLevel.Information, "Resuming logs after exception.")]
    public static partial void ResumingLogsAfterException(this ILogger logger, Exception exception);

    [LoggerMessage(LogLevel.Warning, "Local log file is does not have expected name '{name}'.")]
    public static partial void LocalLogFileDoesNotHaveExpectedName(this ILogger logger, string name);
}
