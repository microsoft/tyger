// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Runtime.CompilerServices;
using System.Threading.Channels;
using Docker.DotNet;
using Docker.DotNet.Models;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;

namespace Tyger.ControlPlane.Compute.Docker;

public class DockerRunReader : IRunReader, IRunAugmenter
{
    private readonly DockerClient _client;
    private readonly Repository _repository;
    private readonly DockerRunUpdater _runUpdater;

    public DockerRunReader(DockerClient client, Repository repository, DockerRunUpdater runUpdater)
    {
        _client = client;
        _repository = repository;
        _runUpdater = runUpdater;
    }

    public async Task<IDictionary<RunStatus, long>> GetRunCounts(DateTimeOffset? since, Dictionary<string, string>? tags, CancellationToken cancellationToken)
    {
        return await _repository.GetRunCounts(since, tags, cancellationToken);
    }

    public async Task<(Run run, DateTimeOffset? modifiedAt, DateTimeOffset? logsArchivedAt, bool final, int tagsVersion)?> GetRun(long id, CancellationToken cancellationToken)
    {
        return await _repository.GetRun(id, cancellationToken);
    }

    public async Task<Run> AugmentRun(Run run, CancellationToken cancellationToken)
    {
        var containers = await (await _client.Containers
            .ListContainersAsync(
                new ContainersListParameters()
                {
                    All = true,
                    Filters = new Dictionary<string, IDictionary<string, bool>>
                    {
                        {"label", new Dictionary<string, bool>{{ $"tyger-run={run.Id}", true } } }
                    }
                }, cancellationToken))
            .ToAsyncEnumerable()
            .Select(async (c, ct) => await _client.Containers.InspectContainerAsync(c.ID, ct))
            .ToListAsync(cancellationToken);

        return UpdateRunFromContainers(run, containers);
    }

    public async Task<(IReadOnlyList<Run>, string? nextContinuationToken)> ListRuns(GetRunsOptions options, CancellationToken cancellationToken)
    {
        var (runInfos, nextContinuationToken) = await _repository.GetRuns(options with { OnlyResourcesCreated = true }, cancellationToken);
        return (runInfos.Select(er => er.run).ToList(), nextContinuationToken);
    }

    public async IAsyncEnumerable<Run> WatchRun(long id, [EnumeratorCancellation] CancellationToken cancellationToken)
    {
        if (await GetRun(id, cancellationToken) is not var (run, _, _, _, _))
        {
            yield break;
        }

        yield return run;

        if (run.Status.IsTerminal())
        {
            yield break;
        }

        var tagsUpdateChannel = Channel.CreateUnbounded<Run>();
        var eventsChannel = Channel.CreateUnbounded<object?>();
        var cancellation = new CancellationTokenSource();

        _runUpdater.RegisterTagUpdateObserver(id, tagsUpdateChannel.Writer);
        _ = Task.Run(async () =>
        {
            await foreach (var run in tagsUpdateChannel.Reader.ReadAllAsync(cancellation.Token))
            {
                await eventsChannel.Writer.WriteAsync(run, cancellation.Token);
            }
        }, cancellation.Token);

        try
        {

            _ = _client.System.MonitorEventsAsync(new ContainerEventsParameters()
            {
                Filters = new Dictionary<string, IDictionary<string, bool>>
                {
                    {
                        "label", new Dictionary<string, bool> { { $"tyger-run={run.Id}", true } }
                    }
                }
            }, new Progress<Message>(m =>
            {
                if (!eventsChannel.Writer.TryWrite(null))
                {
                    eventsChannel.Writer.WriteAsync(m).AsTask().Wait(cancellationToken);
                }
            }), cancellation.Token);

            async Task ScheduleFirstUpdate()
            {
                await Task.Delay(TimeSpan.FromSeconds(1), cancellationToken);
                await eventsChannel.Writer.WriteAsync(null, cancellationToken);
            }

            _ = ScheduleFirstUpdate();

            await foreach (var _ in eventsChannel.Reader.ReadAllAsync(cancellationToken))
            {
                if (await GetRun(id, cancellationToken) is not (var updatedRun, _, _, _, _))
                {
                    yield break;
                }

                if (!updatedRun.Equals(run))
                {
                    run = updatedRun;
                    yield return updatedRun;
                }

                if (run.Status.IsTerminal())
                {
                    yield break;
                }
            }
        }
        finally
        {
            _runUpdater.UnregisterTagUpdateObserver(id, tagsUpdateChannel.Writer);
            cancellation.Cancel();
        }
    }

    private static Run UpdateRunFromContainers(Run run, List<ContainerInspectResponse> containers)
    {
        var mainContainerName = $"/{DockerRunCreator.MainContainerName(run.Id!.Value)}"; // inspect always returns names with a leading slash

        var mainContainer = containers.FirstOrDefault(c => c.Name == mainContainerName);
        if (mainContainer != null)
        {
            run = run with { StartedAt = mainContainer?.State.StartedAt == null ? null : DateTimeOffset.Parse(mainContainer.State.StartedAt) };
        }

        if (run.Status is RunStatus.Canceled)
        {
            return run with { Status = RunStatus.Canceled };
        }

        var socketCount = containers.Aggregate(
            0,
            (acc, c) =>
                c.Config.Labels.TryGetValue(DockerRunCreator.SocketCountLabelKey, out var countString)
                && int.TryParse(countString, out var count) ? acc + count : acc);

        var expectedCountainerCount = (run.Job.Buffers?.Count ?? 0) + socketCount + 1;

        if (containers.Count != expectedCountainerCount)
        {
            return run with { Status = RunStatus.Failed };
        }

        ContainerInspectResponse? exited;
        if ((exited = containers.FirstOrDefault(c => c.State.Status == "exited" && c.State.ExitCode != 0)) != null)
        {
            return run with { Status = RunStatus.Failed, FinishedAt = DateTimeOffset.Parse(exited.State.FinishedAt) };
        }

        if (containers.All(c => c.State.Status == "exited" && c.State.ExitCode == 0))
        {
            return run with { Status = RunStatus.Succeeded, FinishedAt = DateTimeOffset.Parse(containers.Max(c => c.State.FinishedAt)!) };
        }

        if (socketCount > 0)
        {
            var mainSocketContainer = containers.FirstOrDefault(c => c.Config.Labels.TryGetValue(DockerRunCreator.SocketCountLabelKey, out var hasSocket));
            // If the main container has opened a socket, we consider the run successful if all other containers have exited successfully
            if (mainSocketContainer != null && containers.Where(c => c.ID != mainSocketContainer.ID).All(c => c.State.Status == "exited" && c.State.ExitCode == 0))
            {
                return run with { Status = RunStatus.Succeeded, FinishedAt = DateTimeOffset.Parse(containers.Max(c => c.State.FinishedAt)!) };
            }
        }

        if (containers.Any(c => c.State.Running))
        {
            return run with { Status = RunStatus.Running };
        }

        return run with { Status = RunStatus.Failed };
    }
}
