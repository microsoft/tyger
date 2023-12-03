using System.ComponentModel.DataAnnotations;
using Microsoft.Extensions.Options;
using Npgsql;
using Polly;
using Polly.Retry;

namespace Tyger.Server.Database;

public static class Database
{
    public static void AddDatabase(this IServiceCollection services)
    {
        services.AddOptions<DatabaseOptions>().BindConfiguration("database").ValidateDataAnnotations().ValidateOnStart();
        services.AddSingleton(sp =>
        {
            var logger = sp.GetService<ILogger<RepositoryWithRetry>>();
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
            var connectionString = databaseOptions.ConnectionString;
            if (!string.IsNullOrEmpty(databaseOptions.Password))
            {
                connectionString = $"{connectionString}; Password={databaseOptions.Password}";
            }

            var dataSourceBuilder = new NpgsqlDataSourceBuilder(connectionString);
            return dataSourceBuilder.Build();
        });

        services.AddSingleton<Migrations.MigrationRunner>();
    }

    public static async Task EnsureCreated(IServiceProvider serviceProvider)
    {
    }
}

public class DatabaseOptions
{
    [Required]
    public string ConnectionString { get; set; } = null!;

    public string? Password { get; set; }
}
