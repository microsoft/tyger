// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Collections.Immutable;
using System.Text.RegularExpressions;
using k8s.Models;
using Tyger.ControlPlane.Model;
using static Tyger.ControlPlane.Compute.Kubernetes.KubernetesMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

/// <summary>
/// The Kubernetes resources associated with a run.
/// </summary>
public sealed partial class RunObjects
{
    // ErrImagePullBackOff - Container image pull failed, kubelet is backing off image pull
    private const string ErrImagePullBackOff = "ImagePullBackOff";

    // ErrImageInspect - Unable to inspect image
    private const string ErrImageInspect = "ImageInspectError";

    // ErrImagePull - General image pull error
    private const string ErrImagePull = "ErrImagePull";

    // ErrImageNeverPull - Required Image is absent on host and PullPolicy is NeverPullImage
    private const string ErrImageNeverPull = "ErrImageNeverPull";

    // ErrInvalidImageName - Unable to parse the image name.
    private const string ErrInvalidImageName = "InvalidImageName";

    // ErrRegistryUnavailable - Get http error on the PullImage RPC call.
    private const string ErrRegistryUnavailable = "RegistryUnavailable";

    // ErrSignatureValidationFailed - Unable to validate the image signature on the PullImage RPC call.
    private const string ErrSignatureValidationFailed = "SignatureValidationFailed";

    private const string KubernetesImagePullThrottlingMessage = "pull QPS exceeded";

    private static readonly HashSet<string> s_imagePullErrorCodes = [ErrImagePullBackOff, ErrImageInspect, ErrImagePull, ErrImageNeverPull, ErrInvalidImageName, ErrRegistryUnavailable, ErrSignatureValidationFailed];
    private static readonly HashSet<string> s_fatalImagePullErrorCodes = [ErrImageNeverPull, ErrInvalidImageName, ErrSignatureValidationFailed];

    private static readonly TimeSpan s_userImagePullBackoffTimeout = TimeSpan.FromMinutes(1);
    private static readonly TimeSpan s_systemImagePullBackoffTimeout = TimeSpan.FromMinutes(30);

    public RunObjects(long id, int jobReplicas, int workerReplicas)
    {
        Id = id;
        JobPods = new V1Pod[jobReplicas];
        WorkerPods = workerReplicas > 0 ? new V1Pod[workerReplicas] : [];
    }

    public long Id { get; private init; }

    public V1Pod?[] JobPods { get; private init; }
    public V1Pod?[] WorkerPods { get; private init; }

    public ObservedRunState CachedMetadata { get; private set; }

    public RunObjects Clone()
    {
        return new(Id, JobPods.Length, WorkerPods.Length)
        {
            JobPods = [.. JobPods],
            WorkerPods = WorkerPods.Length == 0 ? WorkerPods : [.. WorkerPods],
            CachedMetadata = CachedMetadata
        };
    }

    public ObservedRunState GetObservedState()
    {
        var metadata = GetStatus();
        var (jobNodePool, workerNodePool) = GetNodePools();
        if (jobNodePool != null || workerNodePool != null)
        {
            metadata = metadata with { JobNodePool = jobNodePool, WorkerNodePool = workerNodePool };
        }

        return CachedMetadata = metadata;
    }

    public void ClearCachedMetadata()
    {
        CachedMetadata = default;
    }

    private ObservedRunState GetStatus()
    {
        var startedAt = GetStartedTime();

        if (GetFailureTimeAndReason() is (var failureTime, var reason))
        {
            return new(Id, RunStatus.Failed, JobPods.Length, WorkerPods.Length)
            {
                StatusReason = reason,
                StartedAt = startedAt,
                FinishedAt = failureTime,
            };
        }

        if (GetSuccessTime() is DateTimeOffset successTime)
        {
            return new(Id, RunStatus.Succeeded, JobPods.Length, WorkerPods.Length)
            {
                StartedAt = startedAt,
                FinishedAt = successTime,
            };
        }

        var runningCount = JobPods.Count(p => p?.Status?.Phase == "Running");

        if (runningCount > 0)
        {
            return new(Id, RunStatus.Running, JobPods.Length, WorkerPods.Length) { StartedAt = startedAt, RunningCount = runningCount };
        }

        return new(Id, RunStatus.Pending, JobPods.Length, WorkerPods.Length) { StartedAt = startedAt };
    }

    private (DateTimeOffset?, string)? GetFailureTimeAndReason()
    {
        var fallbackTime = DateTimeOffset.UtcNow;
        var failedContainerStatus = JobPods
            .Where(p => p?.Status?.ContainerStatuses != null)
            .SelectMany(p => p!.Status.ContainerStatuses)
            .Where(cs => cs.State?.Terminated?.ExitCode is not null and not 0)
            .MinBy(cs => cs.State.Terminated.FinishedAt ?? fallbackTime); // sometimes FinishedAt is null https://github.com/kubernetes/kubernetes/issues/104107

        if (failedContainerStatus != null)
        {
            var reason = $"{(failedContainerStatus.Name == MainContainerName ? "Main" : "Sidecar")} exited with code {failedContainerStatus.State.Terminated.ExitCode}";
            return (failedContainerStatus.State.Terminated.FinishedAt ?? fallbackTime, reason);
        }

        // Recognize other failure reasons, such as the pod being evicted.
        V1Pod? failedPod = JobPods.FirstOrDefault(p => p?.Status?.Phase == "Failed");
        if (failedPod != null)
        {
            return (failedPod.Status.Reason, failedPod.Status.Message) switch
            {
                ("" or null, "" or null) => (fallbackTime, "Failed"),
                ("" or null, var message) => (fallbackTime, message),
                (var reason, "" or null) => (fallbackTime, reason),
                (var reason, var message) => (fallbackTime, $"{reason}: {message}"),
            };
        }

        static (DateTimeOffset?, string)? PullFailure(V1ContainerStatus status)
        {
            var waiting = status.State.Waiting!;
            if (string.IsNullOrEmpty(waiting.Message))
            {
                return (null, $"Failed to pull image '{status.Image}': {waiting.Reason}");
            }

            return (null, $"Failed to pull image '{status.Image}': {waiting.Reason}: {waiting.Message}");
        }

        foreach (var pod in JobPods.Concat(WorkerPods))
        {
            if (pod?.Status is null)
            {
                continue;
            }

            if (pod.Status.InitContainerStatuses != null)
            {
                foreach (var initContainerStatus in pod.Status.InitContainerStatuses)
                {
                    if (initContainerStatus.State.Waiting is not null && s_imagePullErrorCodes.Contains(initContainerStatus.State.Waiting.Reason))
                    {
                        if (s_fatalImagePullErrorCodes.Contains(initContainerStatus.State.Waiting.Reason))
                        {
                            return PullFailure(initContainerStatus);
                        }

                        if (initContainerStatus.State.Waiting.Message?.Contains(KubernetesImagePullThrottlingMessage) == true)
                        {
                            continue; // continue retrying
                        }

                        var timeout = initContainerStatus.Name is ImagePullInitContainerName
                            ? s_userImagePullBackoffTimeout
                            : s_systemImagePullBackoffTimeout;

                        var podScheduledTime = pod.Status.Conditions.FirstOrDefault(c => c.Type == "PodScheduled" && c.Status == "True")?.LastTransitionTime;
                        if (podScheduledTime == null ||
                            podScheduledTime + timeout < DateTimeOffset.UtcNow)
                        {
                            return PullFailure(initContainerStatus);
                        }
                    }
                }
            }

            if (pod.Status.ContainerStatuses != null)
            {
                foreach (var containerStatus in pod.Status.ContainerStatuses)
                {
                    if (containerStatus.State.Waiting is not null && s_imagePullErrorCodes.Contains(containerStatus.State.Waiting.Reason))
                    {
                        if (s_fatalImagePullErrorCodes.Contains(containerStatus.State.Waiting.Reason))
                        {
                            return PullFailure(containerStatus);
                        }

                        if (containerStatus.State.Waiting.Message?.Contains(KubernetesImagePullThrottlingMessage) == true)
                        {
                            continue; // continue retrying
                        }

                        var timeout = containerStatus.Name is MainContainerName
                            ? s_userImagePullBackoffTimeout
                            : s_systemImagePullBackoffTimeout;

                        var podScheduledTime = pod.Status.Conditions.FirstOrDefault(c => c.Type == "Initialized" && c.Status == "True")?.LastTransitionTime;
                        if (podScheduledTime == null ||
                            podScheduledTime + timeout < DateTimeOffset.UtcNow)
                        {
                            return PullFailure(containerStatus);
                        }
                    }
                }
            }
        }

        return null;
    }

    private DateTimeOffset? GetStartedTime()
    {
        DateTimeOffset? startedTime = null;
        foreach (var pod in JobPods)
        {
            if (pod?.Status?.ContainerStatuses == null)
            {
                continue;
            }

            foreach (var containerStatus in pod.Status.ContainerStatuses)
            {
                if (containerStatus.Name != MainContainerName)
                {
                    continue;
                }

                var t = containerStatus.State?.Running?.StartedAt ?? containerStatus.State?.Terminated?.StartedAt;
                if (t != null && (startedTime == null || t < startedTime))
                {
                    startedTime = t;
                }
            }
        }

        return startedTime;
    }

    private DateTimeOffset? GetSuccessTime()
    {
        // the main container may still be running if using a socket but if all sidecars have exited successfully, then we consider it complete.
        if (JobPods.All(pod =>
                pod != null &&
                pod.Spec.Containers.All(c =>
                        pod.Status?.ContainerStatuses?.Any(cs =>
                            cs.Name == c.Name &&
                            (cs.State.Terminated?.ExitCode == 0 ||
                            (cs.Name == MainContainerName && pod.GetAnnotation(HasSocketAnnotation) == "true" && cs.State.Running != null))) == true)))
        {
            var finishedTime = JobPods.SelectMany(p => p!.Status.ContainerStatuses).Select(cs => cs.State.Terminated?.FinishedAt).Where(t => t != null).Max();
            return finishedTime ?? GetStartedTime();
        }

        return null;
    }

    private (string? jobNodePool, string? workerNodePool) GetNodePools()
    {
        static string GetNodePoolFromNodeName(string nodeName)
        {
            var match = NodePoolFromNodeNameRegex().Match(nodeName);
            if (!match.Success)
            {
                throw new InvalidOperationException($"Node name in unexpected format: '{nodeName}'");
            }

            return match.Groups[1].Value;
        }

        static string? GetNodePool(ICollection<V1Pod?> pods)
        {
            object? res = null;
            foreach (var pod in pods)
            {
                if (pod?.Spec.NodeName is { } nodeName)
                {
                    if (GetNodePoolFromNodeName(nodeName) is { } nodePool)
                    {
                        res = res switch
                        {
                            null => nodePool,
                            string existingNodePool when existingNodePool == nodePool => existingNodePool,
                            string existingNodePool => ImmutableSortedSet.Create(existingNodePool, nodePool),
                            ImmutableSortedSet<string> set => set.Add(nodePool),
                            _ => throw new InvalidOperationException($"Unexpected value for node pool: {res}"),
                        };
                    }
                }
            }

            return res switch
            {
                null => null,
                string singleNodePool => singleNodePool,
                ImmutableSortedSet<string> set => string.Join(",", set),
                _ => throw new InvalidOperationException($"Unexpected value for node pool: {res}"),
            };
        }

        var jobNodePool = GetNodePool(JobPods);
        var workerNodePool = WorkerPods != null ? GetNodePool(WorkerPods) : null;

        return (jobNodePool, workerNodePool);
    }

    // Used to extract "gpunp" from an AKS node named "aks-gpunp-23329378-vmss000007"
    [GeneratedRegex(@"^aks-([^\-]+)-", RegexOptions.Compiled)]
    private static partial Regex NodePoolFromNodeNameRegex();
}
