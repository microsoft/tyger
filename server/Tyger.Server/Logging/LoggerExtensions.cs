// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.Server.Logging;

public static partial class LoggerExtensions
{
    [LoggerMessage(0, LogLevel.Information, "Archived logs for run {run}")]
    public static partial void ArchivedLogsForRun(this ILogger logger, long run);

    [LoggerMessage(1, LogLevel.Information, "Retrieving archived logs for run {run}")]
    public static partial void RetrievingArchivedLogsForRun(this ILogger logger, long run);

    [LoggerMessage(2, LogLevel.Information, "Resuming logs after exception.")]
    public static partial void ResumingLogsAfterException(this ILogger logger, Exception exception);
}
