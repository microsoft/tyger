namespace Tyger.Server.Kubernetes;

public static partial class LoggerExtensions
{
    [LoggerMessage(1, LogLevel.Information, "Created secret {secretName}")]
    public static partial void CreatedSecret(this ILogger logger, string secretName);

    [LoggerMessage(2, LogLevel.Information, "Created run {runId}")]
    public static partial void CreatedRun(this ILogger logger, long runId);

    [LoggerMessage(5, LogLevel.Error, "The job {job} for run was not found in the cluster")]
    public static partial void RunMissingJob(this ILogger logger, string job);

    [LoggerMessage(6, LogLevel.Information, "Starting background sweep")]
    public static partial void StartingBackgroundSweep(this ILogger logger);

    [LoggerMessage(7, LogLevel.Information, "Background sweep completed")]
    public static partial void BackgroundSweepCompleted(this ILogger logger);

    [LoggerMessage(8, LogLevel.Information, "Error during background sweep.")]
    public static partial void ErrorDuringBackgroundSweep(this ILogger logger, Exception exception);

    [LoggerMessage(11, LogLevel.Warning, "Deleting run {run} that never created resources")]
    public static partial void DeletingRunThatNeverCreatedResources(this ILogger logger, long run);

    [LoggerMessage(12, LogLevel.Information, "Finalizing run {run} with status {status}")]
    public static partial void FinalizingTerminatedRun(this ILogger logger, long run, string status);

    [LoggerMessage(14, LogLevel.Information, "Executed Kubernetes API request {method} {uri}. Status code {statusCode}. {errorBody}")]
    public static partial void ExecutedKubernetesRequest(this ILogger logger, HttpMethod method, string? uri, int statusCode, string? errorBody);
}
