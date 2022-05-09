using System.Globalization;
using System.Net;
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

        var selector = $"{JobLabel} in ({string.Join(",", partialRuns.Where(p => !p.final).Select(p => p.run.Id))})";

        var jobAndPodsById = await _client.EnumerateJobsInNamespace(_k8sOptions.Namespace, labelSelector: selector, cancellationToken: cancellationToken)
            .GroupJoin(
                _client.EnumeratePodsInNamespace(_k8sOptions.Namespace, labelSelector: selector, cancellationToken: cancellationToken),
                j => j.GetLabel(JobLabel),
                p => p.GetLabel(JobLabel),
                (j, p) => (job: j, pods: p))
            .ToDictionaryAsync(p => long.Parse(p.job.GetLabel(JobLabel), CultureInfo.InvariantCulture), p => p, cancellationToken);

        for (int i = 0; i < partialRuns.Count; i++)
        {
            (var run, var final) = partialRuns[i];
            if (!final)
            {
                if (!jobAndPodsById.TryGetValue(run.Id, out var jobAndPod))
                {
                    continue;
                }

                partialRuns[i] = (UpdateRunFromJobAndPods(run, jobAndPod.job, await jobAndPod.pods.ToListAsync(cancellationToken)), true);
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
            job = await _client.ReadNamespacedJobAsync(JobNameFromRunId(id), _k8sOptions.Namespace, cancellationToken: cancellationToken);
        }
        catch (HttpOperationException e) when (e.Response.StatusCode == HttpStatusCode.NotFound)
        {
            _logger.RunMissingJob(JobNameFromRunId(id));
            return null;
        }

        var pods = await _client.EnumeratePodsInNamespace(_k8sOptions.Namespace, labelSelector: $"{JobLabel}={id}", cancellationToken: cancellationToken)
            .ToListAsync(cancellationToken);

        return UpdateRunFromJobAndPods(run, job, pods);
    }

    public static Run UpdateRunFromJobAndPods(Run run, V1Job job, IList<V1Pod> pods)
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
            return run with
            {
                Status = "Succeeded",
                FinishedAt = pods.Where(p => p.Status.Phase == "Succeeded").Min(p => p.Status.ContainerStatuses.Single().State.Terminated.FinishedAt)
            };
        }

        var runningCount = pods.Count(p => p.Status.Phase == "Running");

        if (runningCount > 0)
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
}
