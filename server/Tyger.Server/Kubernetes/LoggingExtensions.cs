namespace Tyger.Server.Kubernetes;

public static partial class LoggerExtensions
{
    [LoggerMessage(0, LogLevel.Information, "Found codespec {codespecRef}")]
    public static partial void FoundCodespec(this ILogger logger, string codespecRef);

    [LoggerMessage(1, LogLevel.Information, "Created secret {secretName}")]
    public static partial void CreatedSecret(this ILogger logger, string secretName);

    [LoggerMessage(2, LogLevel.Information, "Created run {runName}")]
    public static partial void CreatedRun(this ILogger logger, string runName);

    [LoggerMessage(3, LogLevel.Error, "Unable to determine pod {pod} status")]
    public static partial void UnableToDeterminePodPhase(this ILogger logger, string pod);

    [LoggerMessage(4, LogLevel.Warning, "Pod {pod} has missing or invalid run annotation")]
    public static partial void InvalidRunAnnotation(this ILogger logger, string pod);

    [LoggerMessage(5, LogLevel.Error, "Pod {pod} for run was not found in the cluster")]
    public static partial void RunMissingPod(this ILogger logger, string pod);

    [LoggerMessage(6, LogLevel.Information, "Starting background sweep")]
    public static partial void StartingBackgroundSweep(this ILogger logger);

    [LoggerMessage(7, LogLevel.Information, "Background sweep completed")]
    public static partial void BackgroundSweepCompleted(this ILogger logger);

    [LoggerMessage(8, LogLevel.Information, "Error during background sweep.")]
    public static partial void ErrorDuringBackgroundSweep(this ILogger logger, Exception exception);

    [LoggerMessage(9, LogLevel.Error, "Error response body: {body}")]
    public static partial void ErrorResponseBody(this ILogger logger, string body);

    [LoggerMessage(10, LogLevel.Warning, "Deleting run {run} without pod")]
    public static partial void DeletingRunWithoutPod(this ILogger logger, long run);

    [LoggerMessage(11, LogLevel.Warning, "Deleting run {run} that never had a pod created")]
    public static partial void DeletingRunThatNeverCreatedAPod(this ILogger logger, long run);

    [LoggerMessage(12, LogLevel.Information, "Finalizing run {run} with status {status}")]
    public static partial void FinalizingTerminatedRun(this ILogger logger, long run, string status);

    [LoggerMessage(13, LogLevel.Information, "Finalizing timed out run {run}")]
    public static partial void FinalizingTimedOutRun(this ILogger logger, long run);
}
