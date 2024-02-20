
namespace Tyger.Server.Compute.Docker;

public static partial class LoggerExtensions
{
    [LoggerMessage(0, LogLevel.Warning, "Failed to remove container")]
    public static partial void FailedToRemoveContainer(this ILogger logger, string containerId, Exception exception);

    [LoggerMessage(1, LogLevel.Warning, "Failed to remove secrets for run {runId}")]
    public static partial void FailedToRemoveRunSecretsDirectory(this ILogger logger, long runId, Exception exception);

}
