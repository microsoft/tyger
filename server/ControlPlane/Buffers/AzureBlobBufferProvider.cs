// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Azure;
using Azure.Core;
using Azure.Storage.Blobs;
using Azure.Storage.Blobs.Models;
using Azure.Storage.Sas;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;

namespace Tyger.ControlPlane.Buffers;

public sealed class AzureBlobBufferProvider : BackgroundService, IBufferProvider, IHealthCheck, IDisposable
{
    private static readonly TimeSpan s_userDelegationKeyDuration = TimeSpan.FromDays(1);

    private readonly BlobServiceClient _serviceClient;
    private readonly BufferOptions _bufferOptions;
    private readonly DatabaseOptions _databaseOptions;
    private readonly Lazy<IRunCreator> _runCreator;
    private readonly ILogger<BufferManager> _logger;
    private UserDelegationKey? _userDelegationKey;

    public AzureBlobBufferProvider(
        TokenCredential credential,
        IOptions<CloudBufferStorageOptions> storageOptions,
        IOptions<BufferOptions> bufferOptions,
        IOptions<DatabaseOptions> databaseOptions,
        Lazy<IRunCreator> runCreator,
        ILogger<BufferManager> logger)
    {
        _runCreator = runCreator;
        _logger = logger;
        var bufferStorageAccountOptions = storageOptions.Value.StorageAccounts[0];
        _serviceClient = new BlobServiceClient(new Uri(bufferStorageAccountOptions.Endpoint), credential);
        _bufferOptions = bufferOptions.Value;
        _databaseOptions = databaseOptions.Value;
    }

    public async Task CreateBuffer(string id, CancellationToken cancellationToken)
    {
        await _serviceClient.CreateBlobContainerAsync(id, cancellationToken: cancellationToken);
    }

    public Uri CreateBufferAccessUrl(string id, bool writeable, bool preferTcp)
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

        return uriBuilder.ToUri();
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

    public async Task<Run> ExportBuffers(ExportBuffersRequest exportBufferRequest, CancellationToken cancellationToken)
    {
        if (string.IsNullOrEmpty(exportBufferRequest.DestinationStorageEndpoint))
        {
            throw new ValidationException("Destination storage endpoint is required.");
        }

        var args = new List<string>
        {
            "export",
            "--log-format", "json",
            "--source-storage-endpoint", _serviceClient.Uri.ToString(),
            "--destination-storage-endpoint", exportBufferRequest.DestinationStorageEndpoint.ToString(),
            "--db-host", _databaseOptions.Host,
            "--db-user", _databaseOptions.Username,
        };

        if (!string.IsNullOrEmpty(_databaseOptions.DatabaseName))
        {
            args.Add("--db-name");
            args.Add(_databaseOptions.DatabaseName);
        }

        if (_databaseOptions.Port.HasValue)
        {
            args.Add("--db-port");
            args.Add(_databaseOptions.Port.Value.ToString());
        }

        if (exportBufferRequest.Filters != null)
        {
            foreach (var filter in exportBufferRequest.Filters)
            {
                args.Add("--filter");
                args.Add(string.Concat(filter.Key, "=", filter.Value));
            }
        }

        if (exportBufferRequest.HashIds)
        {
            args.Add("--hash-ids");
        }

        var newRun = new Run
        {
            Kind = RunKind.System,
            Job = new JobRunCodeTarget
            {
                Codespec = new JobCodespec
                {
                    Image = _bufferOptions.BufferCopierImage,
                    Identity = _databaseOptions.TygerServerIdentity,
                    Args = args,
                },
            },

            TimeoutSeconds = (int)TimeSpan.FromDays(7).TotalSeconds,
        };

        return await _runCreator.Value.CreateRun(newRun, null, cancellationToken);
    }

    public async Task<Run> ImportBuffers(CancellationToken cancellationToken)
    {
        var args = new List<string>
        {
            "import",
            "--log-format", "json",
            "--storage-endpoint", _serviceClient.Uri.ToString(),
            "--db-host", _databaseOptions.Host,
            "--db-user", _databaseOptions.Username,
        };

        if (!string.IsNullOrEmpty(_databaseOptions.DatabaseName))
        {
            args.Add("--db-name");
            args.Add(_databaseOptions.DatabaseName);
        }

        if (_databaseOptions.Port.HasValue)
        {
            args.Add("--db-port");
            args.Add(_databaseOptions.Port.Value.ToString());
        }

        var newRun = new Run
        {
            Kind = RunKind.System,
            Job = new JobRunCodeTarget
            {
                Codespec = new JobCodespec
                {
                    Image = _bufferOptions.BufferCopierImage,
                    Identity = _databaseOptions.TygerServerIdentity,
                    Args = args,
                },
            },

            TimeoutSeconds = (int)TimeSpan.FromDays(7).TotalSeconds,
        };

        return await _runCreator.Value.CreateRun(newRun, null, cancellationToken);
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

    public override async Task StartAsync(CancellationToken cancellationToken)
    {
        await RefreshUserDelegationKey(cancellationToken);
        await base.StartAsync(cancellationToken);
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        try
        {
            while (!stoppingToken.IsCancellationRequested)
            {
                await Task.Delay(s_userDelegationKeyDuration * 0.75, stoppingToken);
                while (true)
                {
                    try
                    {
                        await RefreshUserDelegationKey(stoppingToken);
                        break;
                    }
                    catch (TaskCanceledException) when (stoppingToken.IsCancellationRequested)
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

                        await Task.Delay(TimeSpan.FromSeconds(30), stoppingToken);
                    }
                }
            }
        }
        catch (TaskCanceledException) when (stoppingToken.IsCancellationRequested)
        {
            return;
        }
    }

    private async Task RefreshUserDelegationKey(CancellationToken cancellationToken)
    {
        var start = DateTimeOffset.UtcNow.AddMinutes(-5);
        _userDelegationKey = await _serviceClient.GetUserDelegationKeyAsync(start, start.Add(s_userDelegationKeyDuration), cancellationToken);
    }
}
