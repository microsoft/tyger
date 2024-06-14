// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Globalization;
using System.Runtime.CompilerServices;
using k8s;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;
using static Tyger.ControlPlane.Compute.Kubernetes.KubernetesMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public partial class KubernetesRunReader : IRunReader

{
    private readonly IKubernetes _client;
    private readonly IRepository _repository;
    private readonly KubernetesApiOptions _k8sOptions;
    private readonly ILogger<KubernetesRunReader> _logger;

    public KubernetesRunReader(
        IKubernetes client,
        IRepository repository,
        IOptions<KubernetesApiOptions> k8sOptions,
        ILogger<KubernetesRunReader> logger)
    {
        _client = client;
        _repository = repository;
        _k8sOptions = k8sOptions.Value;
        _logger = logger;
    }

    public async Task<(IReadOnlyList<Run>, string? nextContinuationToken)> ListRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        (var partialRuns, var nextContinuationToken) = await _repository.GetRuns(limit, since, continuationToken, cancellationToken);
        if (partialRuns.All(r => r.Final))
        {
            return (partialRuns.AsReadOnly(), nextContinuationToken);
        }

        var selector = $"{RunLabel} in ({string.Join(",", partialRuns.Where(p => !p.Final).Select(p => p.Id))})";

        var jobAndPodsById = await _client.EnumerateJobsInNamespace(_k8sOptions.Namespace, labelSelector: selector, cancellationToken: cancellationToken)
            .GroupJoin(
                _client.EnumeratePodsInNamespace(_k8sOptions.Namespace, labelSelector: selector, cancellationToken: cancellationToken),
                j => j.GetLabel(RunLabel),
                p => p.GetLabel(RunLabel),
                (j, p) => (job: j, pods: p))
            .ToDictionaryAsync(p => long.Parse(p.job.GetLabel(RunLabel), CultureInfo.InvariantCulture), p => p, cancellationToken);

        for (int i = 0; i < partialRuns.Count; i++)
        {
            var run = partialRuns[i];
            if (!run.Final)
            {
                if (!jobAndPodsById.TryGetValue(run.Id!.Value, out var jobAndPods))
                {
                    continue;
                }

                partialRuns[i] = await run.GetUpdatedRun(_client, _k8sOptions, cancellationToken, jobAndPods.job, await jobAndPods.pods.ToListAsync(cancellationToken));
            }
        }

        return (partialRuns.AsReadOnly(), nextContinuationToken);
    }

    public async Task<Run?> GetRun(long id, CancellationToken cancellationToken)
    {
        var run = await _repository.GetRun(id, cancellationToken);
        if (run is null or { Final: true })
        {
            return run;
        }

        return await run.GetUpdatedRun(_client, _k8sOptions, cancellationToken);
    }

    public async IAsyncEnumerable<Run> WatchRun(long id, [EnumeratorCancellation] CancellationToken cancellationToken)
    {
        var run = await GetRun(id, cancellationToken);
        if (run is null)
        {
            yield break;
        }

        if (run.Final)
        {
            yield return run;
            yield break;
        }

        var jobList = await _client.BatchV1.ListNamespacedJobAsync(_k8sOptions.Namespace, fieldSelector: $"metadata.name={JobNameFromRunId(run.Id!.Value)}", cancellationToken: cancellationToken);
        if (jobList.Items.Count == 0)
        {
            _logger.RunMissingJob(JobNameFromRunId(run.Id!.Value));
            yield break;
        }

        var job = jobList.Items[0];
        var podList = await _client.CoreV1.ListNamespacedPodAsync(_k8sOptions.Namespace, labelSelector: $"{JobLabel}={id}", cancellationToken: cancellationToken);
        Dictionary<string, V1Pod> pods = podList.Items.ToDictionary(p => p.Name());

        run = await run.GetUpdatedRun(_client, _k8sOptions, cancellationToken, job, pods.Values.ToList());
        yield return run;

        if (run.Status is RunStatus.Succeeded or RunStatus.Failed or RunStatus.Canceled)
        {
            yield break;
        }

        var cts = new CancellationTokenSource();
        using var combinedCts = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, cts.Token);
        var jobWatchStream = _client.WatchNamespacedJobsWithRetry(_logger, _k8sOptions.Namespace, fieldSelector: $"metadata.name={JobNameFromRunId(run.Id!.Value)}", resourceVersion: jobList.ResourceVersion(), cancellationToken: combinedCts.Token).Select(t => (t.Item1, (IKubernetesObject)t.Item2));
        var podWatchStream = _client.WatchNamespacedPodsWithRetry(_logger, _k8sOptions.Namespace, labelSelector: $"{JobLabel}={id}", resourceVersion: podList.ResourceVersion(), cancellationToken: combinedCts.Token).Select(t => (t.Item1, (IKubernetesObject)t.Item2));
        var combinedStream = AsyncEnumerableEx.Merge(jobWatchStream, podWatchStream).WithCancellation(combinedCts.Token);

        var combinedEnumerator = combinedStream.GetAsyncEnumerator();

        bool updateRunFromRepository = false;

        try
        {
            while (await combinedEnumerator.MoveNextAsync())
            {
                (WatchEventType watchEventType, IKubernetesObject k8sObject) = combinedEnumerator.Current;
                switch (k8sObject)
                {
                    case V1Job updatedJob:
                        switch (watchEventType)
                        {
                            case WatchEventType.Modified:
                                job = updatedJob;
                                break;
                            case WatchEventType.Deleted:
                                updateRunFromRepository = true;
                                cts.Cancel();
                                yield break;
                        }

                        break;
                    case V1Pod updatedPod:
                        switch (watchEventType)
                        {
                            case WatchEventType.Added:
                            case WatchEventType.Modified:
                                pods[updatedPod.Name()] = updatedPod;
                                break;
                            case WatchEventType.Deleted:
                                pods.Remove(updatedPod.Name());
                                updateRunFromRepository = true;
                                break;
                        }

                        break;
                }

                var updatedRun = run;

                if (updateRunFromRepository)
                {
                    var currentRun = await _repository.GetRun(id, cancellationToken);
                    if (currentRun is null)
                    {
                        cts.Cancel();
                        yield break;
                    }

                    updatedRun = currentRun;
                    updateRunFromRepository = false;
                }

                updatedRun = await updatedRun.GetUpdatedRun(_client, _k8sOptions, cancellationToken, job, pods.Values.ToList());

                if (run.Status != updatedRun.Status ||
                    !string.Equals(run.StatusReason, updatedRun.StatusReason, StringComparison.Ordinal) ||
                    run.RunningCount != updatedRun.RunningCount)
                {
                    yield return updatedRun;
                }

                run = updatedRun;

                if (run.Final || run.Status is RunStatus.Succeeded or RunStatus.Failed or RunStatus.Canceled)
                {
                    cts.Cancel();
                    yield break;
                }
            }
        }
        finally
        {
            try
            {
                await combinedEnumerator.DisposeAsync();
            }
            catch (AggregateException e) when (cts.IsCancellationRequested && e.InnerExceptions.Any(ex => ex is OperationCanceledException))
            {
            }
        }
    }
}
