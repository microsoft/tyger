// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Security.Cryptography.X509Certificates;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.WebUtilities;
using Microsoft.Extensions.Primitives;
using Tyger.Server.Json;
using Tyger.Server.Model;
using Buffer = Tyger.Server.Model.Buffer;

namespace Tyger.Server.Buffers;

public static class Buffers
{
    private const string ConfigSectionPath = "buffers";

    public static void AddBuffers(this IServiceCollection services, IConfigurationManager configuration)
    {
        services.AddOptions<BufferOptions>().BindConfiguration(ConfigSectionPath).ValidateDataAnnotations().ValidateOnStart().Validate(o =>
        {
            o.LocalStorage.PrimarySigningCertificate = X509Certificate2.CreateFromPemFile(o.LocalStorage.PrimarySigningCertificatePath);
            if (o.LocalStorage.SecondarySigningCertificatePath is not null)
            {
                o.LocalStorage.SecondarySigningCertificate = X509Certificate2.CreateFromPem(File.ReadAllText(o.LocalStorage.SecondarySigningCertificatePath));
            }

            return true;
        });

        services.AddSingleton<BufferManager>();
        var bufferOptions = configuration.GetSection(ConfigSectionPath).Get<BufferOptions>();

        if (bufferOptions?.LocalStorage.Enabled == true)
        {
            services.AddSingleton<LocalStorageBufferProvider>();
            services.AddSingleton<IBufferProvider>(sp => sp.GetRequiredService<LocalStorageBufferProvider>());
            services.AddSingleton<IHostedService>(sp => sp.GetRequiredService<LocalStorageBufferProvider>());

            services.AddSingleton<LocalSasHandler>();
        }
        else
        {
            services.AddSingleton<AzureBlobBufferProvider>();
            services.AddSingleton<IBufferProvider>(sp => sp.GetRequiredService<AzureBlobBufferProvider>());
            services.AddSingleton<IHostedService>(sp => sp.GetRequiredService<AzureBlobBufferProvider>());
            services.AddHealthChecks().AddCheck<AzureBlobBufferProvider>("buffers");
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

        app.MapPost("/v1/buffers/{id}/access", async (BufferManager manager, string id, bool? writeable, CancellationToken cancellationToken) =>
            {
                var bufferAccess = await manager.CreateBufferAccessUrl(id, writeable == true, cancellationToken);
                if (bufferAccess is null)
                {
                    return Responses.NotFound();
                }

                return Results.Json(bufferAccess, statusCode: StatusCodes.Status201Created);
            })
            .WithName("getBufferAccessString")
            .Produces<BufferAccess>(StatusCodes.Status201Created)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound);

        if (app.Services.GetService<LocalStorageBufferProvider>() is { } localProvider)
        {
            app.MapPut("v1/buffers/data/{id}/{**blobRelativePath}", async (string id, string blobRelativePath, HttpContext context, CancellationToken cancellationToken) =>
            {
                await localProvider.HandlePutBlob(id, blobRelativePath, context, cancellationToken);
            }).AllowAnonymous();

            app.MapGet("v1/buffers/data/{id}/{**blobRelativePath}", async (string id, string blobRelativePath, HttpContext context, CancellationToken cancellationToken) =>
            {
                await localProvider.HandleGetBlob(id, blobRelativePath, context, cancellationToken);
            }).AllowAnonymous();
        }
    }
}

public class BufferOptions : IValidatableObject
{
    [Required, MinLength(1)]
    public required BufferStorageAccountOptions[] StorageAccounts { get; init; }

    public LocalStorageOptions LocalStorage { get; } = new();

    [Required]
    public required string BufferSidecarImage { get; init; }

    public IEnumerable<ValidationResult> Validate(ValidationContext validationContext)
    {
        var results = new List<ValidationResult>();

        Validator.TryValidateObject(LocalStorage, new ValidationContext(LocalStorage), results, validateAllProperties: true);

        return results;
    }
}

public class LocalStorageOptions
{
    public bool Enabled { get; init; }

    [Required, MinLength(1)]
    public string DataDirectory { get; init; } = null!;

    [Required, MinLength(1)]
    public string PrimarySigningCertificatePath { get; init; } = null!;

    public X509Certificate2 PrimarySigningCertificate { get; set; } = null!;

    public string? SecondarySigningCertificatePath { get; init; }

    public X509Certificate2? SecondarySigningCertificate { get; set; }
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
