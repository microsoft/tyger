using Docker.DotNet;
using Docker.DotNet.Models;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Logging;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;

namespace Tyger.ControlPlane.Compute.Docker;

public sealed class DockerRunSweeper : IRunSweeper, IHostedService, IDisposable
{
    private static readonly TimeSpan s_minDurationAfterArchivingBeforeDeletingPod = TimeSpan.FromSeconds(30);

    private Task? _backgroundTask;
    private CancellationTokenSource? _backgroundCancellationTokenSource;
    private readonly ILogSource _logSource;
    private readonly DockerOptions _dockerSecretOptions;
    private readonly ILogArchive _logArchive;
    private readonly IRepository _repository;
    private readonly DockerClient _client;
    private readonly IRunReader _runReader;
    private readonly ILogger<DockerRunSweeper> _logger;

    public DockerRunSweeper(IRepository repository, DockerClient client, IRunReader runReader, ILogSource logSource, ILogArchive logArchive, IOptions<DockerOptions> dockerSecretOptions, ILogger<DockerRunSweeper> logger)
    {
        _repository = repository;
        _client = client;
        _runReader = runReader;
        _logger = logger;
        _logSource = logSource;
        _dockerSecretOptions = dockerSecretOptions.Value;
        _logArchive = logArchive;
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

        var containerGroups = (await _client.Containers.ListContainersAsync(new ContainersListParameters()
        {
            All = true,
            Filters = new Dictionary<string, IDictionary<string, bool>>
            {
                {"label", new Dictionary<string, bool>{{ "tyger-run", true } } },
                {"status", new Dictionary<string, bool>{{ "exited", true }, {"removing", true}, {"dead", true} } }
            },
        }, cancellationToken))
        .GroupBy(c => c.Labels["tyger-run"]);

        foreach (var group in containerGroups)
        {
            var runId = long.Parse(group.Key);
            switch (await _runReader.GetRun(runId, cancellationToken))
            {
                case null:
                    await _repository.DeleteRun(runId, cancellationToken);
                    await DeleteRunResources(runId, cancellationToken);
                    continue;
                case { Status: RunStatus.Succeeded or RunStatus.Failed or RunStatus.Canceling or RunStatus.Canceled } run:

                    switch (await _repository.GetRun(runId, cancellationToken))
                    {
                        case (_, _, null):
                            await ArchiveLogs(run, cancellationToken);
                            break;
                        case (_, _, var time) when DateTimeOffset.UtcNow - time > s_minDurationAfterArchivingBeforeDeletingPod:

                            _logger.FinalizingTerminatedRun(run.Id!.Value, run.Status!.Value);
                            await _repository.UpdateRun(run, final: true, cancellationToken: cancellationToken);
                            await DeleteRunResources(run.Id!.Value, cancellationToken);
                            break;
                        default:
                            break;
                    }

                    break;
            }
        }

        _logger.BackgroundSweepCompleted();
    }

    private async Task DeleteRunResources(long runId, CancellationToken cancellationToken)
    {
        try
        {
            var containers = await _client.Containers.ListContainersAsync(new ContainersListParameters()
            {
                All = true,
                Filters = new Dictionary<string, IDictionary<string, bool>>
            {
                {"label", new Dictionary<string, bool>{{ $"tyger-run={runId}", true } } },
            }
            }, cancellationToken);

            foreach (var container in containers)
            {
                try
                {
                    await _client.Containers.RemoveContainerAsync(container.ID, new() { Force = true }, cancellationToken);
                }
                catch (DockerApiException e)
                {
                    _logger.FailedToRemoveContainer(container.ID, e);
                }

                if (container.Labels?.TryGetValue(DockerRunCreator.EphemeralBufferSocketPathLabelKey, out var socketPath) == true)
                {
                    try
                    {
                        File.Delete(socketPath);
                    }
                    catch (Exception e)
                    {
                        _logger.FailedToRemoveEphemeralBufferSocket(socketPath, e);
                    }
                }
            }
        }
        finally
        {
            var secretsPath = Path.Combine(_dockerSecretOptions.RunSecretsPath, runId.ToString());
            if (Directory.Exists(secretsPath))
            {
                try
                {
                    Directory.Delete(secretsPath, true);
                }
                catch (Exception e)
                {
                    _logger.FailedToRemoveRunSecretsDirectory(runId, e);
                }
            }
        }
    }

    private async Task ArchiveLogs(Run run, CancellationToken cancellationToken)
    {
        var pipeline = await _logSource.GetLogs(run.Id!.Value, new GetLogsOptions { IncludeTimestamps = true }, cancellationToken);
        pipeline ??= new Pipeline(Array.Empty<byte>());

        await _logArchive.ArchiveLogs(run.Id.Value, pipeline, cancellationToken);
        await _repository.UpdateRun(run, logsArchivedAt: DateTimeOffset.UtcNow, cancellationToken: cancellationToken);
    }

    public void Dispose()
    {
        if (_backgroundTask is { IsCompleted: true })
        {
            _backgroundTask.Dispose();
        }
    }
}
