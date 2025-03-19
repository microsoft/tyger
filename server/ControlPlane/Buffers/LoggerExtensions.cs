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

    [LoggerMessage(LogLevel.Information, "Soft-deleted {deletedCount} buffers")]
    public static partial void SoftDeletedBuffers(this ILogger logger, int deletedCount);

    [LoggerMessage(LogLevel.Information, "Purged {deletedByProvider} out of {deletedFromDatabase} expired buffers")]
    public static partial void HardDeletedBuffers(this ILogger logger, int deletedByProvider, int deletedFromDatabase);

    [LoggerMessage(LogLevel.Error, "Failed to delete buffer with id {bufferId}")]
    public static partial void FailedToDeleteBuffer(this ILogger logger, string bufferId, Exception exception);

    [LoggerMessage(LogLevel.Error, "Error during buffer deletion")]
    public static partial void ErrorDuringBackgroundBufferDelete(this ILogger logger, Exception exception);

}
