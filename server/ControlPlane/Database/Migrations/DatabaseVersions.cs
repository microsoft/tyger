// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel;
using System.Reflection;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Npgsql;
using Polly;
using static Tyger.ControlPlane.Database.Constants;

namespace Tyger.ControlPlane.Database.Migrations;

/// <summary>
/// The known database versions.
/// </summary>
public enum DatabaseVersion
{
    [Migrator(typeof(Migrator1))]
    [Description("Initial version")]
    Initial = 1,

    [Migrator(typeof(Migrator2))]
    [Description("Adding an index to the codespecs table")]
    AddCodespecsIndex = 2,

    [Migrator(typeof(Migrator3))]
    [Description("Making run management more scalable")]
    RunScalability = 3,

    [Migrator(typeof(Migrator4))]
    [Description("Adjusting run indexes")]
    AddRunsIndexForPaging = 4,

    [Migrator(typeof(Migrator5))]
    [Description("Support multiple storage accounts")]
    MultipleStorageAccounts = 5,

    [Migrator(typeof(Migrator6))]
    [Description("Add ability to tag runs")]
    RunTags = 6,

    [Migrator(typeof(Migrator7))]
    [Description("Add soft/hard delete to buffers")]
    BufferDelete = 7,

    [Migrator(typeof(Migrator8))]
    [Description("Support long-running compute jobs")]
    [MinimumSupportedVersion]
    RefreshBufferSecrets = 8
}

public sealed class DatabaseVersions : BackgroundService, IHealthCheck
{
    private readonly NpgsqlDataSource _dataSource;
    private readonly ResiliencePipeline _resiliencePipeline;
    private readonly ILogger<DatabaseVersions> _logger;

    public DatabaseVersions(NpgsqlDataSource dataSource, ResiliencePipeline resiliencePipeline, ILogger<DatabaseVersions> logger)
    {
        _dataSource = dataSource;
        _resiliencePipeline = resiliencePipeline;
        _logger = logger;
    }

    /// <summary>
    /// Use this property to get the current database version.
    /// </summary>
    public DatabaseVersion CachedCurrentVersion { get; private set; }

    public List<(DatabaseVersion version, Type migrator, bool minimumSupported)> GetKnownVersions() =>
        (from version in Enum.GetValues<DatabaseVersion>()
         let migrationAtt = typeof(DatabaseVersion).GetField(version.ToString())!.GetCustomAttribute<MigratorAttribute>()
             ?? throw new InvalidOperationException($"{nameof(DatabaseVersion)} value must have a {nameof(MigratorAttribute)}")
         let minSupportedAtt = typeof(DatabaseVersion).GetField(version.ToString())!.GetCustomAttribute<MinimumSupportedVersionAttribute>()
         orderby (int)version
         select (version, migrationAtt.MigratorType, minSupportedAtt != null))
        .ToList();

    public async Task<DatabaseVersion?> ReadCurrentDatabaseVersion(CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var cmd = new NpgsqlCommand($"""
            SELECT version
            FROM {MigrationsTableName}
            WHERE state = 'complete'
            ORDER BY version DESC
            LIMIT 1
            """, conn);

            await cmd.PrepareAsync(cancellationToken);
            return await cmd.ExecuteScalarAsync(cancellationToken) switch
            {
                null => (DatabaseVersion?)null,
                int i when Enum.IsDefined(typeof(DatabaseVersion), i) => (DatabaseVersion)i,
                int i => throw new InvalidOperationException($"Database version {i} is not supported. This version of Tyger supports versions {(int)Enum.GetValues<DatabaseVersion>().Min()} to {(int)Enum.GetValues<DatabaseVersion>().Max()}"),
                _ => throw new InvalidOperationException("Unexpected type returned from database")
            };
        }, cancellationToken);
    }

    public async Task<bool> DoesMigrationsTableExist(CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var cmd = _dataSource.CreateCommand($"""
            SELECT EXISTS (
                SELECT
                FROM pg_catalog.pg_class c
                JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
                WHERE n.nspname = $1
                    AND c.relname = $2
                    AND c.relkind = 'r'
            )
            """);

            cmd.Parameters.AddWithValue(DatabaseNamespace);
            cmd.Parameters.AddWithValue(MigrationsTableName);

            return (bool)(await cmd.ExecuteScalarAsync(cancellationToken))!;
        }, cancellationToken);
    }

    public async Task<IList<DatabaseVersionInfo>> GetDatabaseVersions(CancellationToken cancellationToken)
    {
        DatabaseVersion? currentDatabaseVersion = null;
        var migrationsTableExists = await DoesMigrationsTableExist(cancellationToken);
        var stateFromDatabase = new Dictionary<int, DatabaseVersionState>();

        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            if (migrationsTableExists)
            {
                await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
                await using var cmd = new NpgsqlCommand($"""
                    SELECT DISTINCT ON (version) version, state
                    FROM migrations
                    ORDER BY version ASC, timestamp DESC
                    """, conn);

                await cmd.PrepareAsync(cancellationToken);

                await using var reader = await cmd.ExecuteReaderAsync(cancellationToken);

                while (await reader.ReadAsync(cancellationToken))
                {
                    var version = (DatabaseVersion)reader.GetInt32(0);
                    var state = reader.GetString(1) switch
                    {
                        MigrationStateStarted => DatabaseVersionState.Started,
                        MigrationStateComplete => DatabaseVersionState.Complete,
                        MigrationStateFailed => DatabaseVersionState.Failed,
                        var s => throw new InvalidOperationException($"Unexpected state '{s}' returned from database")
                    };

                    if (state == DatabaseVersionState.Complete)
                    {
                        currentDatabaseVersion = version;
                    }

                    stateFromDatabase.Add((int)version, state);
                }
            }

            return GetKnownVersions()
                .OrderBy(v => (int)v.version)
                .Select(v => new DatabaseVersionInfo(
                    (int)v.version,
                    v.version.GetType().GetField(v.version.ToString())?.GetCustomAttribute<DescriptionAttribute>()?.Description ?? v.version.ToString(),
                    State: stateFromDatabase.TryGetValue((int)v.version, out var state) ? state : DatabaseVersionState.Available))
                .ToList();
        }, cancellationToken);
    }

    public override async Task StartAsync(CancellationToken cancellationToken)
    {
        while (true)
        {
            if (await DoesMigrationsTableExist(cancellationToken))
            {
                if (await ReadAndUpdateCachedDatabaseVersion(cancellationToken))
                {
                    if (GetKnownVersions().FirstOrDefault(v => v.minimumSupported) is (var minimumVersion, var _, var _) && CachedCurrentVersion < minimumVersion)
                    {
                        _logger.DatabaseMigrationRequired((int)minimumVersion, (int)CachedCurrentVersion);
                        throw new DatabaseMigrationRequiredException($"This version of Tyger requires the database to be migrated to at least version {(int)minimumVersion}. The current version is {(int)CachedCurrentVersion}.");
                    }

                    break;
                }
            }

            _logger.WaitingForDatabaseTablesToBeCreated();
            await Task.Delay(TimeSpan.FromSeconds(2), cancellationToken);
        }

        await base.StartAsync(cancellationToken);
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                await Task.Delay(TimeSpan.FromSeconds(30), stoppingToken);
                if (!await ReadAndUpdateCachedDatabaseVersion(stoppingToken))
                {
                    throw new InvalidOperationException("Current database version information is not available");
                }
            }
            catch (TaskCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception e)
            {
                _logger.FailedToReadDatabaseVersion(e);
            }
        }
    }

    private async Task<bool> ReadAndUpdateCachedDatabaseVersion(CancellationToken cancellationToken)
    {
        var readVersion = await ReadCurrentDatabaseVersion(cancellationToken);
        if (!readVersion.HasValue)
        {
            return false;
        }

        if (CachedCurrentVersion != readVersion.Value)
        {
            var currentVersionInDatabase = readVersion.Value;
            var mostRecentKnownVersion = GetKnownVersions().Last().version;
            if ((int)currentVersionInDatabase > (int)mostRecentKnownVersion)
            {
                _logger.UnrecognizedDatabaseVersion((int)currentVersionInDatabase, (int)mostRecentKnownVersion);
                currentVersionInDatabase = mostRecentKnownVersion;
            }

            CachedCurrentVersion = currentVersionInDatabase;
            _logger.UsingDatabaseVersion((int)readVersion.Value);
        }

        return true;
    }

    public async Task<HealthCheckResult> CheckHealthAsync(HealthCheckContext context, CancellationToken cancellationToken)
    {
        var res = await ReadCurrentDatabaseVersion(cancellationToken);
        if (!res.HasValue)
        {
            return HealthCheckResult.Unhealthy("Database version is not available");
        }

        return HealthCheckResult.Healthy();
    }
}

[AttributeUsage(AttributeTargets.Field, Inherited = false, AllowMultiple = false)]
public sealed class MigratorAttribute(Type migratorType) : Attribute
{
    public Type MigratorType { get; } = migratorType;
}

[AttributeUsage(AttributeTargets.Field, Inherited = false, AllowMultiple = false)]
public sealed class MinimumSupportedVersionAttribute : Attribute
{
}

public enum DatabaseVersionState
{
    Started,
    Complete,
    Failed,
    Available,
}

public record DatabaseVersionInfo(int Id, string Description, DatabaseVersionState State);

public class DatabaseMigrationRequiredException(string message) : Exception(message);
