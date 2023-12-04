using System.Reflection;
using Npgsql;

namespace Tyger.Server.Database.Migrations;

public abstract class Migration
{
    public int GetId() => GetAttr().Id;

    public string GetDescription() => GetAttr().Description;

    public abstract Task Apply(NpgsqlDataSource dataSource, ILogger logger, CancellationToken cancellationToken);

    private MigrationAttribute GetAttr() => GetType().GetCustomAttribute<MigrationAttribute>() ?? throw new InvalidOperationException("Migration must have a MigrationAttribute");

    protected static string WrapCreateIndexWithExistenceCheck(string indexName, string createIndexStatement)
    {
        return $"""
            DO $$
            BEGIN
                IF NOT EXISTS (
                    SELECT
                    FROM pg_class c
                    JOIN pg_namespace n ON n.oid = c.relnamespace
                    WHERE c.relname = '{indexName}'
                        AND n.nspname = '{MigrationRunner.DatabaseNamespace}'
                ) THEN
                    {Indent("        ", createIndexStatement)};
                END IF;
            END
            $$;
            """;
    }

    private static string Indent(string indent, string s)
    {
        return string.Join(Environment.NewLine, s.Split(Environment.NewLine).Select(l => indent + l));
    }
}

[AttributeUsage(AttributeTargets.Class, Inherited = false, AllowMultiple = false)]
public sealed class MigrationAttribute(int id, string description) : Attribute
{
    public int Id { get; } = id;
    public string Description { get; } = description;
}

public class MigrationRunner
{
    private const string MigrationsTableName = "migrations";
    public const string DatabaseNamespace = "public"; // technically the database schema

    public const string MigrationStateStarted = "started";
    public const string MigrationStateComplete = "complete";
    public const string MigrationStateFailed = "failed";

    private readonly NpgsqlDataSource _dataSource;
    private readonly ILogger<MigrationRunner> _logger;
    private readonly ILoggerFactory _loggerFactory;

    public MigrationRunner(NpgsqlDataSource dataSource, ILogger<MigrationRunner> logger, ILoggerFactory loggerFactory)
    {
        _dataSource = dataSource;
        _logger = logger;
        _loggerFactory = loggerFactory;
    }

    public async Task RunMigrations(int? target, CancellationToken cancellationToken)
    {
        int? current = null;
        bool databaseIsEmpty = !await DoesMigrationsTableExist(cancellationToken);
        if (!databaseIsEmpty)
        {
            current = await GetCurrentDatabaseVersion(cancellationToken);
        }

        var migrations = GetType().Assembly.GetTypes().Where(t => t.IsSubclassOf(typeof(Migration)))
            .Select(t => (id: t.GetCustomAttribute<MigrationAttribute>()?.Id ?? throw new InvalidOperationException($"{t.GetType().Name} is missing {nameof(MigrationAttribute)}"), type: t))
            .Where(t => (current == null || t.id > current) && (target == null || t.id <= target))
            .OrderBy(t => t.id)
            .Select(t => (Migration)Activator.CreateInstance(t.type)!)
            .ToList();

        if (migrations.Count == 0)
        {
            return;
        }

        foreach (var migration in migrations)
        {
            _logger.LogInformation("Applying migration {Id}", migration.GetId());
            string migrationState = MigrationStateStarted;
            if (!databaseIsEmpty)
            {
                await AddToMigrationTable(migration, migrationState, cancellationToken);
            }

            var migrationLogger = _loggerFactory.CreateLogger(migration.GetType());

            try
            {
                await migration.Apply(_dataSource, migrationLogger, cancellationToken);
                migrationState = MigrationStateComplete;
                databaseIsEmpty = false;
                _logger.LogInformation("Migration {Id} complete", migration.GetId());
            }
            catch (Exception e)
            {
                migrationState = MigrationStateFailed;
                _logger.LogError(e, "Migration {Id} failed", migration.GetId());
                throw;
            }
            finally
            {
                if (!databaseIsEmpty)
                {
                    try
                    {
                        await AddToMigrationTable(migration, migrationState, cancellationToken);
                    }
                    catch (Exception e) when (migrationState == MigrationStateFailed)
                    {
                        _logger.LogError(e, "Failed to update migration state");
                    }
                }
            }
        }
    }

    private async Task AddToMigrationTable(Migration migration, string migrationState, CancellationToken cancellationToken)
    {
        await using var cmd = _dataSource.CreateCommand($"""
            INSERT INTO {MigrationsTableName} (version, state)
            VALUES ($1, $2)
            """);

        cmd.Parameters.AddWithValue(migration.GetId());
        cmd.Parameters.AddWithValue(migrationState);

        await cmd.ExecuteNonQueryAsync(cancellationToken);
    }

    public async Task<bool> DoesMigrationsTableExist(CancellationToken cancellationToken)
    {
        await using var cmd = _dataSource.CreateCommand($"""
            SELECT EXISTS (
                SELECT
                FROM pg_catalog.pg_class c
                JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
                WHERE n.nspname = $1
                    AND c.relname = $2
                    AND c.relkind = 'r' -- 'r' denotes a table
            )
            """);

        cmd.Parameters.AddWithValue(DatabaseNamespace);
        cmd.Parameters.AddWithValue(MigrationsTableName);

        return (bool)(await cmd.ExecuteScalarAsync(cancellationToken))!;
    }

    public async Task<int?> GetCurrentDatabaseVersion(CancellationToken cancellationToken)
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
            null => null,
            int i => i,
            _ => throw new InvalidOperationException("Unexpected type returned from database")
        };
    }
}
