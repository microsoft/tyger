// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.Server.Compute.Kubernetes;

public static class KubernetesMetadata
{
    public const string FinalizerName = "research.microsoft.com/tyger-finalizer";

    public const string RunLabel = "tyger-run";
    public const string JobLabel = "tyger-job";
    public const string WorkerLabel = "tyger-worker";

    public static string JobNameFromRunId(long id) => $"run-{id}-job";
    public static string SecretNameFromRunId(long id) => JobNameFromRunId(id);
    public static string StatefulSetNameFromRunId(long id) => $"run-{id}-worker";
}
