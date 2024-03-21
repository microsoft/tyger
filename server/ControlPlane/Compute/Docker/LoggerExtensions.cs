// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.


namespace Tyger.ControlPlane.Compute.Docker;

public static partial class LoggerExtensions
{
    [LoggerMessage(0, LogLevel.Warning, "Failed to remove container")]
    public static partial void FailedToRemoveContainer(this ILogger logger, string containerId, Exception exception);

    [LoggerMessage(1, LogLevel.Warning, "Failed to kill container")]
    public static partial void FailedToKillContainer(this ILogger logger, string containerId, Exception exception);

    [LoggerMessage(2, LogLevel.Warning, "Failed to remove secrets for run {runId}")]
    public static partial void FailedToRemoveRunSecretsDirectory(this ILogger logger, long runId, Exception exception);

    [LoggerMessage(3, LogLevel.Warning, "Failed to remove ephemeral buffer socket {path}")]
    public static partial void FailedToRemoveEphemeralBufferSocket(this ILogger logger, string path, Exception exception);
}
