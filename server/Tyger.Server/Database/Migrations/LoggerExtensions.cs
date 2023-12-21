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

    [LoggerMessage(7, LogLevel.Information, "Database already initialized")]
    public static partial void DatabaseAlreadyInitialized(this ILogger logger);

    [LoggerMessage(8, LogLevel.Information, "Waiting for database tables to be created...")]
    public static partial void WaitingForDatabaseTablesToBeCreated(this ILogger logger);

    [LoggerMessage(9, LogLevel.Warning, "The database has been migrated to an unrecognized version {version}. The maximum known version is {maxVersion}")]
    public static partial void UnrecognizedDatabaseVersion(this ILogger logger, int version, int maxVersion);

    [LoggerMessage(10, LogLevel.Warning, "Error validating current database versions on replicas")]
    public static partial void ErrorValidatingCurrentDatabaseVersionsOnReplicas(this ILogger logger, Exception exception);

    [LoggerMessage(11, LogLevel.Information, "Waiting for pod at {address} to use required version {version} instead of {usingVersion}")]
    public static partial void WaitingForPodToUseRequiredVersion(this ILogger logger, string address, int version, int usingVersion);

}
