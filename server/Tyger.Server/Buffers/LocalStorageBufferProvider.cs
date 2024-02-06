
using System.Security.Cryptography;
using System.Text.Json;
using System.Text.Json.Serialization;
using Microsoft.Extensions.Options;
using Tyger.Server.ServiceMetadata;

namespace Tyger.Server.Buffers;

#pragma warning disable CA5351 // Do Not Use Broken Cryptographic Algorithms. We are using MD5 for compatibility with Azure Blob Storage

public class LocalStorageBufferProvider : IBufferProvider, IHostedService
{
    private const string ErrorCodeHeaderName = "x-ms-error-code";
    private const string ContentMd5Header = "Content-MD5";
    private const string CustomHeaderPrefix = "x-ms-meta-";
    private readonly BufferOptions _options;
    private readonly string _dataDir;
    private readonly string _metadataDir;
    private readonly string _stagingDir;
    private readonly Uri _baseUrl;

    public LocalStorageBufferProvider(IOptions<BufferOptions> bufferOptions, IOptions<ServiceMetadataOptions> serviceMetadataOptions)
    {
        _options = bufferOptions.Value;
        _dataDir = Path.Combine(_options.LocalStorage.DataDirectory, "data");
        _metadataDir = Path.Combine(_options.LocalStorage.DataDirectory, "metadata");
        _stagingDir = Path.Combine(_options.LocalStorage.DataDirectory, "staging");

        var baseUrl = serviceMetadataOptions.Value.ExternalBaseUrl.ToString();
        if (!baseUrl.EndsWith('/'))
        {
            baseUrl += '/';
        }

        baseUrl += "v1/buffers/data/";
        _baseUrl = new Uri(baseUrl);
    }

    public Task<bool> BufferExists(string id, CancellationToken cancellationToken)
    {
        return Task.FromResult(Directory.Exists(Path.Combine(_dataDir, id)));
    }

    public Task CreateBuffer(string id, CancellationToken cancellationToken)
    {
        Directory.CreateDirectory(Path.Combine(_metadataDir, id));
        Directory.CreateDirectory(Path.Combine(_dataDir, id));
        return Task.CompletedTask;
    }

    public Task<Uri> CreateBufferAccessUrl(string id, bool writeable, CancellationToken cancellationToken)
    {
        return Task.FromResult(new Uri(_baseUrl, id));
    }

    public Task StartAsync(CancellationToken cancellationToken)
    {
        if (_options.LocalStorage.Enabled)
        {
            Directory.CreateDirectory(_dataDir);
            Directory.CreateDirectory(_metadataDir);
            Directory.CreateDirectory(_stagingDir);
        }

        return Task.CompletedTask;
    }

    public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;

    public async Task HandlePutBlob(string bufferId, string blobRelativePath, HttpContext context, CancellationToken cancellationToken)
    {
        if (!Directory.Exists(Path.Combine(_dataDir, bufferId)))
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

            var finalMetadataPath = Path.Combine(_metadataDir, bufferId, blobRelativePath);
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

            var finalDataPath = Path.Combine(_dataDir, bufferId, blobRelativePath);
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

    public async Task HandleGetBlob(string bufferId, string blobRelativePath, HttpContext context, CancellationToken cancellationToken)
    {
        var dataPath = Path.Combine(_dataDir, bufferId, blobRelativePath);
        var dataFileInfo = new FileInfo(dataPath);
        if (!dataFileInfo.Exists)
        {
            context.Response.Headers[ErrorCodeHeaderName] = "BlobNotFound";
            context.Response.StatusCode = StatusCodes.Status404NotFound;
            return;
        }

        var metadataPath = Path.Combine(_metadataDir, bufferId, blobRelativePath);

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

        await using var dataFileStream = new FileStream(dataPath, FileMode.Open, FileAccess.Read, FileShare.Read, 4096, FileOptions.Asynchronous | FileOptions.SequentialScan);
        await dataFileStream.CopyToAsync(context.Response.Body, cancellationToken);
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
