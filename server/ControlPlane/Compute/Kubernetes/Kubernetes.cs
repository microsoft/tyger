// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Diagnostics;
using System.Net;
using k8s;
using Microsoft.Extensions.Http.Resilience;
using Microsoft.Extensions.Options;
using Polly;
using Tyger.Common.DependencyInjection;
using Tyger.ControlPlane.Buffers;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Logging;
using Tyger.ControlPlane.Runs;
using Tyger.ControlPlane.ServiceMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public static class Kubernetes
{
    public static void AddKubernetes(this IHostApplicationBuilder builder)
    {
        builder.Services.AddOptions<KubernetesCoreOptions>().BindConfiguration("compute:kubernetes").ValidateDataAnnotations().ValidateOnStart();

        builder.Services.AddSingleton<LoggingHandler>();
        builder.Services.AddSingleton(sp =>
        {
            var kubernetesOptions = sp.GetRequiredService<IOptions<KubernetesCoreOptions>>().Value;
            var config = string.IsNullOrEmpty(kubernetesOptions.KubeconfigPath)
                ? KubernetesClientConfiguration.InClusterConfig()
                : KubernetesClientConfiguration.BuildConfigFromConfigFile(kubernetesOptions.KubeconfigPath);

            var pipeline = new ResiliencePipelineBuilder<HttpResponseMessage>()
                .AddRetry(new HttpStandardResilienceOptions().Retry)
                .AddTimeout(TimeSpan.FromSeconds(100))
                .Build();

#pragma warning disable EXTEXP0001 // Type is for evaluation purposes only and is subject to change or removal in future updates. Suppress this diagnostic to proceed.
            var resilienceHander = new ResilienceHandler(pipeline);
#pragma warning restore EXTEXP0001 // Type is for evaluation purposes only and is subject to change or removal in future updates. Suppress this diagnostic to proceed.

            return new k8s.Kubernetes(config, sp.GetRequiredService<LoggingHandler>(), resilienceHander);
        });
        builder.Services.AddSingleton<IKubernetes>(sp => sp.GetRequiredService<k8s.Kubernetes>());

        builder.Services.AddHttpClient(Options.DefaultName).AddStandardResilienceHandler();
        builder.Services.AddSingleton<IReplicaDatabaseVersionProvider, KubernetesReplicaDatabaseVersionProvider>();

        if (builder is WebApplicationBuilder)
        {
            builder.Services.AddOptions<KubernetesApiOptions>().BindConfiguration("compute:kubernetes").ValidateDataAnnotations().ValidateOnStart();

            builder.Services.AddSingleton(sp => new LeaseManager(sp.GetRequiredService<Repository>(), "tyger-server-leader"));
            builder.AddServiceWithPriority(ServiceDescriptor.Singleton<IHostedService>(sp => sp.GetRequiredService<LeaseManager>()), 20);
            builder.Services.AddSingleton<KubernetesRunCreator>();
            builder.Services.AddSingleton<IRunCreator>(sp => sp.GetRequiredService<KubernetesRunCreator>());
            builder.Services.AddHostedService(sp => sp.GetRequiredService<KubernetesRunCreator>());
            builder.Services.AddSingleton(sp => new Lazy<IRunCreator>(() => sp.GetRequiredService<KubernetesRunCreator>()));
            builder.Services.AddSingleton<ICapabilitiesContributor>(sp => sp.GetRequiredService<KubernetesRunCreator>());
            builder.Services.AddSingleton<KubernetesRunReader>();
            builder.Services.AddSingleton<IRunReader>(sp => sp.GetRequiredService<KubernetesRunReader>());
            builder.Services.AddSingleton<KubernetesRunUpdater>();
            builder.Services.AddSingleton<IRunUpdater>(sp => sp.GetRequiredService<KubernetesRunUpdater>());
            builder.Services.AddSingleton<ILogSource, KubernetesRunLogReader>();
            builder.Services.AddSingleton<IEphemeralBufferProvider, KubernetesEphemeralBufferProvider>();
            builder.Services.AddSingleton<RunStateObserver>();
            builder.Services.AddHostedService(sp => sp.GetRequiredService<RunStateObserver>());
            builder.Services.AddSingleton<RunChangeFeed>();
            builder.Services.AddHostedService(sp => sp.GetRequiredService<RunChangeFeed>());
            builder.Services.AddHostedService<RunFinalizer>();
        }
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

    public Dictionary<string, string>? CustomIdentities { get; set; }

    [Required]
    public string NoOpConfigMap { get; set; } = null!;

    [Required]
    public required string WorkerWaiterImage { get; init; }

    [MinLength(1)]
    public List<ClusterOptions> Clusters { get; } = [];

    [Required]
    public required string CurrentPodUid { get; init; }
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
        var start = Stopwatch.GetTimestamp();
        var resp = await base.SendAsync(request, cancellationToken);
        var end = Stopwatch.GetTimestamp();
        var elapsed = (end - start) * 1000.0 / Stopwatch.Frequency;

        string? errorBody = "";
        if (!resp.IsSuccessStatusCode && !(request.Method == HttpMethod.Delete && resp.StatusCode == HttpStatusCode.NotFound))
        {
            await resp.Content.LoadIntoBufferAsync(cancellationToken);
            errorBody = await resp.Content.ReadAsStringAsync(cancellationToken);
        }

        _logger.ExecutedKubernetesRequest(request.Method, request?.RequestUri?.ToString(), elapsed, (int)resp.StatusCode, errorBody);
        return resp;
    }
}
