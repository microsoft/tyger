using System.ComponentModel.DataAnnotations;
using k8s;
using Microsoft.Extensions.Options;
using Tyger.Server.Logging;
using Tyger.Server.Model;

namespace Tyger.Server.Kubernetes;

public static class Kubernetes
{
    public static void AddKubernetes(this IServiceCollection services, bool isApi)
    {
        if (isApi)
        {
            services.AddOptions<KubernetesApiOptions>().BindConfiguration("kubernetes").ValidateDataAnnotations().ValidateOnStart();
        }

        services.AddOptions<KubernetesCoreOptions>().BindConfiguration("kubernetes").ValidateDataAnnotations().ValidateOnStart();

        services.AddSingleton<LoggingHandler>();
        services.AddSingleton(sp =>
        {
            var kubernetesOptions = sp.GetRequiredService<IOptions<KubernetesApiOptions>>().Value;
            var config = string.IsNullOrEmpty(kubernetesOptions.KubeconfigPath)
                ? KubernetesClientConfiguration.InClusterConfig()
                : KubernetesClientConfiguration.BuildConfigFromConfigFile(kubernetesOptions.KubeconfigPath);
            return new k8s.Kubernetes(config, sp.GetRequiredService<LoggingHandler>());
        });
        services.AddSingleton<IKubernetes>(sp => sp.GetRequiredService<k8s.Kubernetes>());

        if (isApi)
        {
            services.AddSingleton<RunCreator>();
            services.AddSingleton<RunReader>();
            services.AddSingleton<RunUpdater>();
            services.AddSingleton<ILogSource, RunLogReader>();
            services.AddSingleton<RunSweeper>();
            services.AddSingleton<IHostedService, RunSweeper>(sp => sp.GetRequiredService<RunSweeper>());
        }
    }

    public static void MapClusters(this WebApplication app)
    {
        app.MapGet("/v1/clusters/", (IOptions<KubernetesApiOptions> config) =>
        {
            return GetClustersResponse(config.Value);
        });

        app.MapGet("/v1/clusters/{name}", (string name, IOptions<KubernetesApiOptions> config) =>
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

    private static List<Cluster> GetClustersResponse(KubernetesApiOptions options)
    {
        return options.Clusters
            .Where(c => c.ApiHost) // For now we don't support multiple clusters
            .Select(c =>
                new Cluster(
                    c.Name,
                    c.Location,
                    c.UserNodePools.Select(np =>
                        new NodePool(np.Name, np.VmSize)).ToList()))
            .ToList();
    }
}

public class KubernetesCoreOptions
{
    public string? KubeconfigPath { get; set; }

    [Required]
    public string Namespace { get; set; } = null!;
}

public class KubernetesApiOptions : KubernetesCoreOptions
{
    [Required]
    public string JobServiceAccount { get; set; } = null!;

    [Required]
    public string NoOpConfigMap { get; set; } = null!;

    [Required]
    public required string WorkerWaiterImage { get; init; }

    [MinLength(1)]
    public List<ClusterOptions> Clusters { get; } = [];
}

public class ClusterOptions
{
    [Required]
    public required string Name { get; set; }

    [Required]
    public required string Location { get; init; }

    [Required]
    public required bool ApiHost { get; init; }

    [Required]
    public List<NodePoolOptions> UserNodePools { get; } = [];
}

public class NodePoolOptions
{
    [Required]
    public required string Name { get; init; }

    [Required]
    public required string VmSize { get; init; }
}

/// <summary>
/// Logs interactions with the Kubernetes API
/// </summary>
public class LoggingHandler : DelegatingHandler
{
    private readonly ILogger<LoggingHandler> _logger;

    public LoggingHandler(ILogger<LoggingHandler> logger)
    {
        _logger = logger;
    }

    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        var resp = await base.SendAsync(request, cancellationToken);
        string? errorBody = "";
        if (!resp.IsSuccessStatusCode)
        {
            await resp.Content.LoadIntoBufferAsync();
            errorBody = await resp.Content.ReadAsStringAsync(cancellationToken);
        }

        _logger.ExecutedKubernetesRequest(request.Method, request?.RequestUri?.ToString(), (int)resp.StatusCode, errorBody);
        return resp;
    }
}
