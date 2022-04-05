using Azure;
using Azure.Storage.Blobs;
using Azure.Storage.Sas;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;
using Tyger.Server.Model;
using Buffer = Tyger.Server.Model.Buffer;

namespace Tyger.Server.Buffers;

public class BufferManager : IHealthCheck
{
    private readonly BlobStorageOptions _config;
    private readonly ILogger<BufferManager> _logger;
    private readonly BlobServiceClient _serviceClient;

    public BufferManager(IOptions<BlobStorageOptions> config, ILogger<BufferManager> logger)
    {
        _config = config.Value;
        _logger = logger;
        _serviceClient = new BlobServiceClient(_config.ConnectionString);
    }

    public async Task<Buffer> CreateBuffer(CancellationToken cancellationToken)
    {
        string id = UniqueId.Create();
        _logger.CreatingBuffer(id);
        _ = await _serviceClient.CreateBlobContainerAsync(id, cancellationToken: cancellationToken);
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

    internal async Task<BufferAccess?> CreateBufferAccessString(string id, bool writeable, CancellationToken cancellationToken)
    {
        if (await GetBufferById(id, cancellationToken) is null)
        {
            return null;
        }

        var permissions = BlobContainerSasPermissions.Read;
        if (writeable)
        {
            permissions |= BlobContainerSasPermissions.Write;
        }

        var containerClient = _serviceClient.GetBlobContainerClient(id);

        // Create a SAS token that's valid for one hour.
        BlobSasBuilder sasBuilder = new()
        {
            BlobContainerName = containerClient.Name,
            Resource = "c"
        };

        sasBuilder.ExpiresOn = DateTimeOffset.UtcNow.AddHours(1);
        sasBuilder.SetPermissions(permissions);

        Uri uri = containerClient.GenerateSasUri(sasBuilder);
        return new BufferAccess(uri);
    }

    public async Task<HealthCheckResult> CheckHealthAsync(HealthCheckContext context, CancellationToken cancellationToken)
    {
        await _serviceClient.GetPropertiesAsync(cancellationToken);
        return HealthCheckResult.Healthy();
    }
}
