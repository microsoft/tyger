using System.ComponentModel.DataAnnotations;
using Azure.Core;
using Microsoft.Extensions.Options;
using Npgsql;
using Polly;
using Polly.Retry;
using Tyger.Server.Database.Migrations;
using Tyger.Server.Kubernetes;
using Tyger.Server.Model;

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

            return dataSourceBuilder.Build();
        });

        services.AddSingleton<MigrationRunner>();
        services.AddSingleton<IHostedService, MigrationRunner>(sp => sp.GetRequiredService<MigrationRunner>());

        services.AddSingleton<DatabaseVersions>();
        services.AddSingleton<IHostedService, DatabaseVersions>(sp => sp.GetRequiredService<DatabaseVersions>());
        services.AddHealthChecks().AddCheck<DatabaseVersions>("database");
    }
    public static void MapDatabaseVersionInUse(this WebApplication app)
    {
        app.MapGet("/v1/database-version-in-use", (DatabaseVersions versions, IOptions<KubernetesApiOptions> kubernetesOptions, HttpContext context) =>
        {
            // We use a custom bearer token to secure this endpoint. The token is the pod UID. This is obviously not very secure,
            // but the response isn't really sensitive.
            // Using the pod UID ensures that only callers with access to this information can call this endpoint.
            // This endpoint is meant to be called by the migration runner.

            // Using a Kubernetes token is another possiblity, but verifying the token requires cluster permission to the
            // TokenReview resource, which means that the principal installing the API needs to be able to create ClusterRoleBindings.

            const string BearerPrefix = "Bearer ";

            var authHeader = context.Request.Headers.Authorization.ToString();

            if (authHeader.Length > BearerPrefix.Length && authHeader.StartsWith(BearerPrefix, StringComparison.OrdinalIgnoreCase))
            {
                var token = authHeader[BearerPrefix.Length..];
                if (kubernetesOptions.Value.CurrentPodUid.Equals(token, StringComparison.OrdinalIgnoreCase))
                {
                    return Results.Ok(new DatabaseVersionInUse((int)versions.CachedCurrentVersion));
                }
            }

            return Results.Unauthorized();
        })
        .AllowAnonymous()
        .Produces<DatabaseVersionInUse>();
    }
}

public class DatabaseOptions
{
    [Required]
    public string ConnectionString { get; set; } = null!;

    [Required]
    public required string TygerServerRoleName { get; set; }

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
