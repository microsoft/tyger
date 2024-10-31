// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Text.RegularExpressions;
using k8s.Models;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public static partial class KubernetesMetadata
{
    public const string RunLabel = "tyger-run";
    public const string JobLabel = "tyger-job";
    public const string WorkerLabel = "tyger-worker";
    public const string HasSocketAnnotation = "tyger-has-socket";
    public const string JobReplicaCountAnnotation = "tyger-job-replica-count";
    public const string WorkerReplicaCountAnnotation = "tyger-worker-replica-count";

    public static string JobPodName(long id, int index) => $"run-{id}-job-{index}";
    public static string WorkerPodName(long id, int index) => $"run-{id}-worker-{index}";
    public static string JobNameFromRunId(long id) => $"run-{id}-job";
    public static string SecretNameFromRunId(long id) => JobNameFromRunId(id);
    public static string StatefulSetNameFromRunId(long id) => $"run-{id}-worker";
    public static string ServiceNameFromRunId(long id) => StatefulSetNameFromRunId(id);

    public static string RemoveRunPrefix(string podName)
    {
        int lastDashIndex = podName.AsSpan()[4..].IndexOf('-') + 4;
        return podName[(lastDashIndex + 1)..];
    }

    public static bool IsWorkerPodName(string podName) => IsWorkerPodNameRegex().IsMatch(podName);

    public static int IndexFromPodName(string podName)
    {
        var lastDashIndex = podName.LastIndexOf('-');
        return int.Parse(podName.AsSpan()[(lastDashIndex + 1)..]);
    }

    public static (int jobReplicaCount, int workerReplicaCount) GetReplicaCounts(this V1Pod pod)
    {
        if (pod.GetAnnotation(JobReplicaCountAnnotation) is string jobReplicaCountStr && int.TryParse(jobReplicaCountStr, out var jobReplicaCount))
        {
            if (pod.GetAnnotation(WorkerReplicaCountAnnotation) is string workerReplicaCountStr && int.TryParse(workerReplicaCountStr, out var workerReplicaCount))
            {
                return (jobReplicaCount, workerReplicaCount);
            }

            throw new InvalidOperationException($"Pod {pod.Metadata.Name} is missing the {WorkerReplicaCountAnnotation} annotation.");
        }

        throw new InvalidOperationException($"Pod {pod.Metadata.Name} is missing the {JobReplicaCountAnnotation} annotation.");
    }

    [GeneratedRegex(@"^run-(\d+)-worker-(\d+)$")]
    private static partial Regex IsWorkerPodNameRegex();
}
