using System.Text.Json;
using System.Text.Json.Serialization;
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
using Tyger.Server.OpenApi;
using Tyger.Server.Runs;

var builder = WebApplication.CreateBuilder(args);

// Configuration
builder.Configuration.AddJsonFile("appsettings.local.json", optional: true);
if (builder.Configuration.GetValue<string>("KeyPerFileDirectory") is string keyPerFileDir)
{
    builder.Configuration.AddKeyPerFile(keyPerFileDir, optional: true);
}

if (builder.Configuration.GetValue<string>("AppSettingsDirectory") is string settingsDir)
{
    builder.Configuration.AddJsonFile(Path.Combine(settingsDir, "appsettings.json"), optional: false);
}

// Logging
builder.Logging.AddConsoleFormatter<JsonFormatter, ConsoleFormatterOptions>();
builder.Logging.Configure(l => l.ActivityTrackingOptions = ActivityTrackingOptions.None);

// Services
builder.Services.AddDatabase();
builder.Services.AddKubernetes();
builder.Services.AddLogArchive();
builder.Services.AddAuth();
builder.Services.AddBuffers();
builder.Services.AddOpenApi();
builder.Services.AddHealthChecks();

builder.Services.Configure<JsonOptions>(options =>
{
    options.SerializerOptions.DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingDefault;
    options.SerializerOptions.AllowTrailingCommas = true;
    options.SerializerOptions.Converters.Add(new JsonStringEnumConverter(JsonNamingPolicy.CamelCase));
});
builder.Services.AddSingleton(sp => sp.GetRequiredService<IOptions<JsonOptions>>().Value.SerializerOptions);

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

app.MapHealthChecks("/healthcheck").AllowAnonymous();
app.MapGet("/v1/metadata", (IOptions<AuthOptions> auth) => auth.Value.Enabled ? new Metadata(auth.Value.Authority, auth.Value.Audience, auth.Value.CliAppUri) : new Metadata())
    .AllowAnonymous();

app.MapFallback(() => Responses.BadRequest("InvalidRoute", "The request path was not recognized."));

// Run
app.Run();
