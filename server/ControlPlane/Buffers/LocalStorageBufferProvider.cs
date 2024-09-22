// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Net.Sockets;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;
using Tyger.Common.Buffers;
using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Buffers;

public sealed class LocalStorageBufferProvider : IBufferProvider, IHealthCheck, IDisposable
{
    private readonly LocalBufferStorageOptions _storageOptions;
    private readonly Uri _baseUrl;
    private readonly Uri _baseTcpUrl;
    private readonly HttpClient _dataPlaneClient;
    private readonly SignDataFunc _signData;

    public LocalStorageBufferProvider(IOptions<LocalBufferStorageOptions> storageOptions, IOptions<BufferOptions> bufferOptions)
    {
        _storageOptions = storageOptions.Value;

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

    public async Task CreateBuffer(string id, CancellationToken cancellationToken)
    {
        var queryString = LocalSasHandler.GetSasQueryString(id, SasResourceType.Container, SasAction.Create, _signData);
        var resp = await _dataPlaneClient.PutAsync($"v1/containers/{id}{queryString}", null, cancellationToken);
        resp.EnsureSuccessStatusCode();
    }

    public Uri CreateBufferAccessUrl(string id, bool writeable, bool preferTcp)
    {
        var action = writeable ? SasAction.Create | SasAction.Read : SasAction.Read;
        var queryString = LocalSasHandler.GetSasQueryString(id, SasResourceType.Blob, action, _signData);
        return new Uri(preferTcp ? _baseTcpUrl : _baseUrl, $"v1/containers/{id}{queryString}");
    }

    public Task<Run> ExportBuffers(ExportBuffersRequest exportBufferRequest, CancellationToken cancellationToken)
    {
        throw new ValidationException("Exporting buffers is not supported with local storage.");
    }

    public Task<Run> ImportBuffers(CancellationToken cancellationToken)
    {
        throw new ValidationException("Importing buffers is not supported with local storage.");
    }

    public void Dispose() => _dataPlaneClient.Dispose();
}
