// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.Server.Database;

public static partial class LoggerExtensions
{
    [LoggerMessage(0, LogLevel.Information, "Upserting codespec {name}")]
    public static partial void UpsertingCodespec(this ILogger logger, string name);

    [LoggerMessage(1, LogLevel.Information, "Conflict when upserting codespec {name}")]
    public static partial void UpsertingCodespecConflict(this ILogger logger, string name);

    [LoggerMessage(2, LogLevel.Warning, "Retrying database operation. SqlState: {sqlState}, Message: {message}")]
    public static partial void RetryingDatabaseOperation(this ILogger logger, string? sqlState, string? message);

    [LoggerMessage(3, LogLevel.Information, "Refreshed database credentials")]
    public static partial void RefreshedDatabaseCredentials(this ILogger logger);

    [LoggerMessage(4, LogLevel.Information, "Failed to refresh database credentials")]
    public static partial void FailedToRefreshDatabaseCredentials(this ILogger logger, Exception exception);
}
