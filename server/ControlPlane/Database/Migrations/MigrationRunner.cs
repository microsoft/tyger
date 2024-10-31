// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Microsoft.Extensions.Options;
using Npgsql;
using Polly;
using static Tyger.ControlPlane.Database.Constants;

namespace Tyger.ControlPlane.Database.Migrations;

/// <summary>
/// Applies database migrations sequentially.
/// </summary>
public class MigrationRunner : IHostedService
{
    private readonly NpgsqlDataSource _dataSource;
    private readonly DatabaseVersions _databaseVersions;
    private readonly ResiliencePipeline _resiliencePipeline;
    private readonly DatabaseOptions _databaseOptions;
    private readonly IReplicaDatabaseVersionProvider _replicaDatabaseVersionProvider;
    private readonly ILogger<MigrationRunner> _logger;
    private readonly ILoggerFactory _loggerFactory;

    public MigrationRunner(
        NpgsqlDataSource dataSource,
        DatabaseVersions databaseVersions,
        IOptions<DatabaseOptions> databaseOptions,
        ResiliencePipeline resiliencePipeline,
        IReplicaDatabaseVersionProvider replicaDatabaseVersionProvider,
        ILogger<MigrationRunner> logger,
        ILoggerFactory loggerFactory)
    {
        _dataSource = dataSource;
        _databaseVersions = databaseVersions;
        _resiliencePipeline = resiliencePipeline;
        _databaseOptions = databaseOptions.Value;
        _replicaDatabaseVersionProvider = replicaDatabaseVersionProvider;
        _logger = logger;
        _loggerFactory = loggerFactory;
    }

    public async Task RunMigrations(bool initOnly, int? targetVersion, bool offline, CancellationToken cancellationToken)
    {
        DatabaseVersion? current = null;
        bool databaseIsEmpty = !await _databaseVersions.DoesMigrationsTableExist(cancellationToken);
        if (!databaseIsEmpty)
        {
            current = await _databaseVersions.ReadCurrentDatabaseVersion(cancellationToken);
        }

        var knownVersions = _databaseVersions.GetKnownVersions();

        if (current != null && initOnly)
        {
            _logger.DatabaseAlreadyInitialized();
            await LogCurrentOrAvailableDatabaseVersions(knownVersions, cancellationToken);
            return;
        }

        if (targetVersion != null)
        {
            if (targetVersion > (int)knownVersions[^1].version)
            {
                throw new ValidationException($"The target version {targetVersion} is greater than the highest known version {(int)knownVersions[^1].version}");
            }

            if (current != null && targetVersion < (int)current)
            {
                throw new ValidationException($"The target version {targetVersion} is less than the current version {(int)current}");
            }
        }

        var migrations = knownVersions
            .Where(pair => (current == null || (int)pair.version > (int)current) && (targetVersion == null || (int)pair.version <= targetVersion))
            .Select(pair => (pair.version, (Migrator)Activator.CreateInstance(pair.migrator)!))
            .ToList();

        using var httpClient = new HttpClient();

        foreach ((var version, var migrator) in migrations)
        {
            using (_logger.BeginScope(new Dictionary<string, object> { ["migrationVersionScope"] = (int)version }))
            {
                if (!offline)
                {
                    for (int i = 0; ; i++)
                    {
                        var allReady = true;
                        var requiredVersion = version - 1;
                        try
                        {
                            await foreach ((var replicaUri, var replicaDatabaseVersion) in _replicaDatabaseVersionProvider.GetDatabaseVersionsOfReplicas(cancellationToken))
                            {
                                if (replicaDatabaseVersion < requiredVersion)
                                {
                                    _logger.WaitingForReplicaToUseRequiredVersion(replicaUri.ToString(), (int)requiredVersion, (int)replicaDatabaseVersion);
                                    allReady = false;
                                }
                            }
                        }
                        catch (Exception e) when (!cancellationToken.IsCancellationRequested && i < 50)
                        {
                            _logger.ErrorValidatingCurrentDatabaseVersionsOnReplicas(e);
                            allReady = false;
                        }

                        if (allReady)
                        {
                            break;
                        }

                        await Task.Delay(TimeSpan.FromSeconds(30), cancellationToken);
                    }
                }

                _logger.ApplyingMigration((int)version);
                string migrationState = MigrationStateStarted;
                if (!databaseIsEmpty)
                {
                    await AddToMigrationTable(version, migrationState, cancellationToken);
                }

                var migrationLogger = _loggerFactory.CreateLogger(migrator.GetType());

                try
                {
                    await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
                        await migrator.Apply(_dataSource, migrationLogger, cancellationToken),
                        cancellationToken);

                    await GrantAccess(cancellationToken);

                    migrationState = MigrationStateComplete;
                    databaseIsEmpty = false;
                    _logger.MigrationComplete((int)version);
                }
                catch (Exception e)
                {
                    migrationState = MigrationStateFailed;
                    _logger.MigrationFailed((int)version, e);
                    throw;
                }
                finally
                {
                    if (!databaseIsEmpty)
                    {
                        try
                        {
                            await AddToMigrationTable(version, migrationState, cancellationToken);
                        }
                        catch (Exception e) when (migrationState == MigrationStateFailed)
                        {
                            _logger.FailedToUpdateMigrationsTable(e);
                        }
                    }
                }
            }
        }

        await LogCurrentOrAvailableDatabaseVersions(knownVersions, cancellationToken);
    }

    private async Task LogCurrentOrAvailableDatabaseVersions(List<(DatabaseVersion version, Type migrator, bool isMinimumVersion)> knownVersions, CancellationToken cancellationToken)
    {
        if (!await _databaseVersions.DoesMigrationsTableExist(cancellationToken))
        {
            return;
        }

        var currentVersion = await _databaseVersions.ReadCurrentDatabaseVersion(cancellationToken);
        if (currentVersion == null)
        {
            return;
        }

        if (knownVersions.Any(kv => (int)kv.version > (int)currentVersion))
        {
            if (knownVersions.FirstOrDefault(v => v.isMinimumVersion) is (var minimumVersion, var _, var _) && currentVersion < minimumVersion)
            {
                _logger.DatabaseMigrationRequired((int)minimumVersion, (int)currentVersion);
            }
            else
            {
                _logger.NewerDatabaseVersionsExist();
            }
        }
        else
        {
            _logger.UsingMostRecentDatabaseVersion();
        }
    }

    private async Task GrantAccess(CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var batch = _dataSource.CreateBatch();

            batch.BatchCommands.Add(new($"GRANT ALL ON ALL TABLES IN SCHEMA {DatabaseNamespace} TO \"{OwnersRole}\""));
            batch.BatchCommands.Add(new($"GRANT ALL ON ALL SEQUENCES IN SCHEMA {DatabaseNamespace} TO \"{OwnersRole}\""));
            batch.BatchCommands.Add(new($"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA {DatabaseNamespace} TO \"{_databaseOptions.TygerServerRoleName}\""));
            batch.BatchCommands.Add(new($"GRANT USAGE ON ALL SEQUENCES IN SCHEMA {DatabaseNamespace} TO \"{_databaseOptions.TygerServerRoleName}\""));

            await batch.ExecuteNonQueryAsync(cancellationToken);
        }, cancellationToken);
    }

    private async Task AddToMigrationTable(DatabaseVersion version, string migrationState, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var cmd = _dataSource.CreateCommand($"""
            INSERT INTO {MigrationsTableName} (version, state)
            VALUES ($1, $2)
            """);

            cmd.Parameters.AddWithValue((int)version);
            cmd.Parameters.AddWithValue(migrationState);

            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }, cancellationToken);
    }

    async Task IHostedService.StartAsync(CancellationToken cancellationToken)
    {
        if (_databaseOptions.AutoMigrate)
        {
            await RunMigrations(initOnly: false, targetVersion: null, offline: true, cancellationToken);
        }
    }

    Task IHostedService.StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;
}
