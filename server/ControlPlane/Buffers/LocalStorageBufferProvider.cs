using System.Net.Sockets;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;
using Tyger.Common.Buffers;

namespace Tyger.ControlPlane.Buffers;

public sealed class LocalStorageBufferProvider : IBufferProvider, IHealthCheck, IDisposable
{
    private readonly LocalBufferStorageOptions _options;
    private readonly Uri _baseUrl;
    private readonly HttpClient _dataPlaneClient;
    private readonly SignDataFunc _signData;

    public LocalStorageBufferProvider(IOptions<LocalBufferStorageOptions> bufferOptions)
    {
        _options = bufferOptions.Value;

        var baseUrl = _options.DataPlaneEndpoint.ToString();
        if (_options.DataPlaneEndpoint.Scheme is "http+unix" or "https+unix")
        {
            string? socketPath = null;
            var colonIndex = _options.DataPlaneEndpoint.AbsolutePath.IndexOf(':');
            if (colonIndex < 0)
            {
                socketPath = _options.DataPlaneEndpoint.AbsolutePath;
                baseUrl += ":";
            }
            else
            {
                socketPath = _options.DataPlaneEndpoint.AbsolutePath[..colonIndex];
            }

            var socketsHandler = new SocketsHttpHandler()
            {
                ConnectCallback = async (sockHttpConnContext, ctxToken) =>
                {
                    var socket = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);

                    var endpoint = new UnixDomainSocketEndPoint(socketPath);
                    await socket.ConnectAsync(endpoint, ctxToken);
                    return new NetworkStream(socket);
                },
            };

            var httpClientBaseUriBuilder = new UriBuilder()
            {
                Scheme = _options.DataPlaneEndpoint.Scheme[.._options.DataPlaneEndpoint.Scheme.IndexOf('+')],
                Host = "ignored",
            };

            if (colonIndex >= 0 && _options.DataPlaneEndpoint.AbsolutePath.Length > colonIndex + 1)
            {
                httpClientBaseUriBuilder.Path = _options.DataPlaneEndpoint.AbsolutePath[(colonIndex + 1)..];
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

        if (_dataPlaneClient.BaseAddress == null)
        {
            _dataPlaneClient.BaseAddress = _baseUrl;
        }

        _signData = DigitalSignature.CreateSingingFunc(_options.SigningCertificatePath);
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

    public Uri CreateBufferAccessUrl(string id, bool writeable)
    {
        var action = writeable ? SasAction.Create | SasAction.Read : SasAction.Read;
        var queryString = LocalSasHandler.GetSasQueryString(id, SasResourceType.Blob, action, _signData);
        return new Uri(_baseUrl, $"v1/containers/{id}{queryString}");
    }

    public void Dispose() => _dataPlaneClient.Dispose();
}
