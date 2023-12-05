using System.Reflection;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Npgsql;
using static Tyger.Server.Database.Constants;

namespace Tyger.Server.Database.Migrations;

/// <summary>
/// The known database versions.
/// </summary>
public enum DatabaseVersion
{
    [Migrator(typeof(Migrator1))]
    Initial = 1,
}

public sealed class DatabaseVersions : IHostedService, IHealthCheck, IDisposable
{
    private readonly NpgsqlDataSource _dataSource;
    private readonly ILogger<DatabaseVersions> _logger;
    private CancellationTokenSource? _backgroundCancellationTokenSource;
    private Task? _backgroundTask;

    public DatabaseVersion CachedCurrentVersion { get; private set; }

    public DatabaseVersions(NpgsqlDataSource dataSource, ILogger<DatabaseVersions> logger)
    {
        _dataSource = dataSource;
        _logger = logger;
    }

    public List<(DatabaseVersion version, Type migrator)> GetKnownVersions() =>
        (from version in Enum.GetValues<DatabaseVersion>()
         let att = typeof(DatabaseVersion).GetField(version.ToString())!.GetCustomAttribute<MigratorAttribute>()
             ?? throw new InvalidOperationException($"{nameof(DatabaseVersion)} value must have a {nameof(MigratorAttribute)}")
         orderby (int)version
         select (version, att.MigratorType))
        .ToList();

    public async Task<DatabaseVersion?> ReadCurrentDatabaseVersion(CancellationToken cancellationToken)
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
            int i when Enum.IsDefined(typeof(DatabaseVersion), i) => (DatabaseVersion)i,
            int i => throw new InvalidOperationException($"Database version {i} is not supported. This version of Tyger supports versions {Enum.GetValues<DatabaseVersion>().Min()} to {Enum.GetValues<DatabaseVersion>().Max()}"),
            _ => throw new InvalidOperationException("Unexpected type returned from database")
        };
    }

    async Task IHostedService.StartAsync(CancellationToken cancellationToken)
    {
        await ReadAndUpdateCachedDatabaseVersion(cancellationToken);

        _backgroundCancellationTokenSource = new();
        _backgroundTask = BackgroundLoop(_backgroundCancellationTokenSource.Token);
    }

    async Task IHostedService.StopAsync(CancellationToken cancellationToken)
    {
        if (_backgroundCancellationTokenSource == null || _backgroundTask == null)
        {
            return;
        }

        _backgroundCancellationTokenSource.Cancel();

        // wait for the background task to complete, but give up once the cancellation token is canceled.
        var tcs = new TaskCompletionSource();
        cancellationToken.Register(s => ((TaskCompletionSource)s!).SetResult(), tcs);
        await Task.WhenAny(_backgroundTask, tcs.Task);
    }

    private async Task BackgroundLoop(CancellationToken cancellationToken)
    {
        while (!cancellationToken.IsCancellationRequested)
        {
            try
            {
                await Task.Delay(TimeSpan.FromSeconds(30), cancellationToken);
                await ReadAndUpdateCachedDatabaseVersion(cancellationToken);
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

    private async Task ReadAndUpdateCachedDatabaseVersion(CancellationToken cancellationToken)
    {
        var readVersion = await ReadCurrentDatabaseVersion(cancellationToken);
        if (!readVersion.HasValue)
        {
            throw new InvalidOperationException("The database appears to be empty");
        }

        if (CachedCurrentVersion != readVersion.Value)
        {
            CachedCurrentVersion = readVersion.Value;
            _logger.UsingDatabaseVersion((int)readVersion.Value);
        }
    }

    public async Task<HealthCheckResult> CheckHealthAsync(HealthCheckContext context, CancellationToken cancellationToken)
    {
        var res = await ReadCurrentDatabaseVersion(cancellationToken);
        if (!res.HasValue)
        {
            return HealthCheckResult.Unhealthy("Database is empty");
        }

        return HealthCheckResult.Healthy();
    }

    public void Dispose() => _backgroundCancellationTokenSource?.Dispose();
}

[AttributeUsage(AttributeTargets.Field, Inherited = false, AllowMultiple = false)]
public sealed class MigratorAttribute(Type migratorType) : Attribute
{
    public Type MigratorType { get; } = migratorType;
}
