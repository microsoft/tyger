using Azure;
using Azure.Storage.Blobs;
using Azure.Storage.Sas;
using Tyger.Server.Model;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;
using Buffer = Tyger.Server.Model.Buffer;

namespace Tyger.Server.Buffers;

public class BufferManager : IHealthCheck
{
    private readonly BlobStorageOptions _config;
    private readonly ILogger<BufferManager> _logger;
    private BlobServiceClient _serviceClient;
    private BlobServiceClient? _externalSasServiceClient;

    public BufferManager(IOptions<BlobStorageOptions> config, ILogger<BufferManager> logger)
    {
        _config = config.Value;
        _logger = logger;

        if (_config.AccountEndpoint.StartsWith("http://", StringComparison.Ordinal))
        {
            // assume this refers to the local emulator
            _serviceClient = new BlobServiceClient(CreateEmulatorConnectionString(_config.AccountEndpoint));
            if (!string.IsNullOrEmpty(_config.EmulatorExternalEndpoint))
            {
                _externalSasServiceClient = new BlobServiceClient(CreateEmulatorConnectionString(_config.EmulatorExternalEndpoint));
            }

            return;
        }

        _serviceClient = new BlobServiceClient(_config.AccountEndpoint);
    }

    private static string CreateEmulatorConnectionString(string endpoint)
    {
        return $"DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint={endpoint}";
    }

    public async Task<Buffer> CreateBuffer(CancellationToken cancellationToken)
    {
        string id = UniqueId.Create();
        _logger.CreatingBuffer(id);
        var result = await _serviceClient.CreateBlobContainerAsync(id, cancellationToken: cancellationToken);
        return new Buffer(id);
    }

    public async Task<Buffer?> GetBufferById(string id, CancellationToken cancellationToken)
    {
        var containerClient = _serviceClient.GetBlobContainerClient(id);
        try
        {
            if (await containerClient.ExistsAsync(cancellationToken))
            {
                return new Buffer(id);
            }
        }
        catch (RequestFailedException e) when (e.ErrorCode == "InvalidResourceName")
        {
        }

        return null;
    }

    internal async Task<BufferAccess?> CreateBufferAccessString(string id, bool writeable, bool external, CancellationToken cancellationToken)
    {
        if (await GetBufferById(id, cancellationToken) is null)
        {
            return null;
        }

        var permissions = BlobContainerSasPermissions.Read | BlobContainerSasPermissions.Write;
        var containerClient = (_externalSasServiceClient ?? _serviceClient).GetBlobContainerClient(id);

        // Create a SAS token that's valid for one hour.
        BlobSasBuilder sasBuilder = new BlobSasBuilder()
        {
            BlobContainerName = containerClient.Name,
            Resource = "c"
        };

        sasBuilder.ExpiresOn = DateTimeOffset.UtcNow.AddHours(1);
        sasBuilder.SetPermissions(permissions);

        Uri uri = containerClient.GenerateSasUri(sasBuilder);

        if (!external && _externalSasServiceClient != null)
        {
            uri = new UriBuilder(_serviceClient.Uri)
            {
                Path = uri.AbsolutePath,
                Query = uri.Query
            }.Uri;
        }

        return new BufferAccess(uri);
    }

    public async Task<HealthCheckResult> CheckHealthAsync(HealthCheckContext context, CancellationToken cancellationToken)
    {
        await _serviceClient.GetPropertiesAsync(cancellationToken);
        return HealthCheckResult.Healthy();
    }
}
