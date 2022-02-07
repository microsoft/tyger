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
}
