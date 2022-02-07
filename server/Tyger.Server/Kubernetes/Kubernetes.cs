using System.ComponentModel.DataAnnotations;
using k8s;
using Microsoft.Extensions.Options;

namespace Tyger.Server.Kubernetes;

public static class Kubernetes
{
    public static void AddKubernetes(this IServiceCollection services)
    {
        services.AddOptions<KubernetesOptions>().BindConfiguration("kubernetes").ValidateDataAnnotations().ValidateOnStart();
        services.AddSingleton(sp =>
        {
            var kubernetesOptions = sp.GetRequiredService<IOptions<KubernetesOptions>>().Value;
            var config = string.IsNullOrEmpty(kubernetesOptions.KubeconfigPath)
                ? KubernetesClientConfiguration.InClusterConfig()
                : KubernetesClientConfiguration.BuildConfigFromConfigFile(kubernetesOptions.KubeconfigPath);
            return new k8s.Kubernetes(config);
        });

        services.AddSingleton<IKubernetesManager, KubernetesManager>();
    }
}

public class KubernetesOptions
{
    public string? KubeconfigPath { get; set; }
    [Required]
    public string Namespace { get; set; } = "tyger";
}
