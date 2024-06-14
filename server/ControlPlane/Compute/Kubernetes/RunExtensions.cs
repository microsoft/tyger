// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Diagnostics;
using System.Globalization;
using System.Net;
using System.Text.RegularExpressions;
using k8s;
using k8s.Autorest;
using k8s.Models;
using Tyger.ControlPlane.Model;
using static Tyger.ControlPlane.Compute.Kubernetes.KubernetesMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public static partial class RunExtensions
{
    public static ValueTask<Run> GetUpdatedRun(this Run run, IKubernetes client, KubernetesCoreOptions options, CancellationToken cancellationToken, V1Job? job = null, IReadOnlyList<V1Pod>? pods = null)
    {
        return new RunResources(run, client, options, job, pods).GetUpdatedRun(cancellationToken);
    }

    public static ValueTask<Run> GetPartiallyUpdatedRun(this Run run, IKubernetes client, KubernetesCoreOptions options, CancellationToken cancellationToken, V1Job? job = null, IReadOnlyList<V1Pod>? pods = null)
    {
        return new RunResources(run, client, options, job, pods).GetPartiallyUpdatedRun(cancellationToken);
    }

    private sealed partial class RunResources
    {
        private Run _run;
        private readonly IKubernetes _client;
        private readonly KubernetesCoreOptions _k8sOptions;

        private V1Job? _job;
        private IReadOnlyList<V1Pod>? _jobPods;
        private IReadOnlyList<V1Pod>? _workerPods;

        public RunResources(Run run, IKubernetes client, KubernetesCoreOptions options, V1Job? job = null, IReadOnlyList<V1Pod>? pods = null)
        {
            _run = run;
            _client = client;
            _k8sOptions = options;
            _job = job;
            if (pods != null)
            {
                (_jobPods, _workerPods) = PartitionPods(pods);
            }
        }

        public async ValueTask<Run> GetUpdatedRun(CancellationToken cancellationToken)
        {
            return await GetUpdatedRun(true, cancellationToken);
        }

        public async ValueTask<Run> GetPartiallyUpdatedRun(CancellationToken cancellationToken)
        {
            return await GetUpdatedRun(false, cancellationToken);
        }

        private async ValueTask<Run> GetUpdatedRun(bool precise, CancellationToken cancellationToken)
        {
            _run = await UpdateStatus(precise, cancellationToken);
            if (precise)
            {
                _run = await UpdateNodePools(cancellationToken);
            }

            return _run;
        }

        private async ValueTask<V1Job?> GetJob(CancellationToken cancellationToken)
        {
            if (_job != null)
            {
                return _job;
            }

            try
            {
                return _job = await _client.BatchV1.ReadNamespacedJobAsync(JobNameFromRunId(_run.Id!.Value), _k8sOptions.Namespace, cancellationToken: cancellationToken);
            }
            catch (HttpOperationException e) when (e.Response.StatusCode == HttpStatusCode.NotFound)
            {
                return null;
            }
        }

        private async ValueTask<(IReadOnlyList<V1Pod> jobPods, IReadOnlyList<V1Pod> workerPods)> GetPods(CancellationToken cancellationToken)
        {
            if (_jobPods != null && _workerPods != null)
            {
                return (_jobPods, _workerPods);
            }

            var pods = await _client.EnumeratePodsInNamespace(_k8sOptions.Namespace, labelSelector: $"{RunLabel}={_run.Id!.Value}", cancellationToken: cancellationToken)
                .ToListAsync(cancellationToken);

            return (_jobPods, _workerPods) = PartitionPods(pods);
        }

        private (IReadOnlyList<V1Pod> jobPods, IReadOnlyList<V1Pod> workerPods) PartitionPods(IReadOnlyList<V1Pod> pods)
        {
            if (_run.Worker == null)
            {
                _jobPods = pods.Where(p => p.GetLabel(JobLabel) != null).ToList();
                _workerPods = [];
                return (_jobPods, _workerPods);
            }

            var jobPods = new List<V1Pod>();
            var workerPods = new List<V1Pod>();
            foreach (var pod in pods)
            {
                if (pod.GetLabel(JobLabel) != null)
                {
                    jobPods.Add(pod);
                }
                else if (pod.GetLabel(WorkerLabel) != null)
                {
                    workerPods.Add(pod);
                }
                else
                {
                    throw new InvalidOperationException($"Pod {pod.Metadata.Name} has neither {JobLabel} nor {WorkerLabel} label");
                }
            }

            return (jobPods, workerPods);
        }

        private async ValueTask<Run> UpdateStatus(bool precise, CancellationToken cancellationToken)
        {
            var job = await GetJob(cancellationToken);
            if (job == null)
            {
                return _run = _run with
                {
                    Status = RunStatus.Failed,
                    StatusReason = "Resources not found",
                    FinishedAt = _run.CreatedAt,
                    RunningCount = null
                };
            }

            var isCanceling = _job.GetAnnotation("Status") == "Canceling";

            if (await GetFailureTimeAndReason(cancellationToken) is (var failureTime, var reason))
            {
                if (isCanceling)
                {
                    return _run = _run with
                    {
                        Status = RunStatus.Canceled,
                        StatusReason = "Canceled by user",
                        FinishedAt = failureTime,
                        RunningCount = null
                    };
                }

                return _run = _run with
                {
                    Status = RunStatus.Failed,
                    StatusReason = reason,
                    FinishedAt = failureTime,
                    RunningCount = null
                };
            }

            if (await GetSuccessTime(precise, cancellationToken) is DateTimeOffset successTime)
            {
                return _run = _run with
                {
                    Status = RunStatus.Succeeded,
                    FinishedAt = successTime,
                    StatusReason = null,
                    RunningCount = null
                };
            }

            if (!precise)
            {
                return _run = _run with { Status = isCanceling ? RunStatus.Canceling : RunStatus.Pending };
            }

            (var jobPods, _) = await GetPods(cancellationToken);

            var runningCount = jobPods.Count(p => p.Status.Phase == "Running");

            if (isCanceling)
            {
                return _run = _run with
                {
                    Status = RunStatus.Canceling,
                    RunningCount = runningCount
                };
            }

            // Note that the job object may not yet reflect the status of the pods.
            // It could be that pods have succeeeded or failed without the job reflecting this.
            // We want to avoid returning a pending state if no pods are running because they have
            // all exited but the job hasn't been updated yet.
            var isRunning = jobPods.Any(p => p.Status.Phase is "Running" or "Succeeded" or "Failed");
            if (isRunning)
            {
                return _run = _run with
                {
                    Status = RunStatus.Running,
                    RunningCount = runningCount
                };
            }

            return _run = _run with { Status = RunStatus.Pending };
        }

        private async ValueTask<(DateTimeOffset, string)?> GetFailureTimeAndReason(CancellationToken cancellationToken)
        {
            Debug.Assert(_job != null, "_job should have been loaded");

            var failureCondition = _job.Status.Conditions?.FirstOrDefault(c => c.Type == "Failed" && c.Status == "True");
            if (failureCondition != null)
            {
                return (failureCondition.LastTransitionTime!.Value, failureCondition.Reason);
            }

            if (_job.GetAnnotation(HasSocketAnnotation) == "true")
            {
                (var jobPods, _) = await GetPods(cancellationToken);
                var containerStatus = jobPods
                    .SelectMany(p => p.Status!.ContainerStatuses)
                    .Where(cs => cs.State?.Terminated?.ExitCode is not null and not 0)
                    .MinBy(cs => cs.State.Terminated.FinishedAt!.Value);

                if (containerStatus != null)
                {
                    var reason = $"{(containerStatus.Name == "main" ? "Main" : "Sidecar")} exited with code {containerStatus.State.Terminated.ExitCode}";
                    return (containerStatus.State.Terminated.FinishedAt!.Value, reason);
                }
            }

            return null;
        }

        private async ValueTask<DateTimeOffset?> GetSuccessTime(bool precise, CancellationToken cancellationToken)
        {
            Debug.Assert(_job != null, "_job should have been loaded");
            bool succeeeded = false;
            if (_job.Status.Conditions?.Any(c => c.Type == "Complete" && c.Status == "True") == true)
            {
                succeeeded = true;
            }
            else if (precise)
            {
                (var jobPods, _) = await GetPods(cancellationToken);
                succeeeded = Enumerable.Range(0, _run.Job.Replicas)
                    .GroupJoin(jobPods, i => i, GetJobCompletionIndex, (i, p) => (i, p))
                    .All(g => g.p.Any(p => p.Status.Phase == "Succeeded"));
            }

            if (succeeeded)
            {
                if (!precise)
                {
                    return _run.CreatedAt;
                }

                (var jobPods, _) = await GetPods(cancellationToken);
                var finishedTime = jobPods
                        .Where(p => p.Status.Phase == "Succeeded")
                        .Select(p => p.Status.ContainerStatuses.Single(c => c.Name == "main").State.Terminated?.FinishedAt)
                        .Where(t => t != null)
                        .Max();

                return finishedTime ?? _run.CreatedAt;
            }

            if (_job.GetAnnotation(HasSocketAnnotation) == "true")
            {
                (var jobPods, _) = await GetPods(cancellationToken);

                // the main container may still be running but if all sidecars have exited successfully, then we consider it complete.
                if (jobPods.All(pod =>
                        pod.Spec.Containers.All(c =>
                            pod.Status.ContainerStatuses.Any(cs =>
                                cs.Name == c.Name &&
                                (cs.Name == "main"
                                    ? cs.State?.Running != null
                                    : cs.State?.Terminated?.ExitCode == 0)))))
                {
                    var finishedTime = jobPods.SelectMany(p => p.Status.ContainerStatuses).Select(cs => cs.State.Terminated?.FinishedAt).Where(t => t != null).Max();
                    return finishedTime ?? _run.CreatedAt;
                }
            }

            return null;
        }

        private static int GetJobCompletionIndex(V1Pod pod)
        {
            if (!int.TryParse(pod.Metadata.Annotations?["batch.kubernetes.io/job-completion-index"], CultureInfo.InvariantCulture, out var index))
            {
                throw new InvalidOperationException($"Pod {pod.Metadata.Name} is missing the job-completion-index annotation");
            }

            return index;
        }

        private async ValueTask<Run> UpdateNodePools(CancellationToken cancellationToken)
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

            static string GetNodePool(IReadOnlyCollection<V1Pod> pods)
            {
                return string.Join(
                    ",",
                    pods.Select(p => p.Spec.NodeName).Where(n => !string.IsNullOrEmpty(n)).Select(GetNodePoolFromNodeName).Distinct());
            }

            (var jobPods, var workerPods) = await GetPods(cancellationToken);

            RunCodeTarget? newWorkerTarget = _run.Worker != null && _run.Worker.NodePool == null
                ? _run.Worker with { NodePool = GetNodePool(workerPods) }
                : _run.Worker;

            JobRunCodeTarget newJobTarget = _run.Job.NodePool == null
                ? _run.Job with { NodePool = GetNodePool(jobPods) }
                : _run.Job;

            return _run = ReferenceEquals(newWorkerTarget, _run.Worker) && ReferenceEquals(newJobTarget, _run.Job)
                ? _run
                : _run with { Worker = newWorkerTarget, Job = newJobTarget };
        }

        // Used to extract "gpunp" from an AKS node named "aks-gpunp-23329378-vmss000007"
        [GeneratedRegex(@"^aks-([^\-]+)-", RegexOptions.Compiled)]
        private static partial Regex NodePoolFromNodeNameRegex();
    }
}
