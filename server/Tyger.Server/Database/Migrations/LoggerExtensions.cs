namespace Tyger.Server.Database.Migrations;

public static partial class LoggerExtensions
{
    [LoggerMessage(0, LogLevel.Information, "Using database version {version}")]
    public static partial void UsingDatabaseVersion(this ILogger logger, int version);

    [LoggerMessage(1, LogLevel.Error, "Failed to read database version")]
    public static partial void FailedToReadDatabaseVersion(this ILogger logger, Exception exception);

    [LoggerMessage(2, LogLevel.Information, "No migrations to apply")]
    public static partial void NoMigrationsToApply(this ILogger logger);

    [LoggerMessage(3, LogLevel.Information, "Applying migration {Id}")]
    public static partial void ApplyingMigration(this ILogger logger, int Id);

    [LoggerMessage(4, LogLevel.Information, "Migration {Id} complete")]
    public static partial void MigrationComplete(this ILogger logger, int Id);

    [LoggerMessage(5, LogLevel.Error, "Migration {Id} failed")]
    public static partial void MigrationFailed(this ILogger logger, int Id, Exception exception);

    [LoggerMessage(6, LogLevel.Information, "Failed to update migrations table")]
    public static partial void FailedToUpdateMigrationsTable(this ILogger logger, Exception exception);
}
