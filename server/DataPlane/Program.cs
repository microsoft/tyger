// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.Common.Api;
using Tyger.Common.Configuration;
using Tyger.Common.Logging;
using Tyger.Common.Middleware;
using Tyger.Common.Unix;
using Tyger.Common.Versioning;
using Tyger.DataPlane;
using Tyger.DataPlane.Versioning;

var builder = WebApplication.CreateBuilder();

builder.AddConfigurationSources();
builder.ConfigureLogging();
builder.Services.AddEndpointsApiExplorer();
builder.AddDataPlane();
builder.AddApiVersioning();
builder.ConfigureUnixDomainSockets();

var app = builder.Build();

// Middleware and routes
app.UseRequestLogging();
app.UseRequestId();
app.UseBaggage();
app.UseApiV1BackwardCompatibility();

app.MapHealthChecks("/healthcheck").AllowAnonymous();
app.MapFallback(() => Responses.InvalidRoute("The request path was not recognized.")).AllowAnonymous();

var api = app.ConfigureVersionedRouteGroup("/");
api.MapDataPlane();

app.Run();
