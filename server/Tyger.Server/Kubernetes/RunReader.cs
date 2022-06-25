using System.Globalization;
using System.Net;
using System.Text.RegularExpressions;
using k8s;
using k8s.Autorest;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.Server.Database;
using Tyger.Server.Model;
using static Tyger.Server.Kubernetes.KubernetesMetadata;

namespace Tyger.Server.Kubernetes;

public class RunReader
{
    // Used to extract "gpunp" from an AKS node named "aks-gpunp-23329378-vmss000007"
    private static readonly Regex s_nodePoolFromNodeName = new(@"^aks-([^\-]+)-", RegexOptions.Compiled);

    private readonly IKubernetes _client;
    private readonly IRepository _repository;
    private readonly KubernetesOptions _k8sOptions;
    private readonly ILogger<RunReader> _logger;

    public RunReader(
        IKubernetes client,
        IRepository repository,
        IOptions<KubernetesOptions> k8sOptions,
        ILogger<RunReader> logger)
    {
        _client = client;
        _repository = repository;
        _k8sOptions = k8sOptions.Value;
        _logger = logger;
    }

    public async Task<(IReadOnlyList<Run>, string? nextContinuationToken)> ListRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        (var partialRuns, var nextContinuationToken) = await _repository.GetRuns(limit, since, continuationToken, cancellationToken);
        if (partialRuns.All(r => r.final))
        {
            return (partialRuns.Select(r => r.run).ToList(), nextContinuationToken);
        }

        var selector = $"{RunLabel} in ({string.Join(",", partialRuns.Where(p => !p.final).Select(p => p.run.Id))})";

        var jobAndPodsById = await _client.EnumerateJobsInNamespace(_k8sOptions.Namespace, labelSelector: selector, cancellationToken: cancellationToken)
            .GroupJoin(
                _client.EnumeratePodsInNamespace(_k8sOptions.Namespace, labelSelector: selector, cancellationToken: cancellationToken),
                j => j.GetLabel(RunLabel),
                p => p.GetLabel(RunLabel),
                (j, p) => (job: j, pods: p))
            .ToDictionaryAsync(p => long.Parse(p.job.GetLabel(RunLabel), CultureInfo.InvariantCulture), p => p, cancellationToken);

        for (int i = 0; i < partialRuns.Count; i++)
        {
            (var run, var final) = partialRuns[i];
            if (!final)
            {
                if (!jobAndPodsById.TryGetValue(run.Id, out var jobAndPods))
                {
                    continue;
                }

                partialRuns[i] = (UpdateRunFromJobAndPods(run, jobAndPods.job, await jobAndPods.pods.ToListAsync(cancellationToken)), true);
            }
        }

        return (partialRuns.Where(p => p.final).Select(p => p.run).ToList(), nextContinuationToken);
    }

    public async Task<Run?> GetRun(long id, CancellationToken cancellationToken)
    {
        if (await _repository.GetRun(id, cancellationToken) is not (Run run, var final, _))
        {
            return null;
        }

        if (final)
        {
            return run;
        }

        V1Job job;
        try
        {
            job = await _client.BatchV1.ReadNamespacedJobAsync(JobNameFromRunId(id), _k8sOptions.Namespace, cancellationToken: cancellationToken);
        }
        catch (HttpOperationException e) when (e.Response.StatusCode == HttpStatusCode.NotFound)
        {
            _logger.RunMissingJob(JobNameFromRunId(id));
            return null;
        }

        var pods = await _client.EnumeratePodsInNamespace(_k8sOptions.Namespace, labelSelector: $"{RunLabel}={id}", cancellationToken: cancellationToken)
            .ToListAsync(cancellationToken);

        return UpdateRunFromJobAndPods(run, job, pods);
    }

    public static Run UpdateRunFromJobAndPods(Run run, V1Job job, IReadOnlyList<V1Pod> pods)
    {
        IReadOnlyList<V1Pod> jobPods;
        IReadOnlyList<V1Pod> workerPods;

        if (pods.Count == 0)
        {
            jobPods = workerPods = pods;
        }
        else if (run.Worker == null)
        {
            jobPods = pods;
            workerPods = Array.Empty<V1Pod>();
        }
        else
        {
            List<V1Pod> localJobPods, localWorkerPods;
            jobPods = localJobPods = new List<V1Pod>();
            workerPods = localWorkerPods = new List<V1Pod>();

            foreach (var pod in pods)
            {
                (pod.GetLabel(JobLabel) is not null ? localJobPods : localWorkerPods).Add(pod);
            }
        }

        run = UpdateStatus(run, job, jobPods, workerPods);
        return UpdateNodePools(run, job, jobPods, workerPods);

        static Run UpdateStatus(Run run, V1Job job, IReadOnlyList<V1Pod> jobPods, IReadOnlyList<V1Pod> workerPods)
        {
            if (job.Status.Conditions?.FirstOrDefault(c => c.Type == "Failed" && c.Status == "True") is V1JobCondition failureCondition)
            {
                return run with
                {
                    Status = "Failed",
                    Reason = failureCondition.Reason,
                    FinishedAt = failureCondition.LastTransitionTime!
                };
            }

            if (job.Status.Succeeded is > 0)
            {
                var finishedTimes = jobPods.Where(p => p.Status.Phase == "Succeeded").Select(p => p.Status.ContainerStatuses.Single().State.Terminated?.FinishedAt).Where(t => t != null).Select(t => t!.Value).ToList();
                return run with
                {
                    Status = "Succeeded",
                    FinishedAt = finishedTimes.Count == 0 ? null : finishedTimes.Min()
                };
            }

            // Note that the job object may not yet reflect the status of the pods.
            // It could be that pods have succeeeded or failed without the job reflecting this.
            // We only say the run has succeeded or failed based on the job status, but we want
            // to avoid returning a pending state if no pods are running because they have
            // all exited but the job hasn't been updated yet.
            var isRunning = jobPods.Any(p => p.Status.Phase is "Running" or "Succeeded" or "Failed");
            var runningCount = jobPods.Count(p => p.Status.Phase == "Running");

            if (isRunning)
            {
                return run with
                {
                    Status = "Running",
                    RunningCount = runningCount
                };
            }

            return run with
            {
                Status = "Pending",
            };
        }

        static Run UpdateNodePools(Run run, V1Job job, IReadOnlyList<V1Pod> jobPods, IReadOnlyList<V1Pod> workerPods)
        {
            static string GetNodePoolFromNodeName(string nodeName)
            {
                var match = s_nodePoolFromNodeName.Match(nodeName);
                if (!match.Success)
                {
                    throw new InvalidOperationException($"Node name in unexpected format: '{nodeName}'");
                }

                return match.Groups[1].Value;
            }

            static string GetNodePool(IReadOnlyList<V1Pod> pods)
            {
                return string.Join(
                    ",",
                    pods.Select(p => p.Spec.NodeName).Where(n => !string.IsNullOrEmpty(n)).Select(GetNodePoolFromNodeName).Distinct());
            }

            RunCodeTarget? newWorkerTarget = run.Worker != null && run.Worker.NodePool == null
                ? run.Worker with { NodePool = GetNodePool(workerPods) }
                : run.Worker;

            RunCodeTarget newJobTarget = run.Job.NodePool == null
                ? run.Job with { NodePool = GetNodePool(jobPods) }
                : run.Job;

            return ReferenceEquals(newWorkerTarget, run.Worker) && ReferenceEquals(newJobTarget, run.Job)
                ? run
                : run with { Worker = newWorkerTarget, Job = newJobTarget };
        }
    }
}
