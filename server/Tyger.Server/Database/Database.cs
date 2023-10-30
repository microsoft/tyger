using System.ComponentModel.DataAnnotations;
using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Options;
using Npgsql;
using Npgsql.Internal;
using Npgsql.Internal.TypeHandlers;
using Npgsql.Internal.TypeHandling;
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

        services.AddScoped<IRepository, RepositoryWithRetry>();
        services.AddDbContext<TygerDbContext>((sp, options) =>
            {
                var databaseOptions = sp.GetRequiredService<IOptions<DatabaseOptions>>().Value;
                var connectionString = databaseOptions.ConnectionString;
                if (!string.IsNullOrEmpty(databaseOptions.Password))
                {
                    connectionString = $"{connectionString}; Password={databaseOptions.Password}";
                }

                var dataSourceBuilder = new NpgsqlDataSourceBuilder(connectionString);
                dataSourceBuilder.AddTypeResolverFactory(new JsonOverrideTypeHandlerResolverFactory(sp.GetRequiredService<JsonSerializerOptions>()));
                var dataSource = dataSourceBuilder.Build();

                options.UseNpgsql(dataSource)
                    .UseSnakeCaseNamingConvention();

            },
            contextLifetime: ServiceLifetime.Scoped, optionsLifetime: ServiceLifetime.Singleton);
    }

    public static async Task EnsureCreated(IServiceProvider serviceProvider)
    {
        using var scope = serviceProvider.CreateScope();
        using var context = scope.ServiceProvider.GetRequiredService<TygerDbContext>();
        await context.Database.EnsureCreatedAsync();
    }

    /// <summary>
    /// Some ceremory to plumb in the JsonSerializerOptions we want to use for JSONB columns.
    /// Adapted from https://github.com/npgsql/efcore.pg/issues/1107#issuecomment-945126627
    /// </summary>
    private sealed class JsonOverrideTypeHandlerResolverFactory : TypeHandlerResolverFactory
    {
        private readonly JsonSerializerOptions _jsonSerializerOptions;

        public JsonOverrideTypeHandlerResolverFactory(JsonSerializerOptions jsonSerializerOptions) => _jsonSerializerOptions = jsonSerializerOptions;

        public override TypeHandlerResolver Create(NpgsqlConnector connector) => new JsonOverrideTypeHandlerResolver(connector, _jsonSerializerOptions);

        public override string? GetDataTypeNameByClrType(Type clrType) => null;

        public override TypeMappingInfo? GetMappingByDataTypeName(string dataTypeName) => null;

        private sealed class JsonOverrideTypeHandlerResolver : TypeHandlerResolver
        {
            private readonly JsonHandler _jsonbHandler;

            internal JsonOverrideTypeHandlerResolver(NpgsqlConnector connector, JsonSerializerOptions options)
            {
                _jsonbHandler = new JsonHandler(
                 connector.DatabaseInfo.GetPostgresTypeByName("jsonb"),
                 connector.TextEncoding,
                 isJsonb: true,
                 options);
            }

            public override NpgsqlTypeHandler? ResolveByDataTypeName(string typeName)
            {
                return typeName == "jsonb" ? _jsonbHandler : null;
            }

            public override NpgsqlTypeHandler? ResolveByClrType(Type type) => null;

            public override TypeMappingInfo? GetMappingByDataTypeName(string dataTypeName) => null;
        }
    }
}

public class DatabaseOptions
{
    [Required]
    public string ConnectionString { get; set; } = null!;

    public string? Password { get; set; }
}
