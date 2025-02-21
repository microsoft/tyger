// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Database.Migrations;

public static partial class LoggerExtensions
{
    [LoggerMessage(LogLevel.Information, "Using database version {version}")]
    public static partial void UsingDatabaseVersion(this ILogger logger, int version);

    [LoggerMessage(LogLevel.Error, "Failed to read database version")]
    public static partial void FailedToReadDatabaseVersion(this ILogger logger, Exception exception);

    [LoggerMessage(LogLevel.Information, "Applying migration {id}")]
    public static partial void ApplyingMigration(this ILogger logger, int id);

    [LoggerMessage(LogLevel.Information, "Migration {id} complete")]
    public static partial void MigrationComplete(this ILogger logger, int id);

    [LoggerMessage(LogLevel.Error, "Migration {Id} failed")]
    public static partial void MigrationFailed(this ILogger logger, int Id, Exception exception);

    [LoggerMessage(LogLevel.Information, "Failed to update migrations table")]
    public static partial void FailedToUpdateMigrationsTable(this ILogger logger, Exception exception);

    [LoggerMessage(LogLevel.Information, "Database already initialized")]
    public static partial void DatabaseAlreadyInitialized(this ILogger logger);

    [LoggerMessage(LogLevel.Information, "Waiting for database tables to be created...")]
    public static partial void WaitingForDatabaseTablesToBeCreated(this ILogger logger);

    [LoggerMessage(LogLevel.Warning, "The database has been migrated to an unrecognized version {version}. The maximum known version is {maxVersion}")]
    public static partial void UnrecognizedDatabaseVersion(this ILogger logger, int version, int maxVersion);

    [LoggerMessage(LogLevel.Warning, "Error validating current database versions on replicas")]
    public static partial void ErrorValidatingCurrentDatabaseVersionsOnReplicas(this ILogger logger, Exception exception);

    [LoggerMessage(LogLevel.Information, "Waiting for replica at {address} to use required version {version} instead of {usingVersion}")]
    public static partial void WaitingForReplicaToUseRequiredVersion(this ILogger logger, string address, int version, int usingVersion);

    [LoggerMessage(LogLevel.Warning, "Newer database versions exist")]
    public static partial void NewerDatabaseVersionsExist(this ILogger logger);

    [LoggerMessage(LogLevel.Information, "Using most recent database version")]
    public static partial void UsingMostRecentDatabaseVersion(this ILogger logger);

    [LoggerMessage(LogLevel.Error, "This version of Tyger requires the database to be migrated to at least version {requiredVersion} and the server must be take offline for this migration. The current version is {currentVersion}.")]
    public static partial void DatabaseMigrationRequired(this ILogger logger, int requiredVersion, int currentVersion);
}
