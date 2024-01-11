using System.CommandLine;
using System.ComponentModel.DataAnnotations;
using System.Text.Json;
using Tyger.Server;
using Tyger.Server.Auth;
using Tyger.Server.Buffers;
using Tyger.Server.Codespecs;
using Tyger.Server.Configuration;
using Tyger.Server.Database;
using Tyger.Server.Database.Migrations;
using Tyger.Server.Identity;
using Tyger.Server.Json;
using Tyger.Server.Kubernetes;
using Tyger.Server.Logging;
using Tyger.Server.Middleware;
using Tyger.Server.OpenApi;
using Tyger.Server.Runs;
using Tyger.Server.ServiceMetadata;

// Parse command-line arguments to see if we should run migrations or start the server.
var rootCommand = new RootCommand("Tyger Server");
rootCommand.SetHandler(RunServer);

var databaseCommand = new Command("database", "Manage the database");
rootCommand.AddCommand(databaseCommand);

var initCommand = new Command("init", "Initialize the database");
var initTargetVersionOption = new Option<int?>("--target-version", "The target database version");
initCommand.AddOption(initTargetVersionOption);
initCommand.SetHandler(context => RunMigrations(
    initOnly: true,
    targetVersion: context.ParseResult.GetValueForOption(initTargetVersionOption),
    offline: true,
    context.GetCancellationToken()));
databaseCommand.AddCommand(initCommand);

var listVersionsCommand = new Command("list-versions", "List the current and available database versions");
listVersionsCommand.SetHandler(context => ListDatabaseVersions(context.GetCancellationToken()));
databaseCommand.AddCommand(listVersionsCommand);

var migrateCommand = new Command("migrate", "Run database migrations");
var migrateTargetVersionOption = new Option<int>("--target-version", "The target database version") { IsRequired = true };
var offlineOption = new Option<bool>("--offline", "Run migrations assuming there are no server instances connected to the database");
migrateCommand.AddOption(migrateTargetVersionOption);
migrateCommand.SetHandler(context => RunMigrations(
    initOnly: false,
    context.ParseResult.GetValueForOption(migrateTargetVersionOption),
    context.ParseResult.GetValueForOption(offlineOption),
    context.GetCancellationToken()));
databaseCommand.AddCommand(migrateCommand);

return await rootCommand.InvokeAsync(args);

async Task ListDatabaseVersions(CancellationToken cancellationToken)
{
    using var host = ConfigureHostBuilder(Host.CreateApplicationBuilder(args)).Build();

    var databaseVersions = host.Services.GetRequiredService<DatabaseVersions>();
    var serializerOptions = host.Services.GetRequiredService<JsonSerializerOptions>();

    var versions = await databaseVersions.GetDatabaseVersions(cancellationToken);

    await using var stdout = Console.OpenStandardOutput();
    JsonSerializer.Serialize(stdout, versions, serializerOptions);
}

async Task<int> RunMigrations(bool initOnly, int? targetVersion, bool offline, CancellationToken cancellationToken)
{
    using var host = ConfigureHostBuilder(Host.CreateApplicationBuilder(args)).Build();

    var migrationRunner = host.Services.GetRequiredService<MigrationRunner>();
    var logger = host.Services.GetRequiredService<ILogger<Program>>();

    try
    {
        await migrationRunner.RunMigrations(initOnly, targetVersion, offline, CancellationToken.None);
        return 0;
    }
    catch (ValidationException ex)
    {
        logger.MigrationValidationError(ex.Message);
        return 1;
    }
    catch (Exception ex)
    {
        logger.UnhandledMigrationException(ex);
        return 1;
    }
}

T ConfigureHostBuilder<T>(T builder) where T : IHostApplicationBuilder
{
    bool isApi = builder is WebApplicationBuilder;

    // Configuration
    builder.Configuration.AddConfigurationSources();

    // Logging
    builder.Logging.ConfigureLogging();

    // Services
    builder.Services.AddManagedIdentity();
    builder.Services.AddDatabase();
    builder.Services.AddKubernetes(isApi);
    builder.Services.AddJsonFormatting();

    if (isApi)
    {
        builder.Services.AddLogArchive();
        builder.Services.AddAuth();
        builder.Services.AddBuffers();
        builder.Services.AddOpenApi();
        builder.Services.AddHealthChecks();
    }

    return builder;
}

void RunServer()
{
    var app = ConfigureHostBuilder(WebApplication.CreateBuilder(args)).Build();

    // Middleware and routes
    app.UseRequestLogging();
    app.UseRequestId();
    app.UseBaggage();
    app.UseExceptionHandling();

    app.UseOpenApi();
    app.UseAuth();

    app.MapClusters();
    app.MapBuffers();
    app.MapCodespecs();
    app.MapRuns();

    app.MapServiceMetadata();
    app.MapDatabaseVersionInUse();
    app.MapHealthChecks("/healthcheck").AllowAnonymous();

    app.MapFallback(() => Responses.BadRequest("InvalidRoute", "The request path was not recognized."));

    // Run
    app.Run();
}
