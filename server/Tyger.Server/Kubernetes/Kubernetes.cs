using System.ComponentModel.DataAnnotations;
using System.Net;
using k8s;
using Microsoft.Extensions.Options;
using Tyger.Server.Model;

namespace Tyger.Server.Kubernetes;

public static class Kubernetes
{
    public static void AddKubernetes(this IServiceCollection services)
    {
        services.AddOptions<KubernetesOptions>().BindConfiguration("kubernetes").ValidateDataAnnotations().ValidateOnStart();
        services.AddSingleton<BadRequestLoggingHandler>();
        services.AddSingleton(sp =>
        {
            var kubernetesOptions = sp.GetRequiredService<IOptions<KubernetesOptions>>().Value;
            var config = string.IsNullOrEmpty(kubernetesOptions.KubeconfigPath)
                ? KubernetesClientConfiguration.InClusterConfig()
                : KubernetesClientConfiguration.BuildConfigFromConfigFile(kubernetesOptions.KubeconfigPath);
            return new k8s.Kubernetes(config, sp.GetRequiredService<BadRequestLoggingHandler>());
        });

        services.AddScoped<IKubernetesManager, KubernetesManager>();
        services.AddSingleton<IHostedService, KubernetesManager>();
    }

    public static void MapClusters(this WebApplication app)
    {
        app.MapGet("/v1/clusters/", (IOptions<KubernetesOptions> config) =>
        {
            return GetClustersResponse(config.Value);
        });

        app.MapGet("/v1/clusters/{name}", (string name, IOptions<KubernetesOptions> config) =>
        {
            var cluster = GetClustersResponse(config.Value).FirstOrDefault(c => string.Equals(c.Name, name, StringComparison.OrdinalIgnoreCase));
            if (cluster == null)
            {
                return Responses.NotFound();
            }

            return Results.Ok(cluster);
        })
        .Produces<Cluster>()
        .Produces<ErrorBody>(StatusCodes.Status400BadRequest);
    }

    private static IReadOnlyList<Cluster> GetClustersResponse(KubernetesOptions options)
    {
        return options.Clusters
            .Where(c => c.Value.IsPrimary) // For now we don't support multiple clusters
            .Select(c =>
                new Cluster(
                    c.Key,
                    c.Value.Region,
                    c.Value.UserNodePools.Select(n =>
                        new NodePool(n.Key, n.Value.VmSize)).ToList()))
            .ToList();
    }
}

public class KubernetesOptions
{
    public string? KubeconfigPath { get; set; }

    [Required]
    public string Namespace { get; set; } = null!;

    [MinLength(1)]
    public Dictionary<string, ClusterOptions> Clusters { get; } = new(StringComparer.Ordinal);
}

public class ClusterOptions
{
    [Required]
    public string Region { get; init; } = null!;

    public bool IsPrimary { get; init; }

    public Dictionary<string, NodePoolOptions> UserNodePools { get; } = new(StringComparer.Ordinal);
}

public class NodePoolOptions
{
    [Required]
    public string VmSize { get; init; } = null!;
}

/// <summary>
/// Logs response bodies from the Kubernetes API server when an invalid request was issued.
/// </summary>
public class BadRequestLoggingHandler : DelegatingHandler
{
    private readonly ILogger<BadRequestLoggingHandler> _logger;

    public BadRequestLoggingHandler(ILogger<BadRequestLoggingHandler> logger)
    {
        _logger = logger;
    }

    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        var resp = await base.SendAsync(request, cancellationToken);
        if (resp.StatusCode is HttpStatusCode.UnprocessableEntity or HttpStatusCode.BadRequest)
        {
            await resp.Content.LoadIntoBufferAsync();
            _logger.ErrorResponseBody(await resp.Content.ReadAsStringAsync(cancellationToken));
        }

        return resp;
    }
}
