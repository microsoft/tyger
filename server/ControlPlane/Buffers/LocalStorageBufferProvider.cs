// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Net.Sockets;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;
using Tyger.Common.Buffers;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Buffer = Tyger.ControlPlane.Model.Buffer;

namespace Tyger.ControlPlane.Buffers;

public sealed class LocalStorageBufferProvider : IBufferProvider, IHostedService, IHealthCheck, IDisposable
{
    public const string AccountName = "local";
    public const string AccountLocation = "local";

    private readonly LocalBufferStorageOptions _storageOptions;
    private readonly Uri _baseUrl;
    private readonly Uri _baseTcpUrl;
    private readonly HttpClient _dataPlaneClient;
    private readonly SignDataFunc _signData;
    private readonly Repository _repository;
    private readonly ILogger<LocalStorageBufferProvider> _logger;
    private int _storageAccountId;

    public LocalStorageBufferProvider(IOptions<LocalBufferStorageOptions> storageOptions, IOptions<BufferOptions> bufferOptions, Repository repository, ILogger<LocalStorageBufferProvider> logger)
    {
        _storageOptions = storageOptions.Value;
        _repository = repository;
        _logger = logger;
        var baseUrl = _storageOptions.DataPlaneEndpoint.ToString();
        if (_storageOptions.DataPlaneEndpoint.Scheme is "http+unix" or "https+unix")
        {
            string? socketPath = null;
            var colonIndex = _storageOptions.DataPlaneEndpoint.AbsolutePath.IndexOf(':');
            if (colonIndex < 0)
            {
                socketPath = _storageOptions.DataPlaneEndpoint.AbsolutePath;
                baseUrl += ":";
            }
            else
            {
                socketPath = _storageOptions.DataPlaneEndpoint.AbsolutePath[..colonIndex];
            }

            var socketsHandler = new SocketsHttpHandler()
            {
                ConnectCallback = async (sockHttpConnContext, ctxToken) =>
                {
                    var socket = new System.Net.Sockets.Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);

                    var endpoint = new UnixDomainSocketEndPoint(socketPath);
                    await socket.ConnectAsync(endpoint, ctxToken);
                    return new NetworkStream(socket);
                },
            };

            var httpClientBaseUriBuilder = new UriBuilder()
            {
                Scheme = _storageOptions.DataPlaneEndpoint.Scheme[.._storageOptions.DataPlaneEndpoint.Scheme.IndexOf('+')],
                Host = "ignored",
            };

            if (colonIndex >= 0 && _storageOptions.DataPlaneEndpoint.AbsolutePath.Length > colonIndex + 1)
            {
                httpClientBaseUriBuilder.Path = _storageOptions.DataPlaneEndpoint.AbsolutePath[(colonIndex + 1)..];
            }

            if (!httpClientBaseUriBuilder.Path.EndsWith('/'))
            {
                httpClientBaseUriBuilder.Path += "/";
            }

            _dataPlaneClient = new HttpClient(socketsHandler) { BaseAddress = httpClientBaseUriBuilder.Uri };
        }
        else
        {
            _dataPlaneClient = new HttpClient();
        }

        if (!baseUrl.EndsWith('/'))
        {
            baseUrl += '/';
        }

        _baseUrl = new Uri(baseUrl);

        var tcpUrl = _storageOptions.TcpDataPlaneEndpoint.ToString();
        if (!tcpUrl.EndsWith('/'))
        {
            tcpUrl += '/';
        }

        _baseTcpUrl = new Uri(tcpUrl);

        if (_dataPlaneClient.BaseAddress == null)
        {
            _dataPlaneClient.BaseAddress = _baseUrl;
        }

        if (string.IsNullOrEmpty(bufferOptions.Value.PrimarySigningPrivateKeyPath))
        {
            throw new InvalidOperationException("A value for buffers::primarySigningPrivateKeyPath must be provided.");
        }

        _signData = DigitalSignature.CreateSingingFunc(
            DigitalSignature.CreateAsymmetricAlgorithmFromPem(bufferOptions.Value.PrimarySigningPrivateKeyPath));
    }

    public async Task<bool> BufferExists(string id, CancellationToken cancellationToken)
    {
        var queryString = LocalSasHandler.GetSasQueryString(id, SasResourceType.Container, SasAction.Read, _signData);
        var req = new HttpRequestMessage(HttpMethod.Head, $"v1/containers/{id}{queryString}");
        var resp = await _dataPlaneClient.SendAsync(req, cancellationToken);

        return (int)resp.StatusCode switch
        {
            StatusCodes.Status200OK => true,
            StatusCodes.Status404NotFound => false,
            _ => throw new HttpRequestException($"Unexpected status code {resp.StatusCode}: {resp.ReasonPhrase}", null, resp.StatusCode),
        };
    }

    public async Task<HealthCheckResult> CheckHealthAsync(HealthCheckContext context, CancellationToken cancellationToken = default)
    {
        var resp = await _dataPlaneClient.GetAsync("healthcheck", cancellationToken);
        resp.EnsureSuccessStatusCode();
        return HealthCheckResult.Healthy();
    }

    public async Task<Buffer> CreateBuffer(Buffer buffer, CancellationToken cancellationToken)
    {
        if (!string.IsNullOrEmpty(buffer.Location) && !buffer.Location.Equals(AccountLocation, StringComparison.OrdinalIgnoreCase))
        {
            throw new ValidationException($"Buffer location can only be '{AccountLocation}.");
        }

        buffer = buffer with { Location = AccountLocation };

        var queryString = LocalSasHandler.GetSasQueryString(buffer.Id, SasResourceType.Container, SasAction.Create, _signData);
        var resp = await _dataPlaneClient.PutAsync($"v1/containers/{buffer.Id}{queryString}", null, cancellationToken);
        resp.EnsureSuccessStatusCode();
        return await _repository.CreateBuffer(buffer, _storageAccountId, cancellationToken);
    }

    public async Task<int> DeleteBuffers(IList<string> ids, CancellationToken cancellationToken)
    {
        var deletedIds = new List<string>();
        foreach (var id in ids)
        {
            var queryString = LocalSasHandler.GetSasQueryString(id, SasResourceType.Container, SasAction.Delete, _signData);
            var resp = await _dataPlaneClient.DeleteAsync($"v1/containers/{id}{queryString}", cancellationToken);

            // TODO Joe: Handle errors gracefully
            resp.EnsureSuccessStatusCode();

            deletedIds.Add(id);
        }

        return await _repository.HardDeleteBuffers(deletedIds, cancellationToken);
    }

    public async Task<IList<(string id, bool writeable, BufferAccess? bufferAccess)>> CreateBufferAccessUrls(IList<(string id, bool writeable)> requests, bool preferTcp, bool checkExists, CancellationToken cancellationToken)
    {
        var responses = new List<(string id, bool writeable, BufferAccess? bufferAccess)>(requests.Count);
        foreach (var (id, writeable) in requests)
        {
            if (checkExists && !await BufferExists(id, cancellationToken))
            {
                responses.Add((id, writeable, null));
                continue;
            }

            var action = writeable ? SasAction.Create | SasAction.Read : SasAction.Read;
            var queryString = LocalSasHandler.GetSasQueryString(id, SasResourceType.Blob, action, _signData);
            responses.Add((id, writeable, new BufferAccess(new Uri(preferTcp ? _baseTcpUrl : _baseUrl, $"v1/containers/{id}{queryString}"))));
        }

        return responses;
    }

    public IList<StorageAccount> GetStorageAccounts()
    {
        return [new(AccountName, AccountLocation, _baseUrl.ToString())];
    }

    public Task<Run> ExportBuffers(ExportBuffersRequest exportBufferRequest, CancellationToken cancellationToken)
    {
        throw new ValidationException("Exporting buffers is not supported with local storage.");
    }

    public Task<Run> ImportBuffers(ImportBuffersRequest importBuffersRequest, CancellationToken cancellationToken)
    {
        throw new ValidationException("Importing buffers is not supported with local storage.");
    }

    public void Dispose() => _dataPlaneClient.Dispose();

    public async Task StartAsync(CancellationToken cancellationToken)
    {
        var accounts = await _repository.UpsertStorageAccounts(GetStorageAccounts(), cancellationToken);
        if (accounts.Count != 1)
        {
            throw new InvalidOperationException("Failed to upsert storage account.");
        }

        _storageAccountId = accounts.Single().Key;
    }

    public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;

    public async Task TryMarkBufferAsFailed(string id, CancellationToken cancellationToken)
    {
        var queryString = LocalSasHandler.GetSasQueryString(id, SasResourceType.Blob, SasAction.Create, _signData);
        var request = new HttpRequestMessage(HttpMethod.Put, $"v1/containers/{id}/{BufferMetadata.EndMetadataBlobName}{queryString}")
        {
            Content = new ByteArrayContent(BufferMetadata.FailedEndMetadataContent)
        };

        try
        {
            using var response = await _dataPlaneClient.SendAsync(request, cancellationToken);
            if (response.StatusCode != System.Net.HttpStatusCode.Forbidden) // this is the status code when the blob already exists and we only have create permission
            {
                response.EnsureSuccessStatusCode();
            }
        }
        catch (Exception e)
        {
            _logger.FailedToMarkBufferAsFailed(e);
        }
    }
}
