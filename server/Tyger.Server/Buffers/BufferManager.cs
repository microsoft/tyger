// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Text.RegularExpressions;
using Azure;
using Azure.Core;
using Azure.Storage.Blobs;
using Azure.Storage.Blobs.Models;
using Azure.Storage.Sas;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;
using Tyger.Server.Database;
using Tyger.Server.Model;
using Buffer = Tyger.Server.Model.Buffer;

namespace Tyger.Server.Buffers;

public sealed class BufferManager : IHealthCheck, IHostedService, IDisposable
{
    private static readonly TimeSpan s_userDelegationKeyDuration = TimeSpan.FromDays(1);
    private readonly IRepository _repository;
    private readonly ILogger<BufferManager> _logger;
    private readonly BlobServiceClient _serviceClient;
    private readonly CancellationTokenSource _backgroundCancellationTokenSource = new();
    private UserDelegationKey? _userDelegationKey;

    public BufferManager(IRepository repository, TokenCredential credential, IOptions<BufferOptions> config, ILogger<BufferManager> logger)
    {
        _repository = repository;
        _logger = logger;
        var bufferStorageAccountOptions = config.Value.StorageAccounts[0];
        _serviceClient = new BlobServiceClient(new Uri(bufferStorageAccountOptions.Endpoint), credential);
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

        return new BufferAccess(uriBuilder.ToUri());
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
