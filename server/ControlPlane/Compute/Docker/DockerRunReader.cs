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

public class DockerRunReader : IRunReader
{
    private readonly DockerClient _client;
    private readonly IRepository _repository;

    public DockerRunReader(DockerClient client, IRepository repository)
    {
        _client = client;
        _repository = repository;
    }

    public async Task<Run?> GetRun(long id, CancellationToken cancellationToken)
    {
        var run = await _repository.GetRun(id, cancellationToken);
        if (run is null or { Final: true })
        {
            return run;
        }

        var containers = await (await _client.Containers
            .ListContainersAsync(
                new ContainersListParameters()
                {
                    All = true,
                    Filters = new Dictionary<string, IDictionary<string, bool>>
                    {
                        {"label", new Dictionary<string, bool>{{ $"tyger-run={id}", true } } }
                    }
                }, cancellationToken))
            .ToAsyncEnumerable()
            .SelectAwait(async c => await _client.Containers.InspectContainerAsync(c.ID, cancellationToken))
            .ToListAsync(cancellationToken);

        return UpdateRunFromContainers(run, containers);
    }

    public async Task<(IReadOnlyList<Run>, string? nextContinuationToken)> ListRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        (var partialRuns, var nextContinuationToken) = await _repository.GetRuns(limit, since, continuationToken, cancellationToken);
        if (partialRuns.All(r => r.Final))
        {
            return (partialRuns.AsReadOnly(), nextContinuationToken);
        }

        for (int i = 0; i < partialRuns.Count; i++)
        {
            var run = partialRuns[i];
            if (!run.Final)
            {
                partialRuns[i] = await GetRun(run.Id!.Value, cancellationToken) ?? run;
            }
        }

        return (partialRuns.AsReadOnly(), nextContinuationToken);
    }

    public async IAsyncEnumerable<Run> WatchRun(long id, [EnumeratorCancellation] CancellationToken cancellationToken)
    {
        var run = await GetRun(id, cancellationToken);
        if (run is null)
        {
            yield break;
        }

        yield return run;

        if (run.Status is RunStatus.Succeeded or RunStatus.Failed or RunStatus.Canceled)
        {
            yield break;
        }

        var channel = Channel.CreateUnbounded<object?>();
        var cancellation = new CancellationTokenSource();
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
                if (!channel.Writer.TryWrite(null))
                {
                    channel.Writer.WriteAsync(m).AsTask().Wait(cancellationToken);
                }
            }), cancellation.Token);

            async Task ScheduleFirstUpdate()
            {
                await Task.Delay(TimeSpan.FromSeconds(1), cancellationToken);
                await channel.Writer.WriteAsync(null, cancellationToken);
            }

            _ = ScheduleFirstUpdate();

            await foreach (var _ in channel.Reader.ReadAllAsync(cancellationToken))
            {
                var updatedRun = await GetRun(id, cancellationToken);
                if (updatedRun is null)
                {
                    yield break;
                }

                if (updatedRun.Status != run.Status)
                {
                    run = updatedRun;
                    yield return updatedRun;
                }

                if (run.Status is RunStatus.Succeeded or RunStatus.Failed or RunStatus.Canceled)
                {
                    yield break;
                }
            }
        }
        finally
        {
            cancellation.Cancel();
        }
    }

    public static Run UpdateRunFromContainers(Run run, IReadOnlyList<ContainerInspectResponse> containers)
    {
        if (run.Status is RunStatus.Canceled)
        {
            return run;
        }

        var socketCount = containers.Aggregate(0, (acc, c) => c.Config.Labels.TryGetValue(DockerRunCreator.SocketCountLabelKey, out var countString) && int.TryParse(countString, out var count) ? acc + count : acc);

        var expectedCountainerCount = (run.Job.Buffers?.Count ?? 0) + socketCount + 1;

        if (containers.Count != expectedCountainerCount)
        {
            return run with { Status = RunStatus.Failed };
        }

        if (containers.Any(c => c.State.Status == "exited" && c.State.ExitCode != 0))
        {
            return run with { Status = RunStatus.Failed };
        }

        if (containers.All(c => c.State.Status == "exited" && c.State.ExitCode == 0))
        {
            return run with { Status = RunStatus.Succeeded };
        }

        if (socketCount > 0)
        {
            var mainSocketContainer = containers.FirstOrDefault(c => c.Config.Labels.TryGetValue(DockerRunCreator.SocketCountLabelKey, out var hasSocket));
            // If the main container has opened a socket, we consider the run successful if all other containers have exited successfully
            if (mainSocketContainer != null && containers.Where(c => c.ID != mainSocketContainer.ID).All(c => c.State.Status == "exited" && c.State.ExitCode == 0))
            {
                return run with { Status = RunStatus.Succeeded };
            }
        }

        if (containers.Any(c => c.State.Running))
        {
            return run with { Status = RunStatus.Running };
        }

        return run with { Status = RunStatus.Failed };
    }
}
