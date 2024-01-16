// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.Server.Buffers;

public static partial class LogingExtensions
{
    [LoggerMessage(0, LogLevel.Information, "Creating buffer {bufferId}")]
    public static partial void CreatingBuffer(this ILogger logger, string bufferId);

    [LoggerMessage(1, LogLevel.Warning, "GetBlobContainerClient returned InvalidResourceName for {bufferId}")]
    public static partial void InvalidResourceName(this ILogger logger, string bufferId);

    [LoggerMessage(2, LogLevel.Warning, "Failed to refresh user delegation key")]
    public static partial void FailedToRefreshUserDelegationKey(this ILogger logger, Exception ex);

    [LoggerMessage(2, LogLevel.Error, "Failed to refresh user delegation key (expired)")]
    public static partial void FailedToRefreshExpiredUserDelegationKey(this ILogger logger, Exception ex);
}
