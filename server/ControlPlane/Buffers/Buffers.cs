// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.WebUtilities;
using Microsoft.Extensions.Primitives;
using Tyger.Common.Api;
using Tyger.Common.DependencyInjection;
using Tyger.ControlPlane.Json;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.OpenApi;
using Tyger.ControlPlane.Versioning;
using Buffer = Tyger.ControlPlane.Model.Buffer;

namespace Tyger.ControlPlane.Buffers;

public static class Buffers
{
    public static void AddBuffers(this IHostApplicationBuilder builder)
    {
        builder.Services.AddOptions<BufferOptions>().BindConfiguration("buffers").ValidateDataAnnotations().ValidateOnStart();

        if (builder is WebApplicationBuilder)
        {
            builder.Services.AddSingleton<BufferManager>();
            builder.Services.AddHostedService<BufferDeleter>();
        }

        bool cloudStorageEnabled = builder.Configuration.GetSection("buffers:cloudStorage").Exists();
        bool localStorageEnabled = builder.Configuration.GetSection("buffers:localStorage").Exists();

        switch (cloudStorageEnabled, localStorageEnabled)
        {
            case (true, false):
                builder.Services.AddOptions<CloudBufferStorageOptions>().BindConfiguration("buffers:cloudStorage").ValidateDataAnnotations().ValidateOnStart();
                if (builder is WebApplicationBuilder)
                {
                    builder.Services.AddSingleton<AzureBlobBufferProvider>();
                    builder.Services.AddSingleton<IBufferProvider>(sp => sp.GetRequiredService<AzureBlobBufferProvider>());
                    builder.AddServiceWithPriority(ServiceDescriptor.Singleton<IHostedService>(sp => sp.GetRequiredService<AzureBlobBufferProvider>()), 10);
                    builder.Services.AddHealthChecks().AddCheck<AzureBlobBufferProvider>("buffers");
                }

                break;
            case (false, true):
                builder.Services.AddOptions<LocalBufferStorageOptions>().BindConfiguration("buffers:localStorage").ValidateDataAnnotations().ValidateOnStart();
                if (builder is WebApplicationBuilder)
                {
                    builder.Services.AddSingleton<LocalStorageBufferProvider>();
                    builder.Services.AddSingleton<IBufferProvider>(sp => sp.GetRequiredService<LocalStorageBufferProvider>());
                    builder.Services.AddHostedService(sp => sp.GetRequiredService<LocalStorageBufferProvider>());
                    builder.Services.AddHealthChecks().AddCheck<LocalStorageBufferProvider>("data plane");
                }

                break;
            case (false, false):
                throw new InvalidOperationException("One of `buffers.localStorage` and `buffers.cloudStorage` must be enabled.");
            case (true, true):
                throw new InvalidOperationException("`buffers.localStorage` and `buffers.cloudStorage` must cannot both be enabled.");
        }
    }

    public static void MapBuffers(this RouteGroupBuilder root)
    {
        var buffers = root.MapGroup("/buffers");

        buffers.MapPost("/", async (BufferManager manager, HttpContext context, CancellationToken cancellationToken) =>
            {
                var newBuffer = await context.Request.ReadAndValidateJson<Buffer>(context.RequestAborted);
                var buffer = await manager.CreateBuffer(newBuffer, cancellationToken);
                context.Response.Headers.ETag = buffer.ETag;
                return Results.Created($"/buffers/{buffer.Id}", buffer);
            })
            .Accepts<Buffer>("application/json")
            .WithName("createBuffer")
            .Produces<Buffer>(StatusCodes.Status201Created);

        buffers.MapGet("/", async (BufferManager manager, HttpContext context, int? limit, [FromQuery(Name = "_ct")] string? continuationToken, CancellationToken cancellationToken) =>
            {
                limit = limit is null ? 20 : Math.Min(limit.Value, 2000);
                var tagQuery = context.GetTagQueryParameters();
                var excludeTagQuery = context.GetTagQueryParameters("excludeTag");

                var softDeleted = false;
                if (context.Request.Query.TryGetValue("softDeleted", out var softDeletedQuery))
                {
                    if (!bool.TryParse(softDeletedQuery, out softDeleted))
                    {
                        return Responses.BadRequest("softDeleted must be true or false");
                    }
                }

                (var buffers, var nextContinuationToken) = await manager.GetBuffers(tagQuery, excludeTagQuery, softDeleted, limit.Value, continuationToken, cancellationToken);

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

                return Results.Ok(new BufferPage(buffers.AsReadOnly(), nextLink == null ? null : new Uri(nextLink)));
            })
            .WithName("getBuffers")
            .Produces<BufferPage>()
            .Produces<ErrorBody>(StatusCodes.Status400BadRequest)
            .WithTagsQueryParameters();

        buffers.MapDelete("/", async (BufferManager manager, HttpContext context, CancellationToken cancellationToken) =>
            {
                if (!context.ParseAndValidateTtlQueryParameter(out var ttl))
                {
                    return Responses.BadRequest("ttl must be a valid, non-negative TimeSpan");
                }

                var purge = false;
                if (context.Request.Query.TryGetValue("softDeleted", out var softDeletedQuery))
                {
                    if (!bool.TryParse(softDeletedQuery, out purge))
                    {
                        return Responses.BadRequest("softDeleted must be true or false");
                    }
                }

                var tagQuery = context.GetTagQueryParameters();
                var excludeTagQuery = context.GetTagQueryParameters("excludeTag");

                var count = await manager.SoftDeleteBuffers(tagQuery, excludeTagQuery, ttl, purge, cancellationToken);
                return Results.Ok(count);
            })
            .WithName("deleteBuffers")
            .Produces<int>(StatusCodes.Status200OK)
            .Produces<ErrorBody>(StatusCodes.Status400BadRequest);

        buffers.MapPost("/restore", async (BufferManager manager, HttpContext context, CancellationToken cancellationToken) =>
            {
                var tagQuery = context.GetTagQueryParameters();
                var excludeTagQuery = context.GetTagQueryParameters("excludeTag");

                var count = await manager.RestoreBuffers(tagQuery, excludeTagQuery, cancellationToken);
                return Results.Ok(count);
            })
            .WithName("restoreBuffers")
            .Produces<int>(StatusCodes.Status200OK);

        buffers.MapGet("/count", async (BufferManager manager, HttpContext context, CancellationToken cancellationToken) =>
            {
                var tagQuery = context.GetTagQueryParameters();
                var excludeTagQuery = context.GetTagQueryParameters("excludeTag");

                bool? softDeleted = null;
                if (context.Request.Query.TryGetValue("softDeleted", out var softDeletedQuery))
                {
                    if (bool.TryParse(softDeletedQuery, out bool softDeletedParsed))
                    {
                        softDeleted = softDeletedParsed;
                    }
                }

                var count = await manager.GetBufferCount(tagQuery, excludeTagQuery, softDeleted, cancellationToken);
                return Results.Ok(count);
            })
            .WithName("getBufferCount")
            .Produces<int>(StatusCodes.Status200OK);

        buffers.MapGet("/{id}", async (BufferManager manager, HttpContext context, string id, CancellationToken cancellationToken) =>
            {
                var softDeleted = false;
                if (context.Request.Query.TryGetValue("softDeleted", out var softDeletedQuery))
                {
                    if (!bool.TryParse(softDeletedQuery, out softDeleted))
                    {
                        return Responses.BadRequest("softDeleted must be true or false");
                    }
                }

                var buffer = await manager.GetBufferById(id, softDeleted, cancellationToken);
                if (buffer != null)
                {
                    context.Response.Headers.ETag = buffer.ETag;
                    return Results.Ok(buffer);
                }

                return Responses.NotFound();
            })
            .WithName("getBufferById")
            .MapToApiVersion(ApiVersions.V1p0)
            .Produces<Buffer>(StatusCodes.Status200OK)
            .Produces<ErrorBody>(StatusCodes.Status400BadRequest)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound);

        buffers.MapGet("/{id}", async (BufferManager manager, HttpContext context, string id, bool softDeleted, CancellationToken cancellationToken) =>
            {
                var buffer = await manager.GetBufferById(id, softDeleted, cancellationToken);
                if (buffer != null)
                {
                    context.Response.Headers.ETag = buffer.ETag;
                    return Results.Ok(buffer);
                }

                return Responses.NotFound();
            })
            .WithName("getBufferById_deprecated")
            .MapToApiVersion(ApiVersions.V0p8)
            .MapToApiVersion(ApiVersions.V0p9)
            .Produces<Buffer>(StatusCodes.Status200OK)
            .Produces<ErrorBody>(StatusCodes.Status400BadRequest)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound);

        buffers.MapGet("/{id}/hello", async (BufferManager manager, HttpContext context, string id, CancellationToken cancellationToken) =>
            {
                return await Task.Run(() =>
                {
                    return Results.Ok("hello");
                }, context.RequestAborted);
            })
            .WithName("getBufferHello")
            .MapToApiVersion(ApiVersions.V0p9)
            .Produces<Buffer>(StatusCodes.Status200OK);

        buffers.MapPut("/{id}", async (BufferManager manager, HttpContext context, string id, CancellationToken cancellationToken) =>
            {
                var bufferUpdate = await context.Request.ReadAndValidateJson<BufferUpdate>(context.RequestAborted, allowEmpty: true);

                if (!string.IsNullOrEmpty(bufferUpdate.Id) && !string.Equals(id, bufferUpdate.Id, StringComparison.Ordinal))
                {
                    return Responses.BadRequest("The buffer ID in the URL does not match the buffer ID in the request body.");
                }

                string eTagPrecondition = context.Request.Headers.IfMatch.FirstOrDefault() ?? "";
                if (eTagPrecondition == "*") // if-match: * matches everything
                {
                    eTagPrecondition = "";
                }

                var softDeleted = false;
                if (context.Request.Query.TryGetValue("softDeleted", out var softDeletedQuery))
                {
                    if (!bool.TryParse(softDeletedQuery, out softDeleted))
                    {
                        return Responses.BadRequest("softDeleted must be true or false");
                    }
                }

                bufferUpdate = bufferUpdate with { Id = id };

                var result = await manager.UpdateBuffer(bufferUpdate, eTagPrecondition, softDeleted, cancellationToken);

                return result.Match(
                    updated: updated =>
                    {
                        context.Response.Headers.ETag = updated.Value.ETag;
                        return Results.Ok(updated.Value);
                    },
                    notFound: _ => Responses.NotFound(),
                    preconditionFailed: failed => Responses.PreconditionFailed(failed.Reason));
            })
            .WithName("setBufferTags")
            .Accepts<BufferUpdate>("application/json")
            .Produces<Buffer>(StatusCodes.Status200OK)
            .Produces<ErrorBody>(StatusCodes.Status400BadRequest)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound)
            .Produces<ErrorBody>(StatusCodes.Status412PreconditionFailed);

        buffers.MapDelete("/{id}", async (BufferManager manager, HttpContext context, string id, CancellationToken cancellationToken) =>
            {
                if (!context.ParseAndValidateTtlQueryParameter(out var ttl))
                {
                    return Responses.BadRequest("ttl must be a valid, non-negative TimeSpan");
                }

                var purge = false;
                if (context.Request.Query.TryGetValue("softDeleted", out var softDeletedQuery))
                {
                    if (!bool.TryParse(softDeletedQuery, out purge))
                    {
                        return Responses.BadRequest("softDeleted must be true or false");
                    }
                }

                var result = await manager.SoftDeleteBufferById(id, ttl, purge, cancellationToken);
                return result.Match(
                    updated: updated => Results.Ok(updated.Value),
                    notFound: _ => Responses.NotFound(),
                    preconditionFailed: failed => Responses.PreconditionFailed(failed.Reason));
            })
            .WithName("deleteBuffer")
            .Produces<Buffer>(StatusCodes.Status200OK)
            .Produces<ErrorBody>(StatusCodes.Status400BadRequest)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound)
            .Produces<ErrorBody>(StatusCodes.Status412PreconditionFailed);

        buffers.MapPost("/{id}/restore", async (BufferManager manager, HttpContext context, string id, CancellationToken cancellationToken) =>
            {
                var result = await manager.RestoreBufferById(id, cancellationToken);
                return result.Match(
                    updated: updated => Results.Ok(updated.Value),
                    notFound: _ => Responses.NotFound(),
                    preconditionFailed: failed => Responses.PreconditionFailed(failed.Reason));
            })
            .WithName("restoreBuffer")
            .Produces<int>(StatusCodes.Status200OK)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound)
            .Produces<ErrorBody>(StatusCodes.Status412PreconditionFailed);

        buffers.MapPost("/{id}/access", async (BufferManager manager, string id, bool? writeable, bool? preferTcp, bool? fromDocker, CancellationToken cancellationToken) =>
            {
                var bufferAccess = await manager.CreateBufferAccessUrls([(id, writeable == true)], preferTcp == true, fromDocker == true, checkExists: true, cancellationToken);
                if (bufferAccess is [(_, _, null)])
                {
                    return Responses.NotFound();
                }

                return Results.Json(bufferAccess[0].bufferAccess, statusCode: StatusCodes.Status201Created);
            })
            .WithName("getBufferAccessString")
            .Produces<BufferAccess>(StatusCodes.Status201Created)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound);

        buffers.MapGet("/storage-accounts", (BufferManager manager, CancellationToken cancellationToken) =>
            {
                return Results.Ok(manager.GetStorageAccounts());
            })
            .WithName("getStorageAccounts")
            .Produces<IList<StorageAccount>>(StatusCodes.Status200OK);

        buffers.MapPost("/export", async (HttpContext context, BufferManager manager, CancellationToken cancellationToken) =>
            {
                var exportRequest = await context.Request.ReadAndValidateJson<ExportBuffersRequest>(context.RequestAborted);
                var run = await manager.ExportBuffers(exportRequest, cancellationToken);
                return Results.Json(run, statusCode: StatusCodes.Status201Created);
            })
            .WithName("exportBuffers")
            .Accepts<ExportBuffersRequest>("application/json")
            .Produces<Run>(StatusCodes.Status202Accepted);

        buffers.MapPost("/import", async (HttpContext context, BufferManager manager, CancellationToken cancellationToken) =>
            {
                var importRequest = await context.Request.ReadAndValidateJson<ImportBuffersRequest>(context.RequestAborted);
                var run = await manager.ImportBuffers(importRequest, cancellationToken);
                return Results.Json(run, statusCode: StatusCodes.Status201Created);
            })
            .WithName("importBuffers")
            .Accepts<ImportBuffersRequest>("application/json")
            .Produces<Run>(StatusCodes.Status202Accepted);
    }
}

public class BufferOptions
{
    [Required]
    public string BufferSidecarImage { get; set; } = null!;

    public string BufferCopierImage { get; set; } = null!;

    public string PrimarySigningPrivateKeyPath { get; init; } = null!;
    public string SecondarySigningPrivateKeyPath { get; init; } = null!;

    public TimeSpan ActiveLifetime { get; init; } = TimeSpan.Zero;
    public TimeSpan SoftDeletedLifetime { get; init; } = TimeSpan.Zero;
}

public class CloudBufferStorageOptions
{
    [Required]
    public string DefaultLocation { get; init; } = null!;

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
