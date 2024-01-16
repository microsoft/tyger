// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel;
using System.Reflection;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Npgsql;
using Polly;
using static Tyger.Server.Database.Constants;

namespace Tyger.Server.Database.Migrations;

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
}

public sealed class DatabaseVersions : IHostedService, IHealthCheck, IDisposable
{
    private readonly NpgsqlDataSource _dataSource;
    private readonly ResiliencePipeline _resiliencePipeline;
    private readonly ILogger<DatabaseVersions> _logger;
    private readonly CancellationTokenSource _backgroundCancellationTokenSource = new();
    private Task? _backgroundTask;

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

    public List<(DatabaseVersion version, Type migrator)> GetKnownVersions() =>
        (from version in Enum.GetValues<DatabaseVersion>()
         let att = typeof(DatabaseVersion).GetField(version.ToString())!.GetCustomAttribute<MigratorAttribute>()
             ?? throw new InvalidOperationException($"{nameof(DatabaseVersion)} value must have a {nameof(MigratorAttribute)}")
         orderby (int)version
         select (version, att.MigratorType))
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
                int i => throw new InvalidOperationException($"Database version {i} is not supported. This version of Tyger supports versions {Enum.GetValues<DatabaseVersion>().Min()} to {Enum.GetValues<DatabaseVersion>().Max()}"),
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

    async Task IHostedService.StartAsync(CancellationToken cancellationToken)
    {
        while (true)
        {
            if (await DoesMigrationsTableExist(cancellationToken))
            {
                if (await ReadAndUpdateCachedDatabaseVersion(cancellationToken))
                {
                    break;
                }
            }

            _logger.WaitingForDatabaseTablesToBeCreated();
            await Task.Delay(TimeSpan.FromSeconds(2), cancellationToken);
        }

        async Task BackgroundLoop(CancellationToken cancellationToken)
        {
            while (!cancellationToken.IsCancellationRequested)
            {
                try
                {
                    await Task.Delay(TimeSpan.FromSeconds(30), cancellationToken);
                    if (!await ReadAndUpdateCachedDatabaseVersion(cancellationToken))
                    {
                        throw new InvalidOperationException("Current database version information is not available");
                    }
                }
                catch (TaskCanceledException) when (cancellationToken.IsCancellationRequested)
                {
                    return;
                }
                catch (Exception e)
                {
                    _logger.FailedToReadDatabaseVersion(e);
                }
            }
        }

        _backgroundTask = BackgroundLoop(_backgroundCancellationTokenSource.Token);
    }

    Task IHostedService.StopAsync(CancellationToken cancellationToken)
    {
        _backgroundCancellationTokenSource.Cancel();
        return Task.CompletedTask;
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

    public void Dispose() => _backgroundCancellationTokenSource.Dispose();
}

[AttributeUsage(AttributeTargets.Field, Inherited = false, AllowMultiple = false)]
public sealed class MigratorAttribute(Type migratorType) : Attribute
{
    public Type MigratorType { get; } = migratorType;
}

public enum DatabaseVersionState
{
    Started,
    Complete,
    Failed,
    Available,
}

public record DatabaseVersionInfo(int Id, string Description, DatabaseVersionState State);
