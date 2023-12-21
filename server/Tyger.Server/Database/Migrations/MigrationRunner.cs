using System.ComponentModel.DataAnnotations;
using System.Text.Json;
using k8s;
using Microsoft.Extensions.Options;
using Npgsql;
using Polly;
using Tyger.Server.Kubernetes;
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
    private readonly DatabaseOptions _databaseOptions;
    private readonly IKubernetes _kubernetesClient;
    private readonly JsonSerializerOptions _jsonSerializerOptions;
    private readonly KubernetesCoreOptions _kubernetesOptions;
    private readonly ILogger<MigrationRunner> _logger;
    private readonly ILoggerFactory _loggerFactory;

    public MigrationRunner(
        NpgsqlDataSource dataSource,
        DatabaseVersions databaseVersions,
        IOptions<DatabaseOptions> databaseOptions,
        ResiliencePipeline resiliencePipeline,
        IKubernetes kubernetesClient,
        IOptions<KubernetesCoreOptions> kubernetesOptions,
        JsonSerializerOptions jsonSerializerOptions,
        ILogger<MigrationRunner> logger,
        ILoggerFactory loggerFactory)
    {
        _dataSource = dataSource;
        _databaseVersions = databaseVersions;
        _resiliencePipeline = resiliencePipeline;
        _databaseOptions = databaseOptions.Value;
        _kubernetesClient = kubernetesClient;
        _jsonSerializerOptions = jsonSerializerOptions;
        _kubernetesOptions = kubernetesOptions.Value;
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

        if (current != null && initOnly)
        {
            _logger.DatabaseAlreadyInitialized();
            return;
        }

        if (!offline && !string.IsNullOrEmpty(_kubernetesOptions.KubeconfigPath))
        {
            offline = true;
        }

        var knownVersions = _databaseVersions.GetKnownVersions();
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

        if (migrations.Count == 0)
        {
            _logger.NoMigrationsToApply();
            return;
        }

        using var httpClient = new HttpClient();

        foreach ((var version, var migrator) in migrations)
        {
            if (!offline)
            {
                for (int i = 0; ; i++)
                {
                    if (i != 0)
                    {
                        await Task.Delay(TimeSpan.FromSeconds(30), cancellationToken);
                    }

                    try
                    {
                        var endpointSlices = await _kubernetesClient.DiscoveryV1.ListNamespacedEndpointSliceAsync(_kubernetesOptions.Namespace, labelSelector: "kubernetes.io/service-name=tyger-server", cancellationToken: cancellationToken);
                        foreach (var slice in endpointSlices.Items)
                        {
                            var port = slice.Ports.Single(p => p.Protocol == "TCP");
                            foreach (var ep in slice.Endpoints)
                            {
                                if (ep.Conditions.Ready != true)
                                {
                                    continue;
                                }

                                foreach (var address in ep.Addresses)
                                {
                                    var uri = new Uri($"http://{address}:{port.Port}/v1/database-versions");
                                    var resp = await httpClient.GetAsync(uri, cancellationToken);
                                    resp.EnsureSuccessStatusCode();
                                    var versionsPage = (await resp.Content.ReadFromJsonAsync<Model.DatabaseVersionPage>(_jsonSerializerOptions, cancellationToken))!;
                                    var usingVersion = versionsPage.Items.Single(v => v.Using);
                                    if (versionsPage.Items.Single(v => v.Using).Id != (int)version - 1)
                                    {
                                        _logger.WaitingForPodToUseRequiredVersion(address, (int)version - 1, usingVersion.Id);
                                        continue;
                                    }
                                }
                            }
                        }

                        break;
                    }
                    catch (Exception e) when (!cancellationToken.IsCancellationRequested)
                    {
                        _logger.ErrorValidatingCurrentDatabaseVersionsOnReplicas(e);
                    }
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

    private async Task GrantAccess(CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var batch = _dataSource.CreateBatch();

            batch.BatchCommands.Add(new($"GRANT ALL ON ALL TABLES IN SCHEMA {DatabaseNamespace} TO \"{OwnersRole}\""));
            batch.BatchCommands.Add(new($"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO \"{_databaseOptions.TygerServerRoleName}\""));

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
