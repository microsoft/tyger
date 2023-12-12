using System.ComponentModel.DataAnnotations;
using Azure.Core;
using Microsoft.Extensions.Options;
using Npgsql;
using Polly;
using Polly.Retry;
using Tyger.Server.Database.Migrations;

namespace Tyger.Server.Database;

public static class Database
{
    private static readonly string[] s_scopes = ["https://ossrdbms-aad.database.windows.net/.default"];

    public static void AddDatabase(this IServiceCollection services)
    {
        services.AddOptions<DatabaseOptions>().BindConfiguration("database").ValidateDataAnnotations().ValidateOnStart();
        services.AddSingleton(sp =>
        {
            var logger = sp.GetService<ILogger<ResiliencePipeline>>();
            return new ResiliencePipelineBuilder().AddRetry(new RetryStrategyOptions
            {
                ShouldHandle = new PredicateBuilder().Handle<NpgsqlException>(e => e.IsTransient),
                BackoffType = DelayBackoffType.Exponential,
                UseJitter = true,
                MaxRetryAttempts = 4,
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

        services.AddSingleton<IRepository, RepositoryWithRetry>();

        services.AddSingleton(sp =>
        {
            var databaseOptions = sp.GetRequiredService<IOptions<DatabaseOptions>>().Value;
            var dataSourceBuilder = new NpgsqlDataSourceBuilder(databaseOptions.ConnectionString);

            var tokenCredential = sp.GetRequiredService<TokenCredential>();
            dataSourceBuilder.UsePeriodicPasswordProvider(
                async (b, ct) =>
                {
                    var resp = await tokenCredential.GetTokenAsync(new TokenRequestContext(scopes: s_scopes), ct);
                    return resp.Token;
                },
                TimeSpan.FromMinutes(30),
                TimeSpan.FromMinutes(1));

            return dataSourceBuilder.Build();
        });

        services.AddSingleton<MigrationRunner>();
        services.AddSingleton<IHostedService, MigrationRunner>(sp => sp.GetRequiredService<MigrationRunner>());

        services.AddSingleton<DatabaseVersions>();
        services.AddSingleton<IHostedService, DatabaseVersions>(sp => sp.GetRequiredService<DatabaseVersions>());
        services.AddHealthChecks().AddCheck<DatabaseVersions>("database");
    }
}

public class DatabaseOptions
{
    [Required]
    public string ConnectionString { get; set; } = null!;

    public bool AutoMigrate { get; set; }
}

public static class Constants
{
    public const string ServerRole = "tyger-server";

    public const string OwnersRole = "tyger-owners";

    public const string MigrationsTableName = "migrations";
    public const string DatabaseNamespace = "public"; // technically the database schema

    public const string MigrationStateStarted = "started";
    public const string MigrationStateComplete = "complete";
    public const string MigrationStateFailed = "failed";
}
