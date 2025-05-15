// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Compute.Docker;

public static partial class LoggerExtensions
{
    [LoggerMessage(LogLevel.Warning, "Failed to remove container")]
    public static partial void FailedToRemoveContainer(this ILogger logger, string containerId, Exception exception);

    [LoggerMessage(LogLevel.Warning, "Failed to kill container")]
    public static partial void FailedToKillContainer(this ILogger logger, string containerId, Exception exception);

    [LoggerMessage(LogLevel.Warning, "Failed to remove secrets for run {runId}")]
    public static partial void FailedToRemoveRunSecretsDirectory(this ILogger logger, long runId, Exception exception);

    [LoggerMessage(LogLevel.Warning, "Failed to remove ephemeral buffer socket {path}")]
    public static partial void FailedToRemoveEphemeralBufferSocket(this ILogger logger, string path, Exception exception);

    [LoggerMessage(LogLevel.Warning, "Error reading replica database version")]
    public static partial void ErrorReadingReplicaDatabaseVersion(this ILogger logger, Exception exception);

    [LoggerMessage(LogLevel.Warning, "Error reponse {status} reading replica database version")]
    public static partial void ErrorResponseReadingReplicaDatabaseVersion(this ILogger logger, int status);

    [LoggerMessage(LogLevel.Information, "Refreshed buffer access URLs for run {runId}")]
    public static partial void RefreshedBufferAccessUrls(this ILogger logger, long runId);
}
