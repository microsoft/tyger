using Microsoft.AspNetCore.Http.Json;
using Microsoft.Extensions.Logging.Console;
using Microsoft.Extensions.Options;
using Tyger.Server;
using Tyger.Server.Auth;
using Tyger.Server.Buffers;
using Tyger.Server.Codespecs;
using Tyger.Server.Database;
using Tyger.Server.Kubernetes;
using Tyger.Server.Logging;
using Tyger.Server.Middleware;
using Tyger.Server.Model;
using Tyger.Server.Runs;
using Tyger.Server.StorageServer;

var builder = WebApplication.CreateBuilder(args);

// Configuration
builder.Configuration.AddIniFile("localsettings.ini", optional: true);
builder.Configuration.AddEnvironmentVariables("TYGER__");
builder.Configuration.AddKeyPerFile(builder.Configuration.GetValue<string>("KeyPerFileDirectory"), optional: true);

// Logging
builder.Logging.AddConsoleFormatter<LogFormatter, ConsoleFormatterOptions>();
builder.WebHost.ConfigureLogging(l => l.Configure(o => o.ActivityTrackingOptions = ActivityTrackingOptions.None));

// Services
builder.Services.AddDatabase();
builder.Services.AddKubernetes();
builder.Services.AddAuth();
builder.Services.AddBuffers();
builder.Services.AddStorageServer();

builder.Services.AddEndpointsApiExplorer();
builder.Services.AddSwaggerGen();

builder.Services.AddHealthChecks();

builder.Services.Configure<JsonOptions>(options =>
{
    options.SerializerOptions.DefaultIgnoreCondition = System.Text.Json.Serialization.JsonIgnoreCondition.WhenWritingDefault;
    options.SerializerOptions.AllowTrailingCommas = true;
});

var app = builder.Build();

// Middleware and routes
app.UseRequestLogging();
app.UseRequestId();
app.UseExceptionHandling();

app.UseSwagger();
app.UseSwaggerUI();
app.UseAuth();

app.MapBuffers();
app.MapCodespecs();
app.MapRuns();

app.MapHealthChecks("/healthcheck").AllowAnonymous();
app.MapGet("/v1/metadata", (IOptions<AuthOptions> auth) => auth.Value.Enabled ? new Metadata(auth.Value.Authority, auth.Value.Audience) : new Metadata())
    .AllowAnonymous();

app.MapFallback(() => Responses.BadRequest("InvalidRoute", "The request path was not recognized."));

await Database.EnsureCreated(app.Services);

// Run
app.Run();
