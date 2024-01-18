// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.CommandLine;
using System.CommandLine.Parsing;
using Tyger.Server.Auth;
using Tyger.Server.Buffers;
using Tyger.Server.Codespecs;
using Tyger.Server.Configuration;
using Tyger.Server.Database;
using Tyger.Server.Identity;
using Tyger.Server.Json;
using Tyger.Server.Kubernetes;
using Tyger.Server.Logging;
using Tyger.Server.Middleware;
using Tyger.Server.Model;
using Tyger.Server.OpenApi;
using Tyger.Server.Runs;
using Tyger.Server.ServiceMetadata;

var rootCommand = new RootCommand("Tyger Server");
rootCommand.SetHandler(RunServer);

rootCommand.AddDatabaseCliCommand(CreateNonWebHost);

return await rootCommand.InvokeAsync(args);

T InitializeHostBuilder<T>(T builder) where T : IHostApplicationBuilder
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

WebApplication CreateWebApplication()
{
    return InitializeHostBuilder(WebApplication.CreateBuilder()).Build();
}

IHost CreateNonWebHost()
{
    return InitializeHostBuilder(Host.CreateApplicationBuilder()).Build();
}

void RunServer()
{
    var app = CreateWebApplication();

    // Middleware and routes
    app.UseRequestLogging();
    app.UseRequestId();
    app.UseBaggage();
    app.UseExceptionHandling();

    app.UseOpenApi();
    app.UseAuth();

    app.MapBuffers();
    app.MapCodespecs();
    app.MapRuns();

    app.MapServiceMetadata();
    app.MapDatabaseVersionInUse();
    app.MapHealthChecks("/healthcheck").AllowAnonymous();

    app.MapFallback(() => Responses.BadRequest("InvalidRoute", "The request path was not recognized."));

    app.Run();
}
