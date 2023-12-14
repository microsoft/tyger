using Microsoft.Extensions.Options;
using Npgsql;
using Polly;
using static Tyger.Server.Database.Constants;

namespace Tyger.Server.Database.Migrations;

/// <summary>
/// Applies database migrations sequentially.
/// </summary>
public class MigrationRunner : IHostedService
{
    private readonly NpgsqlDataSource _dataSource;
    private readonly DatabaseVersions _databaseVersions;
    private readonly ResiliencePipeline _resiliencePipeline;
    private readonly DatabaseOptions _options;
    private readonly ILogger<MigrationRunner> _logger;
    private readonly ILoggerFactory _loggerFactory;

    public MigrationRunner(NpgsqlDataSource dataSource, DatabaseVersions databaseVersions, IOptions<DatabaseOptions> options, ResiliencePipeline resiliencePipeline, ILogger<MigrationRunner> logger, ILoggerFactory loggerFactory)
    {
        _dataSource = dataSource;
        _databaseVersions = databaseVersions;
        _resiliencePipeline = resiliencePipeline;
        _options = options.Value;
        _logger = logger;
        _loggerFactory = loggerFactory;
    }

    public async Task RunMigrations(bool initOnly, int? target, CancellationToken cancellationToken)
    {
        DatabaseVersion? current = null;
        bool databaseIsEmpty = !await _databaseVersions.DoesMigrationsTableExist(cancellationToken);
        if (!databaseIsEmpty)
        {
            current = await _databaseVersions.ReadCurrentDatabaseVersion(cancellationToken);
        }

        if (current != null && initOnly)
        {
            _logger.DatabaseAlreadyInitialized();
            return;
        }

        var migrations = _databaseVersions.GetKnownVersions()
            .Where(pair => (current == null || (int)pair.version > (int)current) && (target == null || (int)pair.version <= target))
            .Select(pair => (pair.version, (Migrator)Activator.CreateInstance(pair.migrator)!))
            .ToList();

        if (migrations.Count == 0)
        {
            _logger.NoMigrationsToApply();
            return;
        }

        foreach ((var version, var migrator) in migrations)
        {
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

    private async Task GrantAccess(CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var batch = _dataSource.CreateBatch();

            batch.BatchCommands.Add(new($"GRANT ALL ON ALL TABLES IN SCHEMA {DatabaseNamespace} TO \"{OwnersRole}\""));
            batch.BatchCommands.Add(new($"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO \"{ServerRole}\""));

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
        if (_options.AutoMigrate)
        {
            await RunMigrations(false, null, cancellationToken);
        }
    }

    Task IHostedService.StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;
}
