// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Globalization;
using k8s;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Logging;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;
using static Tyger.ControlPlane.Compute.Kubernetes.KubernetesMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public sealed class KubernetesRunSweeper : IRunSweeper, IHostedService, IDisposable
{
    private static readonly TimeSpan s_minDurationAfterArchivingBeforeDeletingPod = TimeSpan.FromSeconds(30);

    private Task? _backgroundTask;
    private CancellationTokenSource? _backgroundCancellationTokenSource;
    private readonly IKubernetes _client;
    private readonly IRepository _repository;
    private readonly ILogSource _logSource;
    private readonly ILogArchive _logArchive;
    private readonly KubernetesApiOptions _k8sOptions;
    private readonly ILogger<KubernetesRunSweeper> _logger;

    public KubernetesRunSweeper(
        IKubernetes client,
        IRepository repository,
        IOptions<KubernetesApiOptions> k8sOptions,
        ILogSource logSource,
        ILogArchive logArchive,
        ILogger<KubernetesRunSweeper> logger)
    {
        _client = client;
        _repository = repository;
        _logSource = logSource;
        _logArchive = logArchive;
        _k8sOptions = k8sOptions.Value;
        _logger = logger;
    }

    public Task StartAsync(CancellationToken cancellationToken)
    {
        _backgroundCancellationTokenSource = new CancellationTokenSource();
        _backgroundTask = BackgroundLoop(_backgroundCancellationTokenSource.Token);
        return Task.CompletedTask;
    }

    public async Task StopAsync(CancellationToken cancellationToken)
    {
        if (_backgroundCancellationTokenSource == null || _backgroundTask == null)
        {
            return;
        }

        _backgroundCancellationTokenSource.Cancel();

        // wait for the background task to complete, but give up once the cancellation token is canceled.
        var tcs = new TaskCompletionSource();
        cancellationToken.Register(s => ((TaskCompletionSource)s!).SetResult(), tcs);
        await Task.WhenAny(_backgroundTask, tcs.Task);
    }

    private async Task BackgroundLoop(CancellationToken cancellationToken)
    {
        while (!cancellationToken.IsCancellationRequested)
        {
            try
            {
                await Task.Delay(TimeSpan.FromSeconds(30), cancellationToken);
                await SweepRuns(cancellationToken);
            }
            catch (TaskCanceledException) when (cancellationToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception e)
            {
                _logger.ErrorDuringBackgroundSweep(e);
            }
        }
    }

    public async Task SweepRuns(CancellationToken cancellationToken)
    {
        _logger.StartingBackgroundSweep();

        // first clear out runs that never got a pod created
        while (true)
        {
            var runs = await _repository.GetPageOfRunsThatNeverGotResources(cancellationToken);
            if (runs.Count == 0)
            {
                break;
            }

            foreach (var run in runs)
            {
                _logger.DeletingRunThatNeverCreatedResources(run.Id!.Value);
                await DeleteRunResources(run.Id.Value, cancellationToken);
                await _repository.DeleteRun(run.Id.Value, cancellationToken);
            }
        }

        // Now go though the list of jobs on the the cluster
        string? continuation = null;
        var placeholderRunTemplate = new Run { CreatedAt = DateTime.MinValue, Job = new() { Codespec = new CommittedCodespecRef("", null) } };
        do
        {
            var jobs = await _client.BatchV1.ListNamespacedJobAsync(_k8sOptions.Namespace, continueParameter: continuation, labelSelector: JobLabel, cancellationToken: cancellationToken);

            foreach (var job in jobs.Items)
            {
                var runId = long.Parse(job.GetLabel(JobLabel), CultureInfo.InvariantCulture);
                var status = (await (placeholderRunTemplate with { Id = runId }).GetPartiallyUpdatedRun(_client, _k8sOptions, cancellationToken, job)).Status;

                if (status is not RunStatus.Succeeded and not RunStatus.Failed and not RunStatus.Canceling and not RunStatus.Canceled)
                {
                    continue;
                }

                var run = await _repository.GetRun(runId, cancellationToken);
                switch (run)
                {
                    case null:
                        await _repository.DeleteRun(runId, cancellationToken);
                        await DeleteRunResources(runId, cancellationToken);
                        break;

                    case { LogsArchivedAt: null }:
                        await ArchiveLogs(run, cancellationToken);
                        if (status is RunStatus.Canceling or RunStatus.Canceled)
                        {
                            // now that we have collected the logs, terminate the pods.
                            string labelSelector = $"{RunLabel}={runId}";
                            await _client.CoreV1.DeleteCollectionNamespacedPodAsync(_k8sOptions.Namespace, labelSelector: labelSelector, cancellationToken: cancellationToken);
                        }

                        break;

                    case var _ when DateTimeOffset.UtcNow - run.LogsArchivedAt > s_minDurationAfterArchivingBeforeDeletingPod || (status is RunStatus.Canceling or RunStatus.Canceled):
                        run = await run.GetUpdatedRun(_client, _k8sOptions, cancellationToken);
                        if (run.Status == RunStatus.Canceling)
                        {
                            // the pods did not termintate in time. Override the status.
                            run = run with
                            {
                                Status = RunStatus.Canceled,
                                StatusReason = "Canceled by user",
                                RunningCount = 0,
                                FinishedAt = run.FinishedAt ?? DateTimeOffset.UtcNow
                            };
                        }

                        _logger.FinalizingTerminatedRun(run.Id!.Value, run.Status!.Value);
                        await _repository.UpdateRun(run with { Final = true }, cancellationToken: cancellationToken);
                        await DeleteRunResources(run.Id!.Value, cancellationToken);
                        break;
                    default:
                        break;
                }
            }

            continuation = jobs.Metadata.ContinueProperty;
        } while (continuation != null);

        _logger.BackgroundSweepCompleted();
    }

    private async Task ArchiveLogs(Run run, CancellationToken cancellationToken)
    {
        var pipeline = await _logSource.GetLogs(run.Id!.Value, new GetLogsOptions { IncludeTimestamps = true }, cancellationToken);
        pipeline ??= new Pipeline(Array.Empty<byte>());

        await _logArchive.ArchiveLogs(run.Id.Value, pipeline, cancellationToken);
        await _repository.UpdateRun(run with { LogsArchivedAt = DateTimeOffset.UtcNow }, cancellationToken: cancellationToken);
    }

    private async Task DeleteRunResources(long runId, CancellationToken cancellationToken)
    {
        string labelSelector = $"{RunLabel}={runId}";

        await _client.BatchV1.DeleteCollectionNamespacedJobAsync(_k8sOptions.Namespace, labelSelector: labelSelector, propagationPolicy: "Foreground", cancellationToken: cancellationToken);
        await _client.AppsV1.DeleteCollectionNamespacedStatefulSetAsync(_k8sOptions.Namespace, labelSelector: labelSelector, propagationPolicy: "Foreground", cancellationToken: cancellationToken);
        await _client.CoreV1.DeleteCollectionNamespacedSecretAsync(_k8sOptions.Namespace, labelSelector: labelSelector, propagationPolicy: "Foreground", cancellationToken: cancellationToken);
        await _client.CoreV1.DeleteCollectionNamespacedServiceAsync(_k8sOptions.Namespace, labelSelector: labelSelector, propagationPolicy: "Foreground", cancellationToken: cancellationToken);

        await foreach (var pod in _client.EnumeratePodsInNamespace(_k8sOptions.Namespace, labelSelector: labelSelector, cancellationToken: cancellationToken))
        {
            // clear finalizer on Pod
            if (pod.RemoveFinalizer(FinalizerName))
            {
                await _client.CoreV1.PatchNamespacedPodAsync(
                    new V1Patch(new { metadata = new { finalizers = pod.Finalizers() } }, V1Patch.PatchType.MergePatch),
                    pod.Name(),
                    pod.Namespace(),
                    cancellationToken: cancellationToken);
            }
        }
    }

    public void Dispose()
    {
        if (_backgroundTask is { IsCompleted: true })
        {
            _backgroundTask.Dispose();
        }
    }
}
