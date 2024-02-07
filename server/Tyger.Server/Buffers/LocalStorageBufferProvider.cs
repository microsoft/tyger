
using System.Globalization;
using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using Microsoft.AspNetCore.Http.Extensions;
using Microsoft.Extensions.Options;
using Tyger.Server.ServiceMetadata;

namespace Tyger.Server.Buffers;

#pragma warning disable CA5351 // Do Not Use Broken Cryptographic Algorithms. We are using MD5 for compatibility with Azure Blob Storage

public class LocalStorageBufferProvider : IBufferProvider, IHostedService
{
    private const string ErrorCodeHeaderName = "x-ms-error-code";
    private const string ContentMd5Header = "Content-MD5";
    private const string CustomHeaderPrefix = "x-ms-meta-";
    private readonly LocalSasHandler _sasHandler;
    private readonly BufferOptions _options;
    private readonly string _dataDir;
    private readonly string _metadataDir;
    private readonly string _stagingDir;
    private readonly Uri _baseUrl;

    public LocalStorageBufferProvider(LocalSasHandler sasHandler, IOptions<BufferOptions> bufferOptions, IOptions<ServiceMetadataOptions> serviceMetadataOptions)
    {
        _sasHandler = sasHandler;
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
        var builder = new UriBuilder(new Uri(_baseUrl, id)) { Query = _sasHandler.GetSasQueryString(id, writeable).ToString() };
        return Task.FromResult(builder.Uri);
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
        switch (_sasHandler.ValidateRequest(bufferId, true, context.Request.Query))
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
        switch (_sasHandler.ValidateRequest(bufferId, false, context.Request.Query))
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

public sealed class LocalSasHandler
{
    private const string CurrentSasVersion = "0.1.0";
    public const string SasTimeFormat = "yyyy-MM-ddTHH:mm:ssZ";
    private readonly Func<byte[], byte[]> _signData;
    private readonly Func<byte[], byte[], bool> _validateSignature;

    public LocalSasHandler(IOptions<BufferOptions> options)
    {
        static (Func<byte[], byte[]> sign, Func<byte[], byte[], bool> validate) GetHandlers(X509Certificate2 cert)
        {
            if (cert.GetECDsaPrivateKey() is { } ecdsaKey)
            {
                return ((data) => ecdsaKey.SignData(data, HashAlgorithmName.SHA256), (data, signature) => ecdsaKey.VerifyData(data, signature, HashAlgorithmName.SHA256));
            }
            else if (cert.GetRSAPrivateKey() is { } rsaKey)
            {
                return ((data) => rsaKey.SignData(data, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1), (data, signature) => rsaKey.VerifyData(data, signature, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1));
            }
            else
            {
                throw new InvalidOperationException("No valid private key found for certificate with thumbprint " + cert.Thumbprint);
            }
        }

        (_signData, _validateSignature) = GetHandlers(options.Value.LocalStorage.PrimarySigningCertificate);

        if (options.Value.LocalStorage.SecondarySigningCertificate is { } secondaryCert)
        {
            var (_, secondaryValidateSignature) = GetHandlers(secondaryCert);
            var primaryValidateSignature = _validateSignature;
            _validateSignature = (data, signature) => primaryValidateSignature(data, signature) || secondaryValidateSignature(data, signature);
        }
    }

    public QueryString GetSasQueryString(string bufferId, bool writeable)
    {
        var startTime = DateTimeOffset.UtcNow;
        var startTimeString = FormatTimeForSasSigning(startTime);
        var endTime = startTime.AddHours(1);
        var endTimeString = FormatTimeForSasSigning(endTime);

        string permissions = writeable ? "rc" : "r";

        var stringToSign = string.Join("\n",
            CurrentSasVersion,
            bufferId,
            permissions,
            FormatTimeForSasSigning(startTime),
            FormatTimeForSasSigning(endTime));

        var signature = Convert.ToBase64String(_signData(Encoding.UTF8.GetBytes(stringToSign)));

        var queryBuilder = new QueryBuilder {
            { "sv", CurrentSasVersion },
            { "sp", permissions },
            { "st", FormatTimeForSasSigning(startTime) },
            { "se", FormatTimeForSasSigning(endTime) },
            { "sig", signature }
        };

        return queryBuilder.ToQueryString();
    }

    internal static string FormatTimeForSasSigning(DateTimeOffset time) =>
        (time == default) ? "" : time.ToString(SasTimeFormat, CultureInfo.InvariantCulture);

    internal SasValidationResult ValidateRequest(string bufferId, bool write, IQueryCollection query)
    {
        if (!query.TryGetValue("sv", out var sv) || sv != CurrentSasVersion)
        {
            return SasValidationResult.InvalidSas;
        }

        if (!query.TryGetValue("sp", out var sp))
        {
            return SasValidationResult.InvalidSas;
        }

        if (!query.TryGetValue("st", out var st) || !DateTimeOffset.TryParseExact(st, SasTimeFormat, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var startTime))
        {
            return SasValidationResult.InvalidSas;
        }

        if (!query.TryGetValue("se", out var se) || !DateTimeOffset.TryParseExact(se, SasTimeFormat, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var endTime))
        {
            return SasValidationResult.InvalidSas;
        }

        if (!query.TryGetValue("sig", out var sig))
        {
            return SasValidationResult.InvalidSas;
        }

        if (endTime < DateTimeOffset.UtcNow)
        {
            return SasValidationResult.InvalidSas;
        }

        var stringToSign = string.Join("\n",
            CurrentSasVersion,
            bufferId,
            sp,
            st,
            se);

        if (!_validateSignature(Encoding.UTF8.GetBytes(stringToSign), Convert.FromBase64String(sig.ToString())))
        {
            return SasValidationResult.InvalidSas;
        }

        if (write)
        {
            if (!sp.ToString().Contains('c'))
            {
                return SasValidationResult.ActionNotAllowed;
            }
        }
        else
        {
            if (!sp.ToString().Contains('r'))
            {
                return SasValidationResult.ActionNotAllowed;
            }
        }

        return SasValidationResult.ActionAllowed;
    }
}

public enum SasValidationResult
{
    InvalidSas,
    ActionAllowed,
    ActionNotAllowed
}
