// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

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
        if (GetFailureTimeAndReason() is (var failureTime, var reason))
        {
            return new(Id, RunStatus.Failed, JobPods.Length, WorkerPods.Length)
            {
                StatusReason = reason,
                FinishedAt = failureTime,
            };
        }

        if (GetSuccessTime() is DateTimeOffset successTime)
        {
            return new(Id, RunStatus.Succeeded, JobPods.Length, WorkerPods.Length)
            {
                FinishedAt = successTime,
            };
        }

        foreach (var pod in JobPods.Concat(WorkerPods))
        {
            if (pod == null)
            {
                continue;
            }

            if (pod.Status?.Phase == "Pending")
            {
                var containerStatuses = (pod.Status.ContainerStatuses ??= Array.Empty<V1ContainerStatus>()).Concat(pod.Status.InitContainerStatuses ?? Array.Empty<V1ContainerStatus>());

                var pullErrorStatus = containerStatuses.FirstOrDefault(cs => cs.State.Waiting?.Reason == "ImagePullBackOff");
                if (pullErrorStatus != null)
                {
                    return new(Id, RunStatus.Failed, JobPods.Length, WorkerPods.Length)
                    {
                        FinishedAt = pod.Status.StartTime ?? pod.Metadata.CreationTimestamp ?? DateTimeOffset.UtcNow,
                        StatusReason = $"{pullErrorStatus.State.Waiting.Reason}: {pullErrorStatus.State.Waiting.Message}",
                    };
                }
            }
        }

        var runningCount = JobPods.Count(p => p?.Status?.Phase == "Running");

        if (runningCount > 0)
        {
            return new(Id, RunStatus.Running, JobPods.Length, WorkerPods.Length) { RunningCount = runningCount };
        }

        return new(Id, RunStatus.Pending, JobPods.Length, WorkerPods.Length);
    }

    private (DateTimeOffset, string)? GetFailureTimeAndReason()
    {
        var containerStatus = JobPods
            .Where(p => p?.Status?.ContainerStatuses != null)
            .SelectMany(p => p!.Status.ContainerStatuses)
            .Where(cs => cs.State?.Terminated?.ExitCode is not null and not 0)
            .MinBy(cs => cs.State.Terminated.FinishedAt ?? DateTimeOffset.MaxValue);

        if (containerStatus != null)
        {
            var reason = $"{(containerStatus.Name == "main" ? "Main" : "Sidecar")} exited with code {containerStatus.State.Terminated.ExitCode}";
            return (containerStatus.State.Terminated.FinishedAt!.Value, reason);
        }

        return null;
    }

    private DateTimeOffset? GetStartedTime()
    {
        DateTimeOffset? startedTime = null;
        foreach (var pod in JobPods)
        {
            if (pod == null)
            {
                continue;
            }

            foreach (var containerStatus in pod.Status.ContainerStatuses)
            {
                if (containerStatus.Name != "main")
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
                         (cs.Name == "main" && pod.GetAnnotation(HasSocketAnnotation) == "true" && cs.State.Running != null))) == true)))
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

        static string GetNodePool(ICollection<V1Pod?> pods)
        {
            return string.Join(
                ",",
                pods.Select(p => p?.Spec.NodeName).Where(n => !string.IsNullOrEmpty(n)).Select(n => GetNodePoolFromNodeName(n!)).Distinct());
        }

        var jobNodePool = GetNodePool(JobPods);
        var workerNodePool = WorkerPods != null ? GetNodePool(WorkerPods) : null;

        return (jobNodePool, workerNodePool);
    }

    // Used to extract "gpunp" from an AKS node named "aks-gpunp-23329378-vmss000007"
    [GeneratedRegex(@"^aks-([^\-]+)-", RegexOptions.Compiled)]
    private static partial Regex NodePoolFromNodeNameRegex();
}
