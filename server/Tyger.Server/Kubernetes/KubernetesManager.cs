using System.ComponentModel.DataAnnotations;
using System.Globalization;
using System.Text.Json;
using k8s;
using Microsoft.Extensions.Options;
using Tyger.Server.Buffers;
using Tyger.Server.Database;
using Tyger.Server.Model;
using Tyger.Server.StorageServer;

namespace Tyger.Server.Kubernetes;

public interface IKubernetesManager
{
    Task<Run> CreateRun(Run newRun, CancellationToken cancellationToken);
    Task<Run?> GetRun(string id, CancellationToken cancellationToken);
}

public class KubernetesManager : IKubernetesManager
{
    private const string SecretMountPath = "/etc/buffer-sas-tokens";

    private readonly k8s.Kubernetes _client;
    private readonly IRepository _repository;
    private readonly BufferManager _bufferManager;
    private readonly KubernetesOptions _k8sOptions;
    private readonly StorageServerOptions _storageServerOptions;
    private readonly ILogger<KubernetesManager> _logger;

    public KubernetesManager(
        k8s.Kubernetes client,
        IRepository repository,
        BufferManager bufferManager,
        IOptions<KubernetesOptions> k8sOptions,
        IOptions<StorageServerOptions> storageServerOptions,
        ILogger<KubernetesManager> logger)
    {
        _client = client;
        _repository = repository;
        _bufferManager = bufferManager;
        _k8sOptions = k8sOptions.Value;
        _storageServerOptions = storageServerOptions.Value;
        _logger = logger;
    }

    public async Task<Run> CreateRun(Run newRun, CancellationToken cancellationToken)
    {
        (var codespec, var normalizedCodespecRef) = await GetCodespec(newRun.Codespec, cancellationToken);

        string? targetNodePool = null;
        bool targetsGpuNodePool = false;
        if (newRun.ComputeTarget != null)
        {
            IEnumerable<ClusterOptions> candidateClusters;
            if (!string.IsNullOrEmpty(newRun.ComputeTarget.Cluster))
            {
                if (!_k8sOptions.Clusters.TryGetValue(newRun.ComputeTarget.Cluster, out var cluster))
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Unknown cluster '{0}'", newRun.ComputeTarget.Cluster));
                }

                candidateClusters = new[] { cluster };
            }
            else
            {
                candidateClusters = _k8sOptions.Clusters.Values;
            }

            if (!string.IsNullOrEmpty(newRun.ComputeTarget.NodePool))
            {
                targetNodePool = newRun.ComputeTarget.NodePool;
                var pool = candidateClusters.Aggregate(default(NodePoolOptions), (found, curr) => curr.UserNodePools.TryGetValue(targetNodePool, out var np) ? np : found);
                if (pool == null)
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Unknown nodepool '{0}'", targetNodePool));
                }

                targetsGpuNodePool = DoesVmHaveSupportedGpu(pool.VmSize);

                if (!targetsGpuNodePool && codespec.Resources?.Gpu is k8s.Models.ResourceQuantity q && q.ToDecimal() != 0)
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Nodepool '{0}' does not have GPUs and cannot satisfy GPU request '{1}'", targetNodePool, q));
                }
            }
        }

        var id = UniqueId.Create();
        var k8sId = K8sIdFromId(id);
        var run = newRun with { Codespec = normalizedCodespecRef, Id = id };

        Dictionary<string, Uri> bufferMap = await GetBufferMap(codespec.Buffers ?? new(null, null), newRun.Buffers ?? new(), cancellationToken);
        var labels = new Dictionary<string, string> { { "tyger", "run" } };
        var secret = new k8s.Models.V1Secret
        {
            Metadata = new()
            {
                Name = k8sId,
                Labels = labels
            },
            StringData = bufferMap.ToDictionary(p => p.Key, p => p.Value.ToString()),
        };

        await _client.CreateNamespacedSecretAsync(secret, _k8sOptions.Namespace, cancellationToken: cancellationToken);
        _logger.CreatedSecret(k8sId);

        var env = new List<k8s.Models.V1EnvVar> { new("MRD_STORAGE_URI", _storageServerOptions.Uri) };
        if (codespec.Env != null)
        {
            env.AddRange(codespec.Env.Select(p => new k8s.Models.V1EnvVar(p.Key, p.Value)));
        }

        env.AddRange(bufferMap.Select(p => new k8s.Models.V1EnvVar($"{p.Key.ToUpperInvariant()}_BUFFER_URI_FILE", $"{SecretMountPath}/{p.Key}")));

        var container = new k8s.Models.V1Container
        {
            Name = "runner",
            Image = codespec.Image,
            Command = codespec.Command,
            Args = codespec.Args,
            Env = env,
            VolumeMounts = new k8s.Models.V1VolumeMount[] {
                new()
                {
                    Name = "buffers",
                    MountPath = SecretMountPath,
                    ReadOnlyProperty = true
                },
            }
        };

        if (codespec.Resources != null)
        {
            var resources = new Dictionary<string, k8s.Models.ResourceQuantity>();
            if (codespec.Resources.Cpu != null)
            {
                resources["cpu"] = codespec.Resources.Cpu;
            }

            if (codespec.Resources.Memory != null)
            {
                resources["memory"] = codespec.Resources.Memory;
            }

            if (codespec.Resources.Gpu != null)
            {
                resources["nvidia.com/gpu"] = codespec.Resources.Gpu;
            }

            container.Resources = new() { Limits = resources, Requests = resources };
        }

        var pod = new k8s.Models.V1Pod
        {
            Metadata = new()
            {
                Name = k8sId,
                Annotations = new Dictionary<string, string> { { "run", JsonSerializer.Serialize(run) } },
                Labels = labels
            },
            Spec = new()
            {
                Containers = new[] { container },
                RestartPolicy = "OnFailure",
                Volumes = new k8s.Models.V1Volume[]
                {
                    new()
                    {
                        Name = "buffers",
                        Secret = new() {SecretName = k8sId}
                    }
                },
                Tolerations = new List<k8s.Models.V1Toleration>
                {
                    new() { Key = "tyger", OperatorProperty= "Equal", Value = "run", Effect = "NoSchedule" } // allow this to run on a user nodepools
                },
                NodeSelector = new Dictionary<string, string> { { "tyger", "run" } } // require this to run on a user nodepool
            }
        };

        if (targetNodePool != null)
        {
            pod.Spec.NodeSelector.Add("agentpool", targetNodePool);
        }

        if (codespec.Resources?.Gpu != null || targetsGpuNodePool)
        {
            pod.Spec.Tolerations.Add(new() { Key = "sku", OperatorProperty = "Equal", Value = "gpu", Effect = "NoSchedule" });
        }

        await _client.CreateNamespacedPodAsync(pod, _k8sOptions.Namespace, cancellationToken: cancellationToken);
        _logger.CreatedRun(k8sId);
        return run;
    }

    private static bool DoesVmHaveSupportedGpu(string vmSize)
    {
        return vmSize.StartsWith("Standard_N", StringComparison.OrdinalIgnoreCase) &&
            !vmSize.EndsWith("_v4", StringComparison.OrdinalIgnoreCase); // unsupported AMD GPU
    }

    private async Task<(Codespec codespec, string normalizedRef)> GetCodespec(string codespecRef, CancellationToken cancellationToken)
    {
        var codespecTokens = codespecRef.Split("/versions/");
        Codespec? codespec;
        int version;

        switch (codespecTokens.Length)
        {
            case 1:
                var reponse = await _repository.GetLatestCodespec(codespecTokens[0], cancellationToken);
                if (reponse == null)
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The codespec '{0}' was not found", codespecTokens[0]));
                }

                (codespec, version) = reponse.Value;
                break;
            case 2:
                if (!int.TryParse(codespecTokens[1], out version))
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "'{0}' is not a valid codespec version", codespecTokens[1]));
                }

                codespec = await _repository.GetCodespecAtVersion(codespecTokens[0], version, cancellationToken);
                if (codespec != null)
                {
                    break;
                }

                // See if it's just the version number that was not found
                var latestResponse = await _repository.GetLatestCodespec(codespecTokens[0], cancellationToken);
                if (latestResponse == null)
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The codespec '{0}' was not found", codespecTokens[0]));
                }

                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The version '{0}' of codespec '{1}' was not found. The latest version is '{2}'.", version, codespecTokens[0], latestResponse.Value.version));

            default:
                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The codespec '{0}' is invalid. The value should be in the form '<codespec_name>' or '<codespec_name>/versions/<version_number>'.", codespecRef));
        }

        var normalizedRef = $"{codespecTokens[0]}/versions/{version}";
        _logger.FoundCodespec(normalizedRef);
        return (codespec, normalizedRef);
    }

    private async Task<Dictionary<string, Uri>> GetBufferMap(BufferParameters parameters, Dictionary<string, string> arguments, CancellationToken cancellationToken)
    {
        arguments = new Dictionary<string, string>(arguments, StringComparer.OrdinalIgnoreCase);
        IEnumerable<(string param, bool writeable)> combinedParameters = (parameters.Inputs?.Select(param => (param, false)) ?? Enumerable.Empty<(string, bool)>())
            .Concat(parameters.Outputs?.Select(param => (param, true)) ?? Enumerable.Empty<(string, bool)>());

        var outputMap = new Dictionary<string, Uri>();

        foreach (var param in combinedParameters)
        {
            if (!arguments.TryGetValue(param.param, out var bufferId))
            {
                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The run is missing required buffer argument '{0}'", param.param));
            }

            var bufferAccess = await _bufferManager.CreateBufferAccessString(bufferId, param.writeable, cancellationToken);
            if (bufferAccess == null)
            {
                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The buffer '{0}' was not found", bufferId));
            }

            outputMap[param.param] = bufferAccess.Uri;
            arguments.Remove(param.param);
        }

        foreach (var arg in arguments)
        {
            throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Buffer argument '{0}' does not correspond to a buffer parameter on the codespec", arg));
        }

        return outputMap;
    }

    private static string K8sIdFromId(string id) => $"run-{id}";

    public async Task<Run?> GetRun(string id, CancellationToken cancellationToken)
    {
        var pod = await _client.ReadNamespacedPodAsync(K8sIdFromId(id), _k8sOptions.Namespace, cancellationToken: cancellationToken);
        var serializedRun = pod.Metadata.Annotations?["run"];
        if (serializedRun == null)
        {
            return null;
        }

        var run = JsonSerializer.Deserialize<Run>(serializedRun);
        if (run == null)
        {
            return null;
        }

        return run with { Status = GetPodStatus(pod) };
    }

    private string GetPodStatus(k8s.Models.V1Pod pod)
    {
        if (pod.Status.ContainerStatuses?.Count == 1)
        {
            var state = pod.Status.ContainerStatuses[0].State;
            if (state.Waiting != null)
            {
                return state.Waiting.Reason;
            }

            if (state.Running != null)
            {
                return "Running";
            }

            if (state.Terminated != null)
            {
                return state.Terminated.Reason;
            }
        }

        if (pod.Status.Phase == "Pending")
        {
            return pod.Status.Phase;
        }

        _logger.UnableToDeterminePodPhase(pod.Metadata.Name);
        return pod.Status.Phase;
    }
}
