using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;

namespace Tyger.Server.StorageServer;

internal class StorageServerHealthCheck : IHealthCheck
{
    private readonly IHttpClientFactory _httpClientFactory;
    private readonly StorageServerOptions _options;

    public StorageServerHealthCheck(IHttpClientFactory httpClientFactory, IOptions<StorageServerOptions> options)
    {
        _httpClientFactory = httpClientFactory;
        _options = options.Value;
    }

    public async Task<HealthCheckResult> CheckHealthAsync(HealthCheckContext context, CancellationToken cancellationToken = default)
    {
        using var client = _httpClientFactory.CreateClient();

        var response = await client.GetAsync($"{_options.Uri}/v1/blobs?subject=0000000000000000000000000000000000000000&_limit=1", cancellationToken);
        response.EnsureSuccessStatusCode();
        return HealthCheckResult.Healthy();
    }
}
