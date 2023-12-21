namespace Tyger.Server;

public static partial class LogingExtensions
{
    [LoggerMessage(0, LogLevel.Error, "Validation error: {message}")]
    public static partial void MigrationValidationError(this ILogger logger, string message);

    [LoggerMessage(1, LogLevel.Error, "Unhandled exception while running migration")]
    public static partial void UnhandledMigrationException(this ILogger logger, Exception e);
}
