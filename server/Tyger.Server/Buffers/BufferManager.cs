using System.ComponentModel.DataAnnotations;
using System.Text.RegularExpressions;
using Azure;
using Azure.Storage.Blobs;
using Azure.Storage.Sas;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;
using Tyger.Server.Database;
using Tyger.Server.Model;
using Buffer = Tyger.Server.Model.Buffer;

namespace Tyger.Server.Buffers;

public class BufferManager : IHealthCheck
{
    private readonly IRepository _repository;
    private readonly BufferOptions _config;
    private readonly ILogger<BufferManager> _logger;
    private readonly BlobServiceClient _serviceClient;

    public BufferManager(IRepository repository, IOptions<BufferOptions> config, ILogger<BufferManager> logger)
    {
        _repository = repository;
        _config = config.Value;
        _logger = logger;
        _serviceClient = new BlobServiceClient(_config.ConnectionString);
    }

    public async Task<Buffer> CreateBuffer(Buffer newBuffer, CancellationToken cancellationToken)
    {
        if (newBuffer.Tags != null)
        {
            string keyPattern = @"^[a-zA-Z0-9-_.]{1,128}$";
            string valuePattern = @"^[a-zA-Z0-9-_.]{0,256}$";

            foreach (var tag in newBuffer.Tags)
            {
                if (!Regex.IsMatch(tag.Key, keyPattern))
                {
                    throw new ValidationException("Tag keys must contain up to 128 letters (a-z, A-Z), numbers (0-9) and underscores (_)");
                }

                if (!Regex.IsMatch(tag.Value, valuePattern))
                {
                    throw new ValidationException("Tag values can contain up to 256 letters (a-z, A-Z), numbers (0-9) and underscores (_)");
                }
            }

            if (newBuffer.Tags.Count > 10)
            {
                throw new ValidationException("Only 10 tags can be set on a buffer");
            }
        }

        string id = UniqueId.Create();
        _logger.CreatingBuffer(id);
        _ = await _serviceClient.CreateBlobContainerAsync(id, cancellationToken: cancellationToken);
        return await _repository.CreateBuffer(newBuffer with { Id = id }, cancellationToken);
    }

    public async Task<Buffer?> GetBufferById(string id, CancellationToken cancellationToken)
    {
        return await GetBufferById(id, "", cancellationToken);
    }

    public async Task<Buffer?> GetBufferById(string id, string eTag, CancellationToken cancellationToken)
    {
        var buffer = await _repository.GetBuffer(id, eTag, cancellationToken);

        if (buffer == null)
        {
            return null;
        }

        var containerClient = _serviceClient.GetBlobContainerClient(id);
        try
        {
            if (await containerClient.ExistsAsync(cancellationToken))
            {
                return buffer;
            }
        }
        catch (RequestFailedException e) when (e.ErrorCode == "InvalidResourceName")
        {
            _logger.InvalidResourceName(id);
        }

        return null;
    }

    public async Task<Buffer?> UpdateBufferById(string id, string eTag, IDictionary<string, string>? tags, CancellationToken cancellationToken)
    {
        return await _repository.UpdateBufferById(id, eTag, tags, cancellationToken);
    }

    public async Task<(IList<Buffer>, string? nextContinuationToken)> GetBuffers(IDictionary<string, string>? tags, int limit, string? continuationToken, CancellationToken cancellationToken)
    {
        return await _repository.GetBuffers(tags, limit, continuationToken, cancellationToken);
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
            permissions |= BlobContainerSasPermissions.Create;
        }

        var containerClient = _serviceClient.GetBlobContainerClient(id);

        // Create a SAS token that's valid for one hour.
        BlobSasBuilder sasBuilder = new()
        {
            BlobContainerName = containerClient.Name,
            Resource = "c",
            ExpiresOn = DateTimeOffset.UtcNow.AddHours(1)
        };
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
