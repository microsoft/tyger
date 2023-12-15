using System.CommandLine;
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

var initCommand = new Command("init", "Initialize the database");
initCommand.SetHandler(async () => await RunMigrations(true));
rootCommand.AddCommand(initCommand);

var res = rootCommand.Parse(args);

return await rootCommand.InvokeAsync(args);

async Task RunMigrations(bool initOnly)
{
    var host = ConfigureHostBuilder(Host.CreateApplicationBuilder(args)).Build();

    var migrationRunner = host.Services.GetRequiredService<MigrationRunner>();

    await migrationRunner.RunMigrations(initOnly, null, CancellationToken.None);
}

T ConfigureHostBuilder<T>(T builder) where T : IHostApplicationBuilder
{
    // Configuration
    builder.Configuration.AddConfigurationSources();

    // Logging
    builder.Logging.ConfigureLogging();

    // Services
    builder.Services.AddManagedIdentity();
    builder.Services.AddDatabase();
    builder.Services.AddKubernetes();
    builder.Services.AddLogArchive();
    builder.Services.AddAuth();
    builder.Services.AddBuffers();
    builder.Services.AddOpenApi();
    builder.Services.AddHealthChecks();
    builder.Services.AddJsonFormatting();

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
    app.MapHealthChecks("/healthcheck").AllowAnonymous();
    app.MapFallback(() => Responses.BadRequest("InvalidRoute", "The request path was not recognized."));

    // Run
    app.Run();
}
