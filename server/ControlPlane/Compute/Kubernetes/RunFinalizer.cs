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

public class RunFinalizer : BackgroundService
{
    private readonly IRepository _repository;
    private readonly RunChangeFeed _changeFeed;

    private readonly IKubernetes _client;
    private readonly ILogSource _logSource;
    private readonly ILogArchive _logArchive;
    private readonly KubernetesApiOptions _k8sOptions;
    private readonly ILogger<RunFinalizer> _logger;

    public RunFinalizer(IRepository repository, RunChangeFeed changeFeed, ILogger<RunFinalizer> logger, IKubernetes client, IOptions<KubernetesApiOptions> kubernetesOptions, ILogSource logSource, ILogArchive logArchive)
    {
        _repository = repository;
        _changeFeed = changeFeed;
        _logger = logger;
        _client = client;
        _k8sOptions = kubernetesOptions.Value;
        _logSource = logSource;
        _logArchive = logArchive;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        var channel = Channel.CreateBounded<ObservedRunState>(128);
        _changeFeed.RegisterObserver(channel.Writer);

        var allTasks = Enumerable.Range(0, 10).Select(async _ =>
        {
            try
            {
                await foreach (var state in channel.Reader.ReadAllAsync(stoppingToken))
                {
                    if (!state.Status.IsTerminal())
                    {
                        continue;
                    }

                    await FinalizeRun(state, stoppingToken);
                }
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
        }).ToList();

        while (allTasks.Count != 0)
        {
            // Wait for any task to complete
            var completedTask = await Task.WhenAny(allTasks);

            // Remove the completed task from the list
            allTasks.Remove(completedTask);

            // Check if the task failed
            if (completedTask.IsFaulted)
            {
                // Throw the original exception to stop the process
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
