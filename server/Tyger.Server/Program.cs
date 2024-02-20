// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.CommandLine;
using System.CommandLine.Parsing;
using Tyger.Server.Auth;
using Tyger.Server.Buffers;
using Tyger.Server.Codespecs;
using Tyger.Server.Compute;
using Tyger.Server.Configuration;
using Tyger.Server.Database;
using Tyger.Server.Identity;
using Tyger.Server.Json;
using Tyger.Server.Logging;
using Tyger.Server.Middleware;
using Tyger.Server.Model;
using Tyger.Server.OpenApi;
using Tyger.Server.Runs;
using Tyger.Server.ServiceMetadata;

var rootCommand = new RootCommand("Tyger Server");
rootCommand.SetHandler(RunServer);

rootCommand.AddDatabaseCliCommand(() =>
    {
        var builder = Host.CreateApplicationBuilder();
        AddCommonServices(builder);
        return builder.Build();
    });

return await rootCommand.InvokeAsync(args);

void AddCommonServices(IHostApplicationBuilder builder)
{
    builder.AddConfigurationSources();
    builder.AddCompute();
    builder.ConfigureLogging();
    builder.AddManagedIdentity();
    builder.AddDatabase();
    builder.AddJsonFormatting();
}

void RunServer()
{
    var builder = WebApplication.CreateBuilder();

    AddCommonServices(builder);
    builder.AddServiceMetadata();
    builder.AddLogArchive();
    builder.AddAuth();
    builder.AddBuffers();
    builder.AddOpenApi();

    var app = builder.Build();

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
