using System.ComponentModel.DataAnnotations;
using System.Security.Cryptography;
using System.Text.Json;
using System.Text.Json.Serialization;
using Microsoft.AspNetCore.Mvc;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;
using Tyger.Common.Buffers;

namespace Tyger.DataPlane;

#pragma warning disable CA5351 // Do Not Use Broken Cryptographic Algorithms. We are using MD5 for compatibility with Azure Blob Storage

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
    public string PrimarySigningCertificatePath { get; init; } = null!;

    public string? SecondarySigningCertificatePath { get; init; }
}

public class DataPlaneStorageHandler : IHealthCheck
{
    private const string ErrorCodeHeaderName = "x-ms-error-code";
    private const string ContentMd5Header = "Content-MD5";
    private const string CustomHeaderPrefix = "x-ms-meta-";
    private readonly StorageOptions _options;
    private readonly string _dataDir;
    private readonly string _metadataDir;
    private readonly string _stagingDir;

    private readonly ValidateSignatureFunc _validateSignature;

    public DataPlaneStorageHandler(IOptions<StorageOptions> bufferOptions)
    {
        _options = bufferOptions.Value;
        _dataDir = Path.Combine(_options.DataDirectory, "data");
        _metadataDir = Path.Combine(_options.DataDirectory, "metadata");
        _stagingDir = Path.Combine(_options.DataDirectory, "staging");

        Directory.CreateDirectory(_dataDir);
        Directory.CreateDirectory(_metadataDir);
        Directory.CreateDirectory(_stagingDir);

        _validateSignature = DigitalSignature.CreateValidationFunc(bufferOptions.Value.PrimarySigningCertificatePath, bufferOptions.Value.SecondarySigningCertificatePath);
    }

    internal void HandleHeadContainer(string containerId, HttpContext context)
    {
        switch (LocalSasHandler.ValidateRequest(containerId, SasResourceType.Container, SasAction.Read, context.Request.Query, _validateSignature))
        {
            case SasValidationResult.InvalidSas:
                context.Response.Headers[ErrorCodeHeaderName] = "AuthenticationFailed";
                context.Response.StatusCode = StatusCodes.Status403Forbidden;
                return;
            case SasValidationResult.ActionNotAllowed:
                context.Response.Headers[ErrorCodeHeaderName] = "AuthorizationPermissionMismatch";
                context.Response.StatusCode = StatusCodes.Status403Forbidden;
                return;
        }

        if (!Directory.Exists(Path.Combine(_dataDir, containerId)))
        {
            context.Response.Headers["x-ms-error-code"] = "ContainerNotFound";
            context.Response.StatusCode = StatusCodes.Status404NotFound;
            return;
        }
    }

    public void HandlePutContainer(string containerId, HttpContext context)
    {
        switch (LocalSasHandler.ValidateRequest(containerId, SasResourceType.Container, SasAction.Create, context.Request.Query, _validateSignature))
        {
            case SasValidationResult.InvalidSas:
                context.Response.Headers[ErrorCodeHeaderName] = "AuthenticationFailed";
                context.Response.StatusCode = StatusCodes.Status403Forbidden;
                return;
            case SasValidationResult.ActionNotAllowed:
                context.Response.Headers[ErrorCodeHeaderName] = "AuthorizationPermissionMismatch";
                context.Response.StatusCode = StatusCodes.Status403Forbidden;
                return;
        }

        Directory.CreateDirectory(Path.Combine(_metadataDir, containerId));
        Directory.CreateDirectory(Path.Combine(_dataDir, containerId));

        context.Response.StatusCode = StatusCodes.Status201Created;
    }

    public async Task HandlePutBlob(string containerId, string blobRelativePath, HttpContext context, CancellationToken cancellationToken)
    {
        switch (LocalSasHandler.ValidateRequest(containerId, SasResourceType.Blob, SasAction.Create, context.Request.Query, _validateSignature))
        {
            case SasValidationResult.InvalidSas:
                context.Response.Headers[ErrorCodeHeaderName] = "AuthenticationFailed";
                context.Response.StatusCode = StatusCodes.Status403Forbidden;
                return;
            case SasValidationResult.ActionNotAllowed:
                context.Response.Headers[ErrorCodeHeaderName] = "AuthorizationPermissionMismatch";
                context.Response.StatusCode = StatusCodes.Status403Forbidden;
                return;
        }

        if (!Directory.Exists(Path.Combine(_dataDir, containerId)))
        {
            context.Response.Headers["x-ms-error-code"] = "ContainerNotFound";
            context.Response.StatusCode = StatusCodes.Status404NotFound;
            return;
        }

        var tempGuid = Guid.NewGuid().ToString();

        var stagingDataPath = Path.Combine(_stagingDir, tempGuid);
        var stagingMetadataPath = Path.Combine(_stagingDir, tempGuid + ".metadata");

        try
        {
            var md5 = MD5.Create();

            await using (var dataFileStream = new FileStream(stagingDataPath, FileMode.CreateNew, FileAccess.Write, FileShare.None, 4096, FileOptions.Asynchronous | FileOptions.SequentialScan))
            using (var cryptoStream = new CryptoStream(dataFileStream, md5, CryptoStreamMode.Write))
            {
                await context.Request.Body.CopyToAsync(cryptoStream, cancellationToken);
            }

            var metadata = new BlobMetadata
            {
                ContentMD5 = Convert.ToBase64String(md5.Hash!),
            };

            if (context.Request.Headers.TryGetValue(ContentMd5Header, out var clientContentMD5) && metadata.ContentMD5 != clientContentMD5.ToString())
            {
                context.Response.Headers[ErrorCodeHeaderName] = "Md5Mismatch";
                context.Response.StatusCode = StatusCodes.Status400BadRequest;
                return;
            }

            foreach (var header in context.Request.Headers)
            {
                if (header.Key.StartsWith(CustomHeaderPrefix, StringComparison.OrdinalIgnoreCase))
                {
                    var key = header.Key[CustomHeaderPrefix.Length..];
                    (metadata.CustomMetadata ??= new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase))[key] = header.Value.ToString();
                }
            }

            await using (var metadataFileStream = new FileStream(stagingMetadataPath, FileMode.CreateNew, FileAccess.Write, FileShare.None, 4096, FileOptions.Asynchronous))
            {
                await JsonSerializer.SerializeAsync(metadataFileStream, metadata, cancellationToken: cancellationToken);
            }

            var finalMetadataPath = Path.Combine(_metadataDir, containerId, blobRelativePath);
            Directory.CreateDirectory(Path.GetDirectoryName(finalMetadataPath)!);
            try
            {
                File.Move(stagingMetadataPath, finalMetadataPath, overwrite: false);
            }
            catch (IOException e)
            {
                if (e.HResult == 17) // file already exists
                {
                    context.Response.Headers["x-ms-error-code"] = "UnauthorizedBlobOverwrite";
                    context.Response.StatusCode = StatusCodes.Status403Forbidden;
                    return;
                }

                throw;
            }

            var finalDataPath = Path.Combine(_dataDir, containerId, blobRelativePath);
            Directory.CreateDirectory(Path.GetDirectoryName(finalDataPath)!);

            try
            {
                File.Move(stagingDataPath, finalDataPath, overwrite: false);
            }
            catch
            {
                try
                {
                    File.Delete(finalMetadataPath);
                }
                catch
                {
                }

                throw;
            }

            context.Response.StatusCode = StatusCodes.Status201Created;
        }
        finally
        {
            try
            {
                File.Delete(stagingMetadataPath);
                File.Delete(stagingDataPath);
            }
            catch
            {
            }
        }
    }

    public async Task HandleGetBlob(string containerId, string blobRelativePath, HttpContext context, CancellationToken cancellationToken)
    {
        switch (LocalSasHandler.ValidateRequest(containerId, SasResourceType.Blob, SasAction.Read, context.Request.Query, _validateSignature))
        {
            case SasValidationResult.InvalidSas:
                context.Response.Headers[ErrorCodeHeaderName] = "AuthenticationFailed";
                context.Response.StatusCode = StatusCodes.Status403Forbidden;
                return;
            case SasValidationResult.ActionNotAllowed:
                context.Response.Headers[ErrorCodeHeaderName] = "AuthorizationPermissionMismatch";
                context.Response.StatusCode = StatusCodes.Status403Forbidden;
                return;
        }

        if (!Directory.Exists(Path.Combine(_dataDir, containerId)))
        {
            context.Response.Headers["x-ms-error-code"] = "ContainerNotFound";
            context.Response.StatusCode = StatusCodes.Status404NotFound;
            return;
        }

        var dataPath = Path.Combine(_dataDir, containerId, blobRelativePath);
        var dataFileInfo = new FileInfo(dataPath);
        if (!dataFileInfo.Exists)
        {
            context.Response.Headers[ErrorCodeHeaderName] = "BlobNotFound";
            context.Response.StatusCode = StatusCodes.Status404NotFound;
            return;
        }

        var metadataPath = Path.Combine(_metadataDir, containerId, blobRelativePath);

        await using var metadataFileStream = new FileStream(metadataPath, FileMode.Open, FileAccess.Read, FileShare.Read, 4096, FileOptions.Asynchronous);
        var metadata = await JsonSerializer.DeserializeAsync<BlobMetadata>(metadataFileStream, cancellationToken: cancellationToken);
        context.Response.Headers[ContentMd5Header] = metadata.ContentMD5;
        if (metadata.CustomMetadata != null)
        {
            foreach (var (key, value) in metadata.CustomMetadata)
            {
                context.Response.Headers[CustomHeaderPrefix + key] = value;
            }
        }

        context.Response.Headers.ContentLength = dataFileInfo.Length;

        if (context.Request.Method == HttpMethods.Head)
        {
            return;
        }

        await using var dataFileStream = new FileStream(dataPath, FileMode.Open, FileAccess.Read, FileShare.Read, 4096, FileOptions.Asynchronous | FileOptions.SequentialScan);
        await dataFileStream.CopyToAsync(context.Response.Body, cancellationToken);
    }

    public Task<HealthCheckResult> CheckHealthAsync(HealthCheckContext context, CancellationToken cancellationToken = default)
    {
        return Task.FromResult(HealthCheckResult.Healthy());
    }
}

internal struct BlobMetadata
{
    [JsonPropertyName("contentMD5")]
    public string ContentMD5 { get; set; }

    [JsonPropertyName("customMetadata")]
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingNull)]
    public Dictionary<string, string> CustomMetadata { get; set; }
}
