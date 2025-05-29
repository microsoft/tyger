// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.CommandLine;
using System.CommandLine.Parsing;
using Tyger.Common.Api;
using Tyger.Common.Configuration;
using Tyger.Common.Logging;
using Tyger.Common.Middleware;
using Tyger.Common.Unix;
using Tyger.Common.Versioning;
using Tyger.ControlPlane.AccessControl;
using Tyger.ControlPlane.Buffers;
using Tyger.ControlPlane.Codespecs;
using Tyger.ControlPlane.Compute;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Identity;
using Tyger.ControlPlane.Json;
using Tyger.ControlPlane.Logging;
using Tyger.ControlPlane.Middleware;
using Tyger.ControlPlane.OpenApi;
using Tyger.ControlPlane.Runs;
using Tyger.ControlPlane.ServiceMetadata;
using Tyger.ControlPlane.Versioning;

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
    builder.AddDatabase();
    builder.AddCompute();
    builder.AddBuffers();
    builder.ConfigureExceptionHandling();
    builder.ConfigureLogging();
    builder.AddManagedIdentity();
    builder.AddJsonFormatting();
}

void RunServer()
{
    var builder = WebApplication.CreateBuilder();

    AddCommonServices(builder);
    builder.AddCodespecs();
    builder.AddLogArchive();
    builder.AddAccessControl();
    builder.AddRuns();
    builder.AddApiVersioning();
    builder.AddOpenApi();
    builder.ConfigureUnixDomainSockets();

    var app = builder.Build();

    // Middleware and routes
    app.UseRequestLogging();
    app.UseRequestId();
    app.UseBaggage();
    app.UseExceptionHandling();
    app.UseApiV1BackwardCompatibility();
    app.UseAuth();

    app.MapServiceMetadata();
    app.MapDatabaseVersionInUse();
    app.MapHealthChecks("/healthcheck").AllowAnonymous();
    app.MapSwagger().AllowAnonymous();
    app.MapFallback(() => Responses.InvalidRoute("The request path was not recognized.")).AllowAnonymous();

    var api = app.ConfigureVersionedRouteGroup("/");

    api.MapBuffers();
    api.MapCodespecs();
    api.MapRuns();

    app.UseOpenApi();
    app.Run();
}
