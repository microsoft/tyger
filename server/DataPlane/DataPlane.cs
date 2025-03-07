// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Text.Json.Serialization;
using Microsoft.AspNetCore.Mvc;

namespace Tyger.DataPlane;

public static class DataPlane
{
    public static void AddDataPlane(this WebApplicationBuilder builder)
    {
        builder.Services.AddOptions<StorageOptions>().BindConfiguration("").ValidateDataAnnotations().ValidateOnStart();
        builder.Services.AddSingleton<DataPlaneStorageHandler>();
        builder.Services.AddHealthChecks().AddCheck<DataPlaneStorageHandler>("DataPlaneStorageHandler");
    }

    public static void MapDataPlane(this WebApplication app)
    {
        app.MapMethods(
            "v1/containers/{containerId}",
            [HttpMethods.Head],
            (
                [FromServices] DataPlaneStorageHandler handler,
                string containerId,
                HttpContext context) =>
            {
                handler.HandleHeadContainer(containerId, context);
            });

        app.MapPut(
            "v1/containers/{containerId}",
            (
                [FromServices] DataPlaneStorageHandler handler,
                string containerId,
                HttpContext context) =>
            {
                handler.HandlePutContainer(containerId, context);
            });

        app.MapDelete(
            "v1/containers/{containerId}",
            (
                [FromServices] DataPlaneStorageHandler handler,
                string containerId,
                HttpContext context) =>
            {
                handler.HandleDeleteContainer(containerId, context);
            });

        app.MapPut(
            "v1/containers/{containerId}/{**blobRelativePath}",
            async (
                [FromServices] DataPlaneStorageHandler handler,
                string containerId,
                string blobRelativePath,
                HttpContext context,
                CancellationToken cancellationToken) =>
            {
                await handler.HandlePutBlob(containerId, blobRelativePath, context, cancellationToken);
            });

        app.MapMethods(
            "v1/containers/{containerId}/{**blobRelativePath}",
            [HttpMethods.Get, HttpMethods.Head],
            async (
                [FromServices] DataPlaneStorageHandler handler,
                string containerId,
                string blobRelativePath,
                HttpContext context,
                CancellationToken cancellationToken) =>
            {
                await handler.HandleGetBlob(containerId, blobRelativePath, context, cancellationToken);
            });
    }
}

public class StorageOptions
{
    [Required, MinLength(1)]
    public string DataDirectory { get; init; } = null!;

    [Required, MinLength(1)]
    public string PrimarySigningPublicKeyPath { get; init; } = null!;

    public string? SecondarySigningPublicKeyPath { get; init; }
}

internal struct BlobMetadata
{
    [JsonPropertyName("contentMD5")]
    public string ContentMD5 { get; set; }

    [JsonPropertyName("customMetadata")]
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    public Dictionary<string, string> CustomMetadata { get; set; }
}
