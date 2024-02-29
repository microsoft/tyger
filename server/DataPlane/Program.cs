using Tyger.Api;
using Tyger.Configuration;
using Tyger.DataPlane;
using Tyger.Middleware;
using Tyger.Logging;
using Tyger.UnixDomainSockets;

var builder = WebApplication.CreateBuilder();

builder.AddConfigurationSources();
builder.ConfigureLogging();
builder.Services.AddEndpointsApiExplorer();
builder.AddDataPlane();
builder.EnsureUnixDomainSocketsDeleted();

var app = builder.Build();

// Middleware and routes
app.UseRequestLogging();
app.UseRequestId();
app.UseBaggage();

app.MapDataPlane();

app.MapHealthChecks("/healthcheck").AllowAnonymous();

app.MapFallback(() => Responses.BadRequest("InvalidRoute", "The request path was not recognized."));

app.Run();
