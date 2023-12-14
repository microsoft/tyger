using Tyger.Server;
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
using Tyger.Server.OpenApi;
using Tyger.Server.Runs;
using Tyger.Server.ServiceMetadata;

var builder = WebApplication.CreateBuilder(args);

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

var app = builder.Build();

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
