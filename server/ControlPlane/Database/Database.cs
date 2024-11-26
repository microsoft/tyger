// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.CommandLine;
using System.ComponentModel.DataAnnotations;
using System.Text.Json;
using Azure.Core;
using Microsoft.Extensions.Options;
using Npgsql;
using Polly;
using Polly.Retry;
using Tyger.ControlPlane.Compute.Kubernetes;
using Tyger.ControlPlane.Database.Migrations;
using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Database;

public static class Database
{
    private static readonly string[] s_scopes = ["https://ossrdbms-aad.database.windows.net/.default"];

    public static void AddDatabase(this IHostApplicationBuilder builder)
    {
        builder.Services.AddOptions<DatabaseOptions>().BindConfiguration("database").ValidateDataAnnotations().ValidateOnStart();
        builder.Services.AddSingleton(sp =>
        {
            var logger = sp.GetService<ILogger<ResiliencePipeline>>();
            return new ResiliencePipelineBuilder().AddRetry(new RetryStrategyOptions
            {
                ShouldHandle = new PredicateBuilder().Handle<NpgsqlException>(e => e.IsTransient),
                BackoffType = DelayBackoffType.Exponential,
                UseJitter = true,
                MaxRetryAttempts = 6,
                Delay = TimeSpan.FromMilliseconds(250),
                OnRetry = args =>
                {
                    var exception = args.Outcome.Exception as NpgsqlException;
                    logger?.RetryingDatabaseOperation(
                        exception?.SqlState,
                        (exception as PostgresException)?.MessageText ?? exception?.Message);
                    return default;
                }
            }).Build();
        });

        builder.Services.AddSingleton<Repository>();

        builder.Services.AddSingleton(sp =>
        {
            var databaseOptions = sp.GetRequiredService<IOptions<DatabaseOptions>>().Value;

            var dataSourceBuilder = new NpgsqlDataSourceBuilder();
            dataSourceBuilder.ConnectionStringBuilder.Host = databaseOptions.Host;
            dataSourceBuilder.ConnectionStringBuilder.Database = databaseOptions.DatabaseName;
            if (databaseOptions.Port.HasValue)
            {
                dataSourceBuilder.ConnectionStringBuilder.Port = databaseOptions.Port.Value;
            }

            dataSourceBuilder.ConnectionStringBuilder.Username = databaseOptions.Username;
            dataSourceBuilder.ConnectionStringBuilder.MaxPoolSize = 250;

            if (string.IsNullOrEmpty(databaseOptions.PasswordFile))
            {
                // if connecting via a unix socket, it could be that no password is needed.
                if (dataSourceBuilder.ConnectionStringBuilder.Host == null ||
                    !Path.IsPathFullyQualified(dataSourceBuilder.ConnectionStringBuilder.Host) ||
                    !Path.Exists(dataSourceBuilder.ConnectionStringBuilder.Host))
                {
                    // assume we are connecting to a cloud database
                    dataSourceBuilder.ConnectionStringBuilder.SslMode = SslMode.VerifyFull;
                    var tokenCredential = sp.GetRequiredService<TokenCredential>();
                    var logger = sp.GetRequiredService<ILoggerFactory>().CreateLogger(typeof(Database).FullName!);
                    dataSourceBuilder.UsePeriodicPasswordProvider(
                        async (b, ct) =>
                        {
                            try
                            {
                                var resp = await tokenCredential.GetTokenAsync(new TokenRequestContext(scopes: s_scopes), ct);
                                logger?.RefreshedDatabaseCredentials();
                                return resp.Token;
                            }
                            catch (Exception ex)
                            {
                                logger?.FailedToRefreshDatabaseCredentials(ex);
                                throw new NpgsqlException("Failed to get token", ex);
                            }
                        },
                        TimeSpan.FromMinutes(30),
                        TimeSpan.FromMinutes(1));
                }
            }
            else
            {
                dataSourceBuilder.ConnectionStringBuilder.Password = File.ReadAllText(databaseOptions.PasswordFile);
            }

            return dataSourceBuilder.Build();
        });

        builder.Services.AddSingleton<MigrationRunner>();
        builder.Services.AddHostedService(sp => sp.GetRequiredService<MigrationRunner>());

        builder.Services.AddSingleton<DatabaseVersions>();
        builder.Services.AddHostedService(sp => sp.GetRequiredService<DatabaseVersions>());
        builder.Services.AddHealthChecks().AddCheck<DatabaseVersions>("database");
    }

    /// <summary>
    /// Adds the database CLI commands for listing and applying migrations.
    /// </summary>
    public static void AddDatabaseCliCommand(this Command parentCommand, Func<IHost> createHost)
    {
        var databaseCommand = new Command("database", "Manage the database");
        parentCommand.AddCommand(databaseCommand);

        databaseCommand.AddListVersionsCommand(createHost);
        databaseCommand.AddInitCommand(createHost);
        databaseCommand.AddMigrateCommand(createHost);
    }

    private static void AddListVersionsCommand(this Command parentCommand, Func<IHost> createHost)
    {
        var listVersionsCommand = new Command("list-versions", "List the current and available database versions");
        parentCommand.AddCommand(listVersionsCommand);

        listVersionsCommand.SetHandler(context => ListDatabaseVersions(
            createHost().Services,
            context.GetCancellationToken()));

        static async Task ListDatabaseVersions(IServiceProvider serviceProvider, CancellationToken cancellationToken)
        {
            var databaseVersions = serviceProvider.GetRequiredService<DatabaseVersions>();
            var serializerOptions = serviceProvider.GetRequiredService<JsonSerializerOptions>();

            var versions = await databaseVersions.GetDatabaseVersions(cancellationToken);

            await using var stdout = Console.OpenStandardOutput();
            JsonSerializer.Serialize(stdout, versions, serializerOptions);
        }
    }

    private static void AddInitCommand(this Command parentCommand, Func<IHost> createHost)
    {
        var initCommand = new Command("init", "Initialize the database");
        parentCommand.AddCommand(initCommand);

        var initTargetVersionOption = new Option<int?>("--target-version", "The target database version");
        initCommand.AddOption(initTargetVersionOption);

        initCommand.SetHandler(context => RunMigrationsCommandImpl(
            serviceProvider: createHost().Services,
            initOnly: true,
            targetVersion: context.ParseResult.GetValueForOption(initTargetVersionOption),
            offline: true,
            context.GetCancellationToken()));
    }

    private static void AddMigrateCommand(this Command parentCommand, Func<IHost> createHost)
    {
        var migrateCommand = new Command("migrate", "Run database migrations");
        parentCommand.AddCommand(migrateCommand);

        var migrateTargetVersionOption = new Option<int>("--target-version", "The target database version") { IsRequired = true };
        migrateCommand.AddOption(migrateTargetVersionOption);
        var offlineOption = new Option<bool>("--offline", "Run migrations assuming there are no server instances connected to the database");
        migrateCommand.AddOption(offlineOption);

        migrateCommand.SetHandler(context => RunMigrationsCommandImpl(
            serviceProvider: createHost().Services,
            initOnly: false,
            context.ParseResult.GetValueForOption(migrateTargetVersionOption),
            context.ParseResult.GetValueForOption(offlineOption),
            context.GetCancellationToken()));
    }

    private static async Task<int> RunMigrationsCommandImpl(IServiceProvider serviceProvider, bool initOnly, int? targetVersion, bool offline, CancellationToken cancellationToken)
    {
        var migrationRunner = serviceProvider.GetRequiredService<MigrationRunner>();

        await migrationRunner.RunMigrations(initOnly, targetVersion, offline, cancellationToken);
        return 0;
    }

    public static void MapDatabaseVersionInUse(this WebApplication app)
    {
        app.MapGet("/v1/database-version-in-use", (DatabaseVersions versions, IOptions<KubernetesApiOptions> kubernetesOptions, HttpContext context) =>
        {
            if (!string.IsNullOrEmpty(kubernetesOptions.Value.CurrentPodUid))
            {
                // We use a custom bearer token to secure this endpoint. The token is the pod UID. This is obviously not very secure,
                // but the response isn't really sensitive.
                // Using the pod UID ensures that only callers with access to this information can call this endpoint.
                // This endpoint is meant to be called by the migration runner.

                // Using a Kubernetes token is another possibility, but verifying the token requires cluster permission to the
                // TokenReview resource, which means that the principal installing the API needs to be able to create ClusterRoleBindings.

                const string BearerPrefix = "Bearer ";

                var authHeader = context.Request.Headers.Authorization.ToString();

                if (authHeader.Length <= BearerPrefix.Length || !authHeader.StartsWith(BearerPrefix, StringComparison.OrdinalIgnoreCase))
                {
                    return Results.Unauthorized();
                }

                var token = authHeader[BearerPrefix.Length..];
                if (!kubernetesOptions.Value.CurrentPodUid.Equals(token, StringComparison.OrdinalIgnoreCase))
                {
                    return Results.Unauthorized();
                }
            }

            return Results.Ok(new DatabaseVersionInUse((int)versions.CachedCurrentVersion));
        })
        .AllowAnonymous()
        .Produces<DatabaseVersionInUse>();
    }
}

public class DatabaseOptions
{
    [Required]
    public required string Host { get; set; }

    public string? DatabaseName { get; set; }

    public int? Port { get; set; }

    [Required]
    public required string Username { get; set; }

    public string? PasswordFile { get; set; }

    [Required]
    public required string TygerServerRoleName { get; set; }

    public required string TygerServerIdentity { get; set; }

    public bool AutoMigrate { get; set; }
}

public static class Constants
{
    public const string OwnersRole = "tyger-owners";

    public const string MigrationsTableName = "migrations";
    public const string DatabaseNamespace = "public"; // technically the database schema

    public const string MigrationStateStarted = "started";
    public const string MigrationStateComplete = "complete";
    public const string MigrationStateFailed = "failed";
}
