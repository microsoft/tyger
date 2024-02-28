using Azure;
using Azure.Core;
using Azure.Storage.Blobs;
using Azure.Storage.Blobs.Models;
using Azure.Storage.Sas;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;

namespace Tyger.ControlPlane.Buffers;

public sealed class AzureBlobBufferProvider : IBufferProvider, IHealthCheck, IHostedService, IDisposable
{
    private static readonly TimeSpan s_userDelegationKeyDuration = TimeSpan.FromDays(1);

    private readonly BlobServiceClient _serviceClient;
    private readonly ILogger<BufferManager> _logger;
    private readonly CancellationTokenSource _backgroundCancellationTokenSource = new();
    private UserDelegationKey? _userDelegationKey;

    public AzureBlobBufferProvider(TokenCredential credential, IOptions<CloudBufferStorageOptions> config, ILogger<BufferManager> logger)
    {
        _logger = logger;
        var bufferStorageAccountOptions = config.Value.StorageAccounts[0];
        _serviceClient = new BlobServiceClient(new Uri(bufferStorageAccountOptions.Endpoint), credential);
    }

    public async Task CreateBuffer(string id, CancellationToken cancellationToken)
    {
        await _serviceClient.CreateBlobContainerAsync(id, cancellationToken: cancellationToken);
    }

    public Task<Uri> CreateBufferAccessUrl(string id, bool writeable, CancellationToken cancellationToken)
    {
        var permissions = BlobContainerSasPermissions.Read;
        if (writeable)
        {
            permissions |= BlobContainerSasPermissions.Create;
        }

        var containerClient = _serviceClient.GetBlobContainerClient(id);

        // Create a SAS token that's valid for one hour.
        var start = DateTimeOffset.UtcNow;
        BlobSasBuilder sasBuilder = new()
        {
            BlobContainerName = containerClient.Name,
            Resource = "c",
            StartsOn = start,
            ExpiresOn = start.AddHours(1),
            Protocol = SasProtocol.Https
        };
        sasBuilder.SetPermissions(permissions);

        var uriBuilder = new BlobUriBuilder(containerClient.Uri)
        {
            Sas = sasBuilder.ToSasQueryParameters(_userDelegationKey, containerClient.AccountName)
        };

        return Task.FromResult(uriBuilder.ToUri());
    }

    public async Task<bool> BufferExists(string id, CancellationToken cancellationToken)
    {
        var containerClient = _serviceClient.GetBlobContainerClient(id);
        try
        {
            return await containerClient.ExistsAsync(cancellationToken);
        }
        catch (RequestFailedException e) when (e.ErrorCode == "InvalidResourceName")
        {
            _logger.InvalidResourceName(id);
            return false;
        }
    }

    public async Task<HealthCheckResult> CheckHealthAsync(HealthCheckContext context, CancellationToken cancellationToken)
    {
        await _serviceClient.GetBlobContainerClient("healthcheck").ExistsAsync(cancellationToken);
        if (_userDelegationKey is null || _userDelegationKey.SignedExpiresOn < DateTimeOffset.UtcNow)
        {
            return HealthCheckResult.Unhealthy("User delegation key is not valid");
        }

        return HealthCheckResult.Healthy();
    }

    async Task IHostedService.StartAsync(CancellationToken cancellationToken)
    {
        async Task BackgroundLoop(CancellationToken cancellationToken)
        {
            while (!cancellationToken.IsCancellationRequested)
            {
                await Task.Delay(s_userDelegationKeyDuration * 0.75, cancellationToken);
                while (true)
                {
                    try
                    {
                        await RefreshUserDelegationKey(cancellationToken);
                        break;
                    }
                    catch (TaskCanceledException) when (cancellationToken.IsCancellationRequested)
                    {
                        return;
                    }
                    catch (Exception e)
                    {
                        if (_userDelegationKey is not null && _userDelegationKey.SignedExpiresOn > DateTimeOffset.UtcNow)
                        {
                            _logger.FailedToRefreshExpiredUserDelegationKey(e);
                        }
                        else
                        {
                            _logger.FailedToRefreshUserDelegationKey(e);
                        }

                        await Task.Delay(TimeSpan.FromSeconds(30), cancellationToken);
                    }
                }
            }
        }

        await RefreshUserDelegationKey(cancellationToken);
        _ = BackgroundLoop(_backgroundCancellationTokenSource.Token);
    }

    Task IHostedService.StopAsync(CancellationToken cancellationToken)
    {
        _backgroundCancellationTokenSource.Cancel();
        return Task.CompletedTask;
    }

    private async Task RefreshUserDelegationKey(CancellationToken cancellationToken)
    {
        var start = DateTimeOffset.UtcNow.AddMinutes(-5);
        _userDelegationKey = await _serviceClient.GetUserDelegationKeyAsync(start, start.Add(s_userDelegationKeyDuration), cancellationToken);
    }

    public void Dispose()
    {
        _backgroundCancellationTokenSource.Dispose();
    }
}
