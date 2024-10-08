// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Collections.Immutable;
using System.ComponentModel.DataAnnotations;
using System.Globalization;
using System.Text;
using System.Text.Json;
using k8s;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Buffers;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;
using Tyger.ControlPlane.ServiceMetadata;
using static Tyger.ControlPlane.Compute.Kubernetes.KubernetesMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public class KubernetesRunCreator : RunCreatorBase, IRunCreator, ICapabilitiesContributor
{
    private readonly IKubernetes _client;
    private readonly BufferOptions _bufferOptions;
    private readonly KubernetesApiOptions _k8sOptions;
    private readonly ILogger<KubernetesRunCreator> _logger;
    private static readonly string[] s_waitForWorkerCommand = { "/no-op/no-op" };

    public KubernetesRunCreator(
        IKubernetes client,
        IRepository repository,
        BufferManager bufferManager,
        IOptions<KubernetesApiOptions> k8sOptions,
        IOptions<BufferOptions> bufferOptions,
        ILogger<KubernetesRunCreator> logger)
        : base(repository, bufferManager)
    {
        _client = client;
        _bufferOptions = bufferOptions.Value;
        _k8sOptions = k8sOptions.Value;
        _logger = logger;
    }

    public Capabilities GetCapabilities() => Capabilities.Kubernetes | Capabilities.DistributedRuns | Capabilities.NodePools;

    public async Task<Run> CreateRun(Run newRun, CancellationToken cancellationToken)
    {
        // Phase 1: Validate newRun and create the leaf building blocks.

        ClusterOptions targetCluster = GetTargetCluster(newRun);

        if (await GetCodespec(newRun.Job.Codespec, cancellationToken) is not JobCodespec jobCodespec)
        {
            throw new ArgumentException($"The codespec for the job is required to be a job codespec");
        }

        newRun = newRun with
        {
            Cluster = targetCluster.Name,
            Job = newRun.Job with
            {
                Codespec = jobCodespec.ToCodespecRef()
            }
        };

        var jobPodTemplateSpec = CreatePodTemplateSpec(jobCodespec, newRun.Job, targetCluster, newRun, "Never");

        V1PodTemplateSpec? workerPodTemplateSpec = null;
        WorkerCodespec? workerCodespec = null;
        if (newRun.Worker != null)
        {
            workerCodespec = await GetCodespec(newRun.Worker.Codespec, cancellationToken) as WorkerCodespec;
            if (workerCodespec == null)
            {
                throw new ArgumentException($"The codespec for the worker is required to be a worker codespec");
            }

            newRun = newRun with
            {
                Worker = newRun.Worker with
                {
                    Codespec = workerCodespec.ToCodespecRef()
                }
            };
            workerPodTemplateSpec = CreatePodTemplateSpec(workerCodespec, newRun.Worker, targetCluster, newRun, "Always");
        }

        if (newRun.Job.Buffers == null)
        {
            newRun = newRun with { Job = newRun.Job with { Buffers = [] } };
        }

        if (newRun.Job.Tags == null)
        {
            newRun = newRun with { Job = newRun.Job with { Tags = [] } };
        }

        var bufferMap = await GetBufferMap(jobCodespec.Buffers, newRun.Job.Buffers, newRun.Job.Tags, cancellationToken);

        // Phase 2: now that we have performed validation, create a record for this run in the database

        var run = await Repository.CreateRun(newRun, cancellationToken);

        // Phase 3: assemble and create Kubernetes objects

        var commonLabels = ImmutableDictionary<string, string>.Empty.Add(RunLabel, $"{run.Id}");

        var jobLabels = commonLabels.Add(JobLabel, $"{run.Id}");
        if (jobPodTemplateSpec.Metadata.Labels != null)
        {
            jobLabels = jobLabels.AddRange(jobPodTemplateSpec.Metadata.Labels);
        }

        jobPodTemplateSpec.Metadata.Labels = jobLabels;

        var job = new V1Job
        {
            Metadata = new()
            {
                Name = JobNameFromRunId(run.Id!.Value),
                Labels = jobLabels,
                Annotations = jobCodespec.Sockets?.Count > 0 ? new Dictionary<string, string>() { { HasSocketAnnotation, "true" } } : null
            },
            Spec = new()
            {
                Parallelism = run.Job.Replicas,
                Completions = run.Job.Replicas,
                CompletionMode = "Indexed",
                ManualSelector = true,
                Selector = new() { MatchLabels = jobLabels },
                Template = jobPodTemplateSpec,
                ActiveDeadlineSeconds = run.TimeoutSeconds,
                BackoffLimit = 0,
            },
        };

        if (bufferMap != null)
        {
            await AddBufferProxySidecars(job, run, bufferMap, jobCodespec, cancellationToken);
        }

        if (newRun.Worker != null)
        {
            var workerLabels = commonLabels.Add(WorkerLabel, $"{run.Id}");
            if (workerPodTemplateSpec!.Metadata.Labels != null)
            {
                workerLabels = workerLabels.AddRange(workerPodTemplateSpec.Metadata.Labels);
            }

            workerPodTemplateSpec!.Metadata.Labels = workerLabels;

            var workerStatefulSet = new V1StatefulSet()
            {
                Metadata = new()
                {
                    Name = StatefulSetNameFromRunId(run.Id.Value),
                    Labels = workerLabels
                },
                Spec = new()
                {
                    PodManagementPolicy = "Parallel",
                    Replicas = newRun.Worker.Replicas,
                    Template = workerPodTemplateSpec,
                    Selector = new() { MatchLabels = workerLabels },
                    ServiceName = StatefulSetNameFromRunId(run.Id.Value)
                },
            };

            AddWaitForWorkerInitContainersToJob(job, run);
            AddWorkerNodesEnvironmentVariables(job, run, workerCodespec);

            await _client.AppsV1.CreateNamespacedStatefulSetAsync(workerStatefulSet, _k8sOptions.Namespace, cancellationToken: cancellationToken);

            var headlessWorkerService = new V1Service
            {
                Metadata = new()
                {
                    Name = StatefulSetNameFromRunId(run.Id.Value),
                    Labels = workerLabels,
                },
                Spec = new()
                {
                    ClusterIP = "None",
                    Selector = workerLabels,
                }
            };

            await _client.CoreV1.CreateNamespacedServiceAsync(headlessWorkerService, _k8sOptions.Namespace, cancellationToken: cancellationToken);
        }

        await _client.BatchV1.CreateNamespacedJobAsync(job, _k8sOptions.Namespace, cancellationToken: cancellationToken);

        // Phase 4: Inform the database that the Kubernetes objects have been created in the cluster.

        await Repository.UpdateRun(run, resourcesCreated: true, cancellationToken: cancellationToken);
        _logger.CreatedRun(run.Id.Value);
        return run;
    }

    private void AddWaitForWorkerInitContainersToJob(V1Job job, Run run)
    {
        var initContainers = job.Spec.Template.Spec.InitContainers ??= [];

        initContainers.Add(
            new()
            {
                Name = "imagepull",
                Image = GetMainContainer(job.Spec.Template.Spec).Image,
                Command = s_waitForWorkerCommand,
                VolumeMounts = new V1VolumeMount[] { new("/no-op/", "no-op") }
            });

        var waitScript = new StringBuilder("set -euo pipefail").AppendLine();
        waitScript.AppendLine($"until kubectl wait --for=condition=ready pod -l {WorkerLabel}={run.Id}; do echo waiting for workers to be ready; sleep 1; done;");
        foreach (var host in GetWorkerDnsNames(run))
        {
            waitScript.AppendLine($"until getent hosts {host}; do echo waiting for hostname {host} to resolve; sleep 1; done;");
        }

        initContainers.Add(
            new()
            {
                Name = "waitforworker",
                Image = _k8sOptions.WorkerWaiterImage,
                Command = new[] { "bash", "-c", waitScript.ToString() },
            });

        (job.Spec.Template.Spec.Volumes ??= []).Add(new()
        {
            Name = "no-op",
            ConfigMap = new V1ConfigMapVolumeSource
            {
                DefaultMode = 111,
                Name = _k8sOptions.NoOpConfigMap
            }
        });
    }

    private void AddWorkerNodesEnvironmentVariables(V1Job job, Run run, WorkerCodespec? workerCodespec)
    {
        var dnsNames = GetWorkerDnsNames(run);

        var envVars = GetMainContainer(job.Spec.Template.Spec).Env ??= [];
        envVars.Add(new("TYGER_WORKER_NODES", JsonSerializer.Serialize(dnsNames)));
        if (workerCodespec?.Endpoints != null)
        {
            foreach ((var name, var port) in workerCodespec.Endpoints)
            {
                envVars.Add(new($"TYGER_{name.ToUpperInvariant()}_WORKER_ENDPOINT_ADDRESSES", JsonSerializer.Serialize(dnsNames.Select(n => $"{n}:{port}"))));
            }
        }
    }

    private string[] GetWorkerDnsNames(Run run)
    {
        return Enumerable.Range(0, run.Worker!.Replicas).Select(i => $"{StatefulSetNameFromRunId(run.Id!.Value)}-{i}.{StatefulSetNameFromRunId(run.Id.Value)}.{_k8sOptions.Namespace}.svc.cluster.local").ToArray();
    }

    private async Task AddBufferProxySidecars(V1Job job, Run run, Dictionary<string, (bool write, Uri sasUri)> bufferMap, JobCodespec codespec, CancellationToken cancellationToken)
    {
        const string SecretMountPath = "/etc/buffer-sas-tokens";
        const string FifoMountPath = "/etc/buffer-fifos";
        const string PipeVolumeName = "pipevolume";

        var mainContainer = GetMainContainer(job.Spec.Template.Spec);
        mainContainer.Env ??= [];

        IEnumerable<string> buffersNotUsedBySockets = bufferMap.Keys;
        if (codespec.Sockets?.Count > 0)
        {
            buffersNotUsedBySockets = bufferMap.Keys.Except(codespec.Sockets.SelectMany(s => new[] { s.InputBuffer!, s.OutputBuffer! }));
        }

        foreach (var envVar in buffersNotUsedBySockets.Select(p => new V1EnvVar($"{p.ToUpperInvariant()}_PIPE", $"{FifoMountPath}/{p}")))
        {
            mainContainer.Env.Add(envVar);
        }

        var buffersSecret = new V1Secret
        {
            Metadata = new()
            {
                Name = SecretNameFromRunId(run.Id!.Value),
                Labels = job.Labels() ?? throw new InvalidOperationException("expected job labels to be set"),
            },
            StringData = bufferMap.ToDictionary(p => p.Key, p => p.Value.sasUri.ToString()),
        };

        (job.Spec.Template.Spec.Volumes ??= []).Add(
            new()
            {
                Name = "buffers",
                Secret = new() { SecretName = buffersSecret.Metadata.Name },
            });

        job.Spec.Template.Spec.Volumes.Add(new() { Name = PipeVolumeName, EmptyDir = new() });

        var fifoVolumeMount = new V1VolumeMount(FifoMountPath, PipeVolumeName);
        (mainContainer.VolumeMounts ??= []).Add(fifoVolumeMount);

        var mkfifoBuilder = new StringBuilder("set -euo pipefail").AppendLine();
        foreach (var buffer in bufferMap)
        {
            var fifoPath = $"{FifoMountPath}/{buffer.Key}";
            mkfifoBuilder.AppendLine($"mkfifo {fifoPath}").AppendLine($"chmod 666 {fifoPath}");
        }

        (job.Spec.Template.Spec.InitContainers ??= []).Add(
            new()
            {
                Name = "mkfifo",
                Image = "mcr.microsoft.com/cbl-mariner/base/core:2.0-nonroot",
                Command = new[] { "bash", "-c", mkfifoBuilder.ToString() },
                VolumeMounts = new[] { fifoVolumeMount }
            }
        );

        foreach ((string bufferName, (bool write, Uri sasUri)) in bufferMap)
        {
            job.Spec.Template.Spec.Containers.Add(new()
            {
                Name = $"{bufferName}-buffer-sidecar",
                Image = _bufferOptions.BufferSidecarImage,
                Args =
                [
                    write ? "output" : "input",
                    $"{SecretMountPath}/{bufferName}",
                    write ? "--input" : "--output",
                    $"{FifoMountPath}/{bufferName}",
                    "--namespace",
                    _k8sOptions.Namespace,
                    "--pod",
                    "$(POD_NAME)",
                    "--container",
                    "main",
                    "--log-format",
                    "json",
                ],
                VolumeMounts =
                [
                    fifoVolumeMount,
                    new()
                    {
                        Name = "buffers",
                        MountPath = SecretMountPath,
                        ReadOnlyProperty = true,
                    },
                ],
                Env =
                [
                    new V1EnvVar("POD_NAME", valueFrom: new V1EnvVarSource(fieldRef: new V1ObjectFieldSelector("metadata.name"))),
                ],
            });
        }

        if (codespec.Sockets != null)
        {
            foreach (var socket in codespec.Sockets)
            {
                job.Spec.Template.Spec.Containers.Add(new()
                {
                    Name = $"socket-{socket.Port}-sidecar",
                    Image = _bufferOptions.BufferSidecarImage,
                    Args =
                    [
                        "socket-adapt",
                        "--address",
                        $"localhost:{socket.Port}",
                        "--input",
                        string.IsNullOrEmpty(socket.InputBuffer) ? "" : $"{FifoMountPath}/{socket.InputBuffer}",
                        "--output",
                        string.IsNullOrEmpty(socket.OutputBuffer) ? "" : $"{FifoMountPath}/{socket.OutputBuffer}",
                        "--namespace",
                        _k8sOptions.Namespace,
                        "--pod",
                        "$(POD_NAME)",
                        "--container",
                        "main",
                        "--log-format",
                        "json",
                    ],
                    VolumeMounts =
                    [
                        fifoVolumeMount,
                    ],
                    Env =
                    [
                        new V1EnvVar("POD_NAME", valueFrom: new V1EnvVarSource(fieldRef: new V1ObjectFieldSelector("metadata.name"))),
                    ],
                });
            }
        }

        await _client.CoreV1.CreateNamespacedSecretAsync(buffersSecret, _k8sOptions.Namespace, cancellationToken: cancellationToken);
        _logger.CreatedSecret(buffersSecret.Metadata.Name);
    }

    private static V1Container GetMainContainer(V1PodSpec podSpec) => podSpec.Containers.Single(c => c.Name == "main");

    private ClusterOptions GetTargetCluster(Run newRun)
    {
        ClusterOptions? targetCluster;
        if (!string.IsNullOrEmpty(newRun.Cluster))
        {
            targetCluster = _k8sOptions.Clusters.FirstOrDefault(c => string.Equals(c.Name, newRun.Cluster, StringComparison.OrdinalIgnoreCase));
            if (targetCluster == null)
            {
                var options = string.Join(", ", _k8sOptions.Clusters.Select(c => $"'{c.Name}'"));
                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Unknown cluster '{0}'. Valid options are: {1}.", newRun.Cluster, options));
            }
        }
        else
        {
            // Only supporting single cluster for the moment
            targetCluster = _k8sOptions.Clusters.First();
        }

        return targetCluster;
    }

    private V1PodTemplateSpec CreatePodTemplateSpec(Codespec codespec, RunCodeTarget codeTarget, ClusterOptions? targetCluster, Run run, string restartPolicy)
    {
        string? GetServiceAccount()
        {
            var identities = _k8sOptions.CustomIdentities;
            if (!string.IsNullOrEmpty(codespec.Identity))
            {
                if (run.Kind == RunKind.System)
                {
                    return codespec.Identity;
                }

                if (identities?.TryGetValue(codespec.Identity, out var serviceAccount) == true)
                {
                    return serviceAccount;
                }

                if (identities is null or { Count: 0 })
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Identity '{0}' is not supported.", codespec.Identity));
                }

                var options = string.Join(", ", identities.Keys.Select(c => $"'{c}'"));
                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Unknown identity '{0}'. Valid options are: {1}.", codespec.Identity, options));
            }

            return codespec is JobCodespec ? _k8sOptions.JobServiceAccount : null;
        }

        var podTemplateSpec = new V1PodTemplateSpec()
        {
            Metadata = new()
            {
                Finalizers = [FinalizerName],
                Labels = new Dictionary<string, string>
                {
                    { "azure.workload.identity/use", (!string.IsNullOrEmpty(codespec.Identity)).ToString().ToLowerInvariant() }
                },
            },
            Spec = new()
            {
                Containers =
                [
                    new()
                    {
                        Name = "main",
                        Image = codespec.Image,
                        Command = codespec.Command?.ToArray(),
                        Args = codespec.Args?.ToArray(),
                        Env = [new V1EnvVar("TYGER_RUN_ID", valueFrom: new V1EnvVarSource(fieldRef: new V1ObjectFieldSelector($"metadata.labels['{RunLabel}']")))],
                    }
                ],
                RestartPolicy = restartPolicy,
                ServiceAccountName = GetServiceAccount(),
            }
        };

        if (codespec.Env != null)
        {
            foreach (var (key, value) in codespec.Env)
            {
                podTemplateSpec.Spec.Containers[0].Env.Add(new(key, value));
            }
        }

        AddComputeResources(podTemplateSpec, codespec, codeTarget, targetCluster);

        return podTemplateSpec;
    }

    private static void AddComputeResources(V1PodTemplateSpec podTemplateSpec, Codespec codespec, RunCodeTarget codeTarget, ClusterOptions? targetCluster)
    {
        string? targetNodePool = null;
        bool targetsGpuNodePool = false;
        if (!string.IsNullOrEmpty(codeTarget.NodePool))
        {
            if (targetCluster == null)
            {
                throw new ValidationException("A cluster must be specified if a nodepool is specified.");
            }

            targetNodePool = codeTarget.NodePool;
            NodePoolOptions? pool;
            if ((pool = targetCluster.UserNodePools.FirstOrDefault(np => string.Equals(np.Name, codeTarget.NodePool, StringComparison.OrdinalIgnoreCase))) == null)
            {
                var options = string.Join(", ", targetCluster.UserNodePools.Select(np => $"'{np.Name}'"));
                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Unknown nodepool '{0}'. Valid options are: {1}.", targetNodePool, options));
            }

            targetsGpuNodePool = DoesVmHaveSupportedGpu(pool.VmSize);

            if (!targetsGpuNodePool && codespec.Resources?.Gpu is ResourceQuantity q && q.ToDecimal() != 0)
            {
                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Nodepool '{0}' does not have GPUs and cannot satisfy GPU request '{1}'", targetNodePool, q));
            }
        }

        if (codespec.Resources != null)
        {
            Dictionary<string, ResourceQuantity> requests = ToDictionary(codespec.Resources.Requests);
            Dictionary<string, ResourceQuantity> limits = ToDictionary(codespec.Resources.Limits);

            if (codespec.Resources.Gpu != null)
            {
                requests["nvidia.com/gpu"] = limits["nvidia.com/gpu"] = codespec.Resources.Gpu;
            }

            GetMainContainer(podTemplateSpec.Spec).Resources = new() { Requests = requests, Limits = limits };
        }

        podTemplateSpec.Spec.Tolerations =
            [
                new() { Key = "tyger", OperatorProperty = "Equal", Value = "run", Effect = "NoSchedule" } // allow this to run on a user nodepools
            ];
        if (codespec.Resources?.Gpu != null || targetsGpuNodePool)
        {
            podTemplateSpec.Spec.Tolerations.Add(new() { Key = "sku", OperatorProperty = "Equal", Value = "gpu", Effect = "NoSchedule" });
        }

        podTemplateSpec.Spec.NodeSelector = new Dictionary<string, string> { { "tyger", "run" } }; // require this to run on a user nodepool
        if (targetNodePool != null)
        {
            podTemplateSpec.Spec.NodeSelector.Add("agentpool", targetNodePool);
        }
    }

    private static Dictionary<string, ResourceQuantity> ToDictionary(OvercommittableResources? resources)
    {
        var dict = new Dictionary<string, ResourceQuantity>();
        if (resources?.Cpu != null)
        {
            dict["cpu"] = resources.Cpu;
        }

        if (resources?.Memory != null)
        {
            dict["memory"] = resources.Memory;
        }

        return dict;
    }

    private static bool DoesVmHaveSupportedGpu(string vmSize)
    {
        return vmSize.StartsWith("Standard_N", StringComparison.OrdinalIgnoreCase) &&
            !vmSize.EndsWith("_v4", StringComparison.OrdinalIgnoreCase); // unsupported AMD GPU
    }
}
