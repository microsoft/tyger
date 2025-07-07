// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Collections.Concurrent;
using System.Collections.Immutable;
using System.ComponentModel.DataAnnotations;
using System.Globalization;
using System.Net;
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
using Buffer = Tyger.ControlPlane.Model.Buffer;

namespace Tyger.ControlPlane.Buffers;

public sealed class AzureBlobBufferProvider : IHostedService, IBufferProvider, IHealthCheck
{
    public static readonly TimeSpan DefaultAccessTtl = TimeSpan.FromHours(1);
    private readonly BufferOptions _bufferOptions;
    private readonly DatabaseOptions _databaseOptions;
    private readonly TokenCredential _credential;
    private readonly CloudBufferStorageOptions _storageOptions;
    private readonly Lazy<IRunCreator> _runCreator;
    private readonly Repository _repository;
    private readonly ILoggerFactory _loggerFactory;
    private readonly ILogger<AzureBlobBufferProvider> _logger;
    private ImmutableDictionary<int, BlobServiceClientWithRefreshingCredentials>? _storageAccountClients;
    private ImmutableDictionary<string, RoundRobinCounter>? _roundRobinCounters;
    private string? _defaultLocation;

    public AzureBlobBufferProvider(
        TokenCredential credential,
        IOptions<CloudBufferStorageOptions> storageOptions,
        IOptions<BufferOptions> bufferOptions,
        IOptions<DatabaseOptions> databaseOptions,
        Lazy<IRunCreator> runCreator,
        Repository repository,
        ILoggerFactory loggerFactory,
        ILogger<AzureBlobBufferProvider> logger)
    {
        _credential = credential;
        _storageOptions = storageOptions.Value;
        _runCreator = runCreator;
        _bufferOptions = bufferOptions.Value;
        _databaseOptions = databaseOptions.Value;
        _repository = repository;
        _loggerFactory = loggerFactory;
        _logger = logger;
    }

    public async Task<Buffer> CreateBuffer(Buffer buffer, CancellationToken cancellationToken)
    {
        string location;
        if (!string.IsNullOrEmpty(buffer.Location))
        {
            location = buffer.Location;
        }
        else
        {
            if (string.IsNullOrEmpty(_defaultLocation))
            {
                throw new ValidationException("A location must be specified for this buffer because there is no default location.");
            }

            location = _defaultLocation;
        }

        if (!_roundRobinCounters!.TryGetValue(location, out var roundRobinCounter))
        {
            throw new ValidationException($"No storage accounts configured for location '{location}'");
        }

        var storageAccountId = roundRobinCounter.GetNextId();
        var client = _storageAccountClients![storageAccountId];

        await client.ServiceClient.CreateBlobContainerAsync(buffer.Id, cancellationToken: cancellationToken);

        buffer = buffer with { Location = location };
        return await _repository.CreateBuffer(buffer, storageAccountId, cancellationToken);
    }

    public async Task<IList<string>> DeleteBuffers(IList<string> ids, CancellationToken cancellationToken)
    {
        var bufferStorageIds = await _repository.GetBufferStorageAccountIds(ids, cancellationToken);

        // Group buffers by storage account ID
        var buffersByStorageAccount = bufferStorageIds
            .Where(pair => pair.accountId.HasValue)
            .GroupBy(pair => pair.accountId!.Value)
            .ToDictionary(
                g => g.Key,
                g => g.Select(pair => pair.bufferId).ToList()
            );

        var deletedIds = new ConcurrentQueue<string>();
        await Parallel.ForEachAsync(buffersByStorageAccount, async (batch, cancellationToken) =>
        {
            var storageAccountId = batch.Key;
            var bufferIds = batch.Value;
            var refreshingClient = await GetRefreshingServiceClient(storageAccountId, cancellationToken);

            await Parallel.ForEachAsync(bufferIds, async (bufferId, cancellationToken) =>
            {
                try
                {
                    var containerClient = refreshingClient.ServiceClient.GetBlobContainerClient(bufferId);
                    await containerClient.DeleteIfExistsAsync(cancellationToken: cancellationToken);
                    deletedIds.Enqueue(bufferId);
                }
                catch (Exception ex)
                {
                    _logger.FailedToDeleteBuffer(bufferId, ex);
                }
            });
        });

        return [.. deletedIds];
    }

    public async Task<IList<(string id, bool writeable, BufferAccess? bufferAccess)>> CreateBufferAccessUrls(IList<(string id, bool writeable)> requests, bool preferTcp, bool checkExists, TimeSpan? accessTtl, CancellationToken cancellationToken)
    {
        var ttl = accessTtl ?? DefaultAccessTtl;
        if (ttl < TimeSpan.FromSeconds(30))
        {
            throw new ValidationException("Access TTL must be at least 30 seconds.");
        }
        else if (ttl > DefaultAccessTtl)
        {
            throw new ValidationException($"Access TTL must be less than or equal to {DefaultAccessTtl}.");
        }

        var storageAccountIds = await _repository.GetBufferStorageAccountIds(requests.Select(r => r.id).ToArray(), cancellationToken);
        var results = new List<(string id, bool writeable, BufferAccess? bufferAccess)>(requests.Count);
        foreach (var (id, writeable) in requests)
        {
            var storageAccountId = storageAccountIds.First(sa => sa.bufferId == id).accountId;
            if (!storageAccountId.HasValue)
            {
                results.Add((id, writeable, null));
                continue;
            }

            var refreshingClient = await GetRefreshingServiceClient(storageAccountId.Value, cancellationToken);

            var permissions = BlobContainerSasPermissions.Read;
            if (writeable)
            {
                permissions |= BlobContainerSasPermissions.Create;
            }

            var containerClient = refreshingClient.ServiceClient.GetBlobContainerClient(id);

            // Create a SAS token that's valid for one hour.
            var start = DateTimeOffset.UtcNow;
            BlobSasBuilder sasBuilder = new()
            {
                BlobContainerName = containerClient.Name,
                Resource = "c",
                StartsOn = start,
                ExpiresOn = start.Add(ttl),
                Protocol = SasProtocol.Https
            };
            sasBuilder.SetPermissions(permissions);

            var uriBuilder = new BlobUriBuilder(containerClient.Uri)
            {
                Sas = sasBuilder.ToSasQueryParameters(refreshingClient.UserDelegationKey, containerClient.AccountName)
            };

            results.Add((id, writeable, new(uriBuilder.ToUri())));
        }

        return results;
    }

    public async Task<Run> ExportBuffers(ExportBuffersRequest exportBufferRequest, CancellationToken cancellationToken)
    {
        if (string.IsNullOrEmpty(exportBufferRequest.DestinationStorageEndpoint))
        {
            throw new ValidationException("Destination storage endpoint is required.");
        }

        int storageAccountId = -1;
        if (string.IsNullOrEmpty(exportBufferRequest.SourceStorageAccountName))
        {
            if (_storageAccountClients!.Count > 1)
            {
                throw new ValidationException("A source storage account name is required when multiple storage accounts are configured.");
            }

            storageAccountId = _storageAccountClients!.Keys.Single();
        }
        else
        {
            bool found = false;
            foreach (var kvp in _storageAccountClients!)
            {
                if (kvp.Value.ServiceClient.AccountName.Equals(exportBufferRequest.SourceStorageAccountName, StringComparison.OrdinalIgnoreCase))
                {
                    storageAccountId = kvp.Key;
                    found = true;
                    break;
                }
            }

            if (!found)
            {
                throw new ValidationException($"Source storage account '{exportBufferRequest.SourceStorageAccountName}' not found.");
            }
        }

        var args = new List<string>
        {
            "export",
            "--log-format", "json",
            "--source-storage-account-id", storageAccountId.ToString(CultureInfo.InvariantCulture),
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

    public async Task<Run> ImportBuffers(ImportBuffersRequest importBuffersRequest, CancellationToken cancellationToken)
    {
        int storageAccountId = -1;
        if (string.IsNullOrEmpty(importBuffersRequest.StorageAccountName))
        {
            if (_storageAccountClients!.Count > 1)
            {
                throw new InvalidOperationException("A storage account name is required when multiple storage accounts are configured.");
            }

            storageAccountId = _storageAccountClients!.Keys.Single();
        }
        else
        {
            var found = false;
            foreach (var kvp in _storageAccountClients!)
            {
                if (kvp.Value.ServiceClient.AccountName.Equals(importBuffersRequest.StorageAccountName, StringComparison.OrdinalIgnoreCase))
                {
                    storageAccountId = kvp.Key;
                    found = true;
                    break;
                }
            }

            if (!found)
            {
                throw new ValidationException($"Storage account '{importBuffersRequest.StorageAccountName}' not found.");
            }
        }

        var args = new List<string>
        {
            "import",
            "--log-format", "json",
            "--storage-account-id", storageAccountId.ToString(CultureInfo.InvariantCulture),
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
        async Task<HealthCheckResult> InnerCheck(BlobServiceClientWithRefreshingCredentials client)
        {
            await client.ServiceClient.GetBlobContainerClient("healthcheck").ExistsAsync(cancellationToken);
            if (client.UserDelegationKey is null || client.UserDelegationKey.SignedExpiresOn < DateTimeOffset.UtcNow)
            {
                return HealthCheckResult.Unhealthy("User delegation key is not valid");
            }

            return HealthCheckResult.Healthy();
        }

        var clients = _storageAccountClients;
        if (clients is null)
        {
            return HealthCheckResult.Unhealthy("Storage account clients not initialized");
        }

        var remainingChecks = clients.Values.Select(InnerCheck).ToList();
        while (remainingChecks.Count > 0)
        {
            var completed = await Task.WhenAny(remainingChecks);
            remainingChecks.Remove(completed);
            var result = await completed;
            if (result.Status != HealthStatus.Healthy)
            {
                return result;
            }
        }

        return HealthCheckResult.Healthy();
    }

    public async Task StartAsync(CancellationToken cancellationToken)
    {
        if (_storageOptions.StorageAccounts.Count == 0)
        {
            throw new InvalidOperationException("No storage accounts configured");
        }

        Dictionary<int, string> idToNameMap = await _repository.UpsertStorageAccounts(GetStorageAccounts(), cancellationToken);

        _storageAccountClients = idToNameMap.ToImmutableDictionary(
            kvp => kvp.Key,
            kvp => new BlobServiceClientWithRefreshingCredentials(
                new BlobServiceClient(new Uri(_storageOptions.StorageAccounts.Single(sa => sa.Name.Equals(kvp.Value, StringComparison.OrdinalIgnoreCase)).Endpoint), _credential),
                _loggerFactory.CreateLogger<BlobServiceClientWithRefreshingCredentials>()));

        foreach (var client in _storageAccountClients.Values)
        {
            await client.StartAsync(cancellationToken);
        }

        _roundRobinCounters = _storageOptions.StorageAccounts.GroupBy(sa => sa.Location, StringComparer.OrdinalIgnoreCase).ToImmutableDictionary(
            g => g.Key,
            g => new RoundRobinCounter(g.Select(sa => idToNameMap.Single(kvp => kvp.Value.Equals(sa.Name, StringComparison.OrdinalIgnoreCase)).Key).ToArray()),
            StringComparer.OrdinalIgnoreCase);

        if (!string.IsNullOrEmpty(_storageOptions.DefaultLocation) && _roundRobinCounters.ContainsKey(_storageOptions.DefaultLocation))
        {
            _defaultLocation = _storageOptions.DefaultLocation;
        }
        else if (_roundRobinCounters.Count == 1)
        {
            _defaultLocation = _roundRobinCounters.Keys.Single();
        }
    }

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        if (_storageAccountClients is not null)
        {
            foreach (var client in _storageAccountClients.Values)
            {
                await client.StopAsync(cancellationToken);
            }
        }
    }

    public IList<StorageAccount> GetStorageAccounts()
    {
        return _storageOptions.StorageAccounts.Select(sa => new StorageAccount(sa.Name, sa.Location, sa.Endpoint)).ToList();
    }

    private async ValueTask<BlobServiceClientWithRefreshingCredentials> GetRefreshingServiceClient(int storageAccountId, CancellationToken cancellationToken)
    {
        if (_storageAccountClients!.TryGetValue(storageAccountId, out var client))
        {
            return client;
        }

        var endpoint = await _repository.GetStorageAccountEndpoint(storageAccountId, cancellationToken);
        var serviceClient = new BlobServiceClient(new Uri(endpoint), _credential);
        var refreshingClient = new BlobServiceClientWithRefreshingCredentials(serviceClient, _loggerFactory.CreateLogger<BlobServiceClientWithRefreshingCredentials>());
        await refreshingClient.StartAsync(cancellationToken);

        var returnResult = ImmutableInterlocked.GetOrAdd(ref _storageAccountClients, storageAccountId, refreshingClient);
        if (!ReferenceEquals(returnResult, refreshingClient))
        {
            await refreshingClient.StopAsync(cancellationToken);
        }

        return returnResult;
    }

    public async Task TryMarkBufferAsFailed(string id, CancellationToken cancellationToken)
    {
        var ids = await _repository.GetBufferStorageAccountIds([id], cancellationToken);
        if (ids is not [{ accountId: int accountId }])
        {
            return;
        }

        var refreshingClient = await GetRefreshingServiceClient(accountId, cancellationToken);
        var containerClient = refreshingClient.ServiceClient.GetBlobContainerClient(id);

        var blobClient = containerClient.GetBlobClient(BufferMetadata.EndMetadataBlobName);
        var doNotOverwriteOptions = new BlobUploadOptions { Conditions = new() { IfNoneMatch = new("*"), }, };
        try
        {
            await blobClient.UploadAsync(new BinaryData(BufferMetadata.FailedEndMetadataContent), doNotOverwriteOptions, cancellationToken: cancellationToken);
        }
        catch (RequestFailedException e) when (e.Status == (int)HttpStatusCode.PreconditionFailed)
        {
        }
        catch (Exception e)
        {
            _logger.FailedToMarkBufferAsFailed(e);
        }
    }

    private sealed class RoundRobinCounter
    {
        private readonly int[] _ids;

        private uint _counter;

        public RoundRobinCounter(int[] ids)
        {
            _ids = ids;
        }

        public int GetNextId() => _ids.Length switch
        {
            1 => _ids[0],
            _ => _ids[(int)(Interlocked.Increment(ref _counter) % _ids.Length)]
        };
    }

    private sealed class BlobServiceClientWithRefreshingCredentials : BackgroundService
    {
        private static readonly TimeSpan s_userDelegationKeyDuration = TimeSpan.FromDays(1);
        private readonly ILogger<BlobServiceClientWithRefreshingCredentials> _logger;

        public BlobServiceClientWithRefreshingCredentials(BlobServiceClient client, ILogger<BlobServiceClientWithRefreshingCredentials> logger)
        {
            ServiceClient = client;
            _logger = logger;
        }

        public BlobServiceClient ServiceClient { get; }
        public UserDelegationKey? UserDelegationKey { get; set; }

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
                            if (UserDelegationKey is not null && UserDelegationKey.SignedExpiresOn > DateTimeOffset.UtcNow)
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
            UserDelegationKey = await ServiceClient.GetUserDelegationKeyAsync(start, start.Add(s_userDelegationKeyDuration), cancellationToken);
        }
    }
}
