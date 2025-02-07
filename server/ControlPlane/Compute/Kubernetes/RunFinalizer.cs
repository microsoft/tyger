// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Net;
using System.Threading.Channels;
using k8s;
using k8s.Autorest;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Logging;
using Tyger.ControlPlane.Model;
using static Tyger.ControlPlane.Compute.Kubernetes.KubernetesMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

/// <summary>
/// When runs reach a terminal state, this class archives their logs and deletes associated Kubernetes resources.
/// </summary>
public class RunFinalizer : BackgroundService
{
    // A value that is too high will put a lot of load on the Kubernetes API server
    // because retrieving logs is a relatively expensive operation.
    private const int MaxConcurrentFinalizations = 10;

    private readonly Repository _repository;
    private readonly RunChangeFeed _changeFeed;

    private readonly IKubernetes _client;
    private readonly ILogSource _logSource;
    private readonly ILogArchive _logArchive;
    private readonly KubernetesApiOptions _k8sOptions;
    private readonly ILogger<RunFinalizer> _logger;
    private readonly Channel<(bool leaseHeld, int token)> _leaseStateChangeChannel = Channel.CreateUnbounded<(bool leaseHeld, int token)>();

    public RunFinalizer(Repository repository, RunChangeFeed changeFeed, ILogger<RunFinalizer> logger, IKubernetes client, IOptions<KubernetesApiOptions> kubernetesOptions, ILogSource logSource, LeaseManager leaseManager, ILogArchive logArchive)
    {
        _repository = repository;
        _changeFeed = changeFeed;
        _logger = logger;
        _client = client;
        _k8sOptions = kubernetesOptions.Value;
        _logSource = logSource;
        _logArchive = logArchive;

        leaseManager.RegisterListener(_leaseStateChangeChannel.Writer);
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        var channel = Channel.CreateBounded<ObservedRunState>(1024);
        _changeFeed.RegisterObserver(channel.Writer);

        // Keep track of retry counts for failed finalizations
        var failedRuns = new Dictionary<long, int>();

        var hasLease = false;
        var initialLeaseStateTask = new TaskCompletionSource();

        var allTasks = new List<Task>
        {
            Task.Run(async () =>
            {
                await foreach ((var newHasLease, _) in _leaseStateChangeChannel.Reader.ReadAllAsync(stoppingToken))
                {
                    hasLease = newHasLease;
                    bool isInitialState = initialLeaseStateTask.TrySetResult();
                    if (newHasLease && !isInitialState)
                    {
                        foreach (var run in await _repository.GetFinalizableRuns(stoppingToken))
                        {
                            await channel.Writer.WriteAsync(new ObservedRunState(run, databaseUpdatedAt: null), stoppingToken);
                        }
                    }
                }
            }, stoppingToken)
        };

        allTasks.AddRange([.. Enumerable.Range(0, MaxConcurrentFinalizations).Select(async _ =>
        {
            try
            {
                await foreach (var state in channel.Reader.ReadAllAsync(stoppingToken))
                {
                    if (!hasLease || !state.Status.IsTerminal())
                    {
                        continue;
                    }

                    try
                    {
                        await FinalizeRun(state, stoppingToken);
                        lock (failedRuns)
                        {
                            failedRuns.Remove(state.Id);
                        }
                    }
                    catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
                    {
                        return;
                    }
                    catch (Exception e)
                    {
                        _logger.ErrorDuringFinalization(state.Id, e);
                        lock (failedRuns)
                        {
                            var currentCount = failedRuns.GetValueOrDefault(state.Id);
                            if (currentCount >= 5)
                            {
                                throw;
                            }

                            failedRuns[state.Id] = currentCount + 1;
                        }

                        var discarded = Task.Run(async () =>
                        {
                            await Task.Delay(TimeSpan.FromSeconds(5), stoppingToken);
                            await channel.Writer.WriteAsync(state, stoppingToken);
                        }, stoppingToken);
                    }
                }
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
        })]);

        while (allTasks.Count != 0)
        {
            var completedTask = await Task.WhenAny(allTasks);

            allTasks.Remove(completedTask);

            if (completedTask.IsFaulted)
            {
                await completedTask;
            }
        }
    }

    private async Task FinalizeRun(ObservedRunState runState, CancellationToken cancellationToken)
    {
        _logger.FinalizingRun(runState.Id);

        await ArchiveLogs(runState.Id, cancellationToken);
        await _repository.UpdateRunAsLogsArchived(runState.Id, cancellationToken);
        _logger.ArchivedLogsForRun(runState.Id);

        await DeleteRunResources(runState, cancellationToken);
        await _repository.UpdateRunAsFinal(runState.Id, cancellationToken);
        _logger.FinalizedRun(runState.Id);
    }

    private async Task ArchiveLogs(long runId, CancellationToken cancellationToken)
    {
        var pipeline = await _logSource.GetLogs(runId, new GetLogsOptions { IncludeTimestamps = true }, cancellationToken);
        pipeline ??= new Pipeline([]);

        await _logArchive.ArchiveLogs(runId, pipeline, cancellationToken);
    }

    private async Task DeleteRunResources(ObservedRunState runState, CancellationToken cancellationToken)
    {
        for (var i = 0; i < runState.SpecifiedJobReplicaCount; i++)
        {
            try
            {
                await _client.CoreV1.DeleteNamespacedPodAsync(JobPodName(runState.Id, i), _k8sOptions.Namespace, gracePeriodSeconds: 2, cancellationToken: cancellationToken);
            }
            catch (HttpOperationException ex) when (ex.Response.StatusCode == HttpStatusCode.NotFound)
            {
            }
        }

        try
        {
            await _client.CoreV1.DeleteNamespacedSecretAsync(SecretNameFromRunId(runState.Id), _k8sOptions.Namespace, cancellationToken: cancellationToken);
        }
        catch (HttpOperationException ex) when (ex.Response.StatusCode == HttpStatusCode.NotFound)
        {
        }

        if (runState.SpecifiedWorkerReplicaCount > 0)
        {
            try
            {
                await _client.AppsV1.DeleteNamespacedStatefulSetAsync(StatefulSetNameFromRunId(runState.Id), _k8sOptions.Namespace, propagationPolicy: "Foreground", cancellationToken: cancellationToken);
            }
            catch (HttpOperationException ex) when (ex.Response.StatusCode == HttpStatusCode.NotFound)
            {
            }

            try
            {
                await _client.CoreV1.DeleteNamespacedServiceAsync(ServiceNameFromRunId(runState.Id), _k8sOptions.Namespace, propagationPolicy: "Foreground", cancellationToken: cancellationToken);
            }
            catch (HttpOperationException ex) when (ex.Response.StatusCode == HttpStatusCode.NotFound)
            {
            }
        }
    }
}
