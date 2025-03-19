// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.Common.Api;
using Tyger.Common.Configuration;
using Tyger.Common.Logging;
using Tyger.Common.Middleware;
using Tyger.Common.Unix;
using Tyger.DataPlane;

var builder = WebApplication.CreateBuilder();

builder.AddConfigurationSources();
builder.ConfigureLogging();
builder.Services.AddEndpointsApiExplorer();
builder.AddDataPlane();
builder.ConfigureUnixDomainSockets();

var app = builder.Build();

// Middleware and routes
app.UseRequestLogging();
app.UseRequestId();
app.UseBaggage();

app.MapDataPlane();

app.MapHealthChecks("/healthcheck").AllowAnonymous();

app.MapFallback(() => Responses.InvalidRoute("The request path was not recognized."));

app.Run();
