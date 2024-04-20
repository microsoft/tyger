// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.WebUtilities;
using Microsoft.Extensions.Primitives;
using Tyger.Common.Api;
using Tyger.ControlPlane.Json;
using Tyger.ControlPlane.Model;
using Buffer = Tyger.ControlPlane.Model.Buffer;

namespace Tyger.ControlPlane.Buffers;

public static class Buffers
{
    public static void AddBuffers(this IHostApplicationBuilder builder)
    {
        builder.Services.AddOptions<BufferOptions>().BindConfiguration("buffers").ValidateDataAnnotations().ValidateOnStart();

        builder.Services.AddSingleton<BufferManager>();

        bool cloudStorageEnabled = builder.Configuration.GetSection("buffers:cloudStorage").Exists();
        bool localStorageEnabled = builder.Configuration.GetSection("buffers:localStorage").Exists();

        switch (cloudStorageEnabled, localStorageEnabled)
        {
            case (true, false):
                builder.Services.AddOptions<CloudBufferStorageOptions>().BindConfiguration("buffers:cloudStorage").ValidateDataAnnotations().ValidateOnStart();
                builder.Services.AddSingleton<AzureBlobBufferProvider>();
                builder.Services.AddSingleton<IBufferProvider>(sp => sp.GetRequiredService<AzureBlobBufferProvider>());
                builder.Services.AddSingleton<IHostedService>(sp => sp.GetRequiredService<AzureBlobBufferProvider>());
                builder.Services.AddHealthChecks().AddCheck<AzureBlobBufferProvider>("buffers");
                break;
            case (false, true):
                builder.Services.AddOptions<LocalBufferStorageOptions>().BindConfiguration("buffers:localStorage").ValidateDataAnnotations().ValidateOnStart();
                builder.Services.AddSingleton<LocalStorageBufferProvider>();
                builder.Services.AddSingleton<IBufferProvider>(sp => sp.GetRequiredService<LocalStorageBufferProvider>());
                builder.Services.AddHealthChecks().AddCheck<LocalStorageBufferProvider>("data plane");
                break;
            case (false, false):
                throw new InvalidOperationException("One of `buffers.localStorage` and `buffers.cloudStorage` must be enabled.");
            case (true, true):
                throw new InvalidOperationException("`buffers.localStorage` and `buffers.cloudStorage` must cannot both be enabled.");
        }
    }

    public static void MapBuffers(this WebApplication app)
    {
        app.MapPost("/v1/buffers", async (BufferManager manager, HttpContext context, CancellationToken cancellationToken) =>
            {
                var newBuffer = await context.Request.ReadAndValidateJson<Buffer>(context.RequestAborted);
                var buffer = await manager.CreateBuffer(newBuffer, cancellationToken);
                context.Response.Headers.ETag = buffer.ETag;
                return Results.CreatedAtRoute("getBufferById", new { buffer.Id }, buffer);
            })
            .Accepts<Buffer>("application/json")
            .WithName("createBuffer")
            .Produces<Buffer>(StatusCodes.Status201Created);

        app.MapGet("/v1/buffers", async (BufferManager manager, HttpContext context, int? limit, [FromQuery(Name = "_ct")] string? continuationToken, CancellationToken cancellationToken) =>
            {
                limit = limit is null ? 20 : Math.Min(limit.Value, 200);
                var tagQuery = new Dictionary<string, string>();

                foreach (var tag in context.Request.Query)
                {
                    if (tag.Key.StartsWith("tag.", StringComparison.Ordinal))
                    {
                        tagQuery.Add(tag.Key[4..], tag.Value.FirstOrDefault() ?? "");
                    }
                }

                if (tagQuery.Count == 0)
                {
                    tagQuery = null;
                }

                (var buffers, var nextContinuationToken) = await manager.GetBuffers(tagQuery, limit.Value, continuationToken, cancellationToken);

                string? nextLink;
                if (nextContinuationToken is null)
                {
                    nextLink = null;
                }
                else if (context.Request.QueryString.HasValue)
                {
                    var qd = QueryHelpers.ParseQuery(context.Request.QueryString.Value);
                    qd["_ct"] = new StringValues(nextContinuationToken);
                    nextLink = QueryHelpers.AddQueryString(context.Request.Path, qd);
                }
                else
                {
                    nextLink = QueryHelpers.AddQueryString(context.Request.Path, "_ct", nextContinuationToken);
                }

                return Results.Ok(new BufferPage(buffers, nextLink == null ? null : new Uri(nextLink)));
            })
            .WithName("getBuffers")
            .Produces<BufferPage>();

        app.MapGet("/v1/buffers/{id}", async (BufferManager manager, HttpContext context, string id, CancellationToken cancellationToken) =>
            {
                var buffer = await manager.GetBufferById(id, cancellationToken);
                if (buffer != null)
                {
                    context.Response.Headers.ETag = buffer.ETag;
                    return Results.Ok(buffer);
                }

                return Results.NotFound();
            })
            .WithName("getBufferById")
            .Produces<Buffer>(StatusCodes.Status200OK)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound);

        app.MapPut("/v1/buffers/{id}/tags", async (BufferManager manager, HttpContext context, string id, CancellationToken cancellationToken) =>
            {
                string eTag = context.Request.Headers.IfMatch.FirstOrDefault() ?? "";
                if (eTag == "*") // if-match: * matches everything
                {
                    eTag = "";
                }

                var newTags = await context.Request.ReadAndValidateJson<IDictionary<string, string>>(context.RequestAborted, allowEmpty: true);
                newTags = Normalizer.NormalizeEmptyToNull(newTags);

                var buffer = await manager.UpdateBufferById(id, eTag, newTags, cancellationToken);

                if (buffer != null)
                {
                    context.Response.Headers.ETag = buffer.ETag;
                    return Results.Ok(buffer);
                }
                else if (eTag != "")
                {
                    buffer = await manager.GetBufferById(id, cancellationToken);
                    if (buffer != null)
                    {
                        return Results.StatusCode(StatusCodes.Status412PreconditionFailed);
                    }
                }

                return Results.NotFound();
            })
            .WithName("setBufferTags")
            .Accepts<IDictionary<string, string>>("application/json")
            .Produces<Buffer>(StatusCodes.Status200OK)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound)
            .Produces<ErrorBody>(StatusCodes.Status412PreconditionFailed);

        app.MapPost("/v1/buffers/{id}/access", async (BufferManager manager, string id, bool? writeable, bool? preferTcp, CancellationToken cancellationToken) =>
            {
                var bufferAccess = await manager.CreateBufferAccessUrl(id, writeable == true, preferTcp == true, cancellationToken);
                if (bufferAccess is null)
                {
                    return Responses.NotFound();
                }

                return Results.Json(bufferAccess, statusCode: StatusCodes.Status201Created);
            })
            .WithName("getBufferAccessString")
            .Produces<BufferAccess>(StatusCodes.Status201Created)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound);
    }
}

public class BufferOptions
{
    [Required]
    public string BufferSidecarImage { get; set; } = null!;

    public string PrimarySigningPrivateKeyPath { get; init; } = null!;
    public string SecondarySigningPrivateKeyPath { get; init; } = null!;
}

public class CloudBufferStorageOptions
{
    [Required, MinLength(1)]
    public IList<BufferStorageAccountOptions> StorageAccounts { get; } = [];
}

public class LocalBufferStorageOptions
{
    [Required]
    public Uri DataPlaneEndpoint { get; init; } = null!;

    [Required]
    public Uri TcpDataPlaneEndpoint { get; init; } = null!;
}

public class BufferStorageAccountOptions
{
    [Required]
    public required string Name { get; init; }

    [Required]
    public required string Location { get; init; }

    [Required]
    public required string Endpoint { get; init; }
}
