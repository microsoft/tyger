// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Buffers;

public static partial class LogingExtensions
{
    [LoggerMessage(LogLevel.Information, "Creating buffer {bufferId}")]
    public static partial void CreatingBuffer(this ILogger logger, string bufferId);

    [LoggerMessage(LogLevel.Warning, "Failed to refresh user delegation key")]
    public static partial void FailedToRefreshUserDelegationKey(this ILogger logger, Exception ex);

    [LoggerMessage(LogLevel.Error, "Failed to refresh user delegation key (expired)")]
    public static partial void FailedToRefreshExpiredUserDelegationKey(this ILogger logger, Exception ex);

    [LoggerMessage(LogLevel.Warning, "Failed to mark buffer as failed")]
    public static partial void FailedToMarkBufferAsFailed(this ILogger logger, Exception ex);
}
