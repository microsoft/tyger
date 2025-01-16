// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Collections.Immutable;
using System.Threading.Channels;
using Docker.DotNet;
using Docker.DotNet.Models;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;

namespace Tyger.ControlPlane.Compute.Docker;

public class DockerRunUpdater : IRunUpdater
{
    private readonly Repository _repository;
    private readonly DockerClient _client;
    private readonly ILogger<DockerRunUpdater> _logger;

    private ImmutableDictionary<long, ImmutableArray<ChannelWriter<Run>>> _tagUpdateObservers = ImmutableDictionary<long, ImmutableArray<ChannelWriter<Run>>>.Empty;

    public DockerRunUpdater(
    Repository repository,
    DockerClient client,
    ILogger<DockerRunUpdater> logger)
    {
        _repository = repository;
        _logger = logger;
        _client = client;
    }


    public async Task<Run?> CancelRun(long id, CancellationToken cancellationToken)
    {
        _logger.CancelingRun(id);
        var updatedRun = await _repository.CancelRun(id, cancellationToken: cancellationToken);
        if (updatedRun is null)
        {
            return null;
        }

        var containers = await _client.Containers.ListContainersAsync(new ContainersListParameters()
        {
            All = true,
            Filters = new Dictionary<string, IDictionary<string, bool>>
            {
                {"label", new Dictionary<string, bool>{{ $"tyger-run={id}", true } } }
            }
        }, cancellationToken);

        foreach (var container in containers)
        {
            if (container.State is not "exited" or "dead")
            {
                try
                {
                    await _client.Containers.KillContainerAsync(container.ID, new ContainerKillParameters(), cancellationToken);
                }
                catch (DockerApiException e)
                {
                    _logger.FailedToKillContainer(container.ID, e);
                }
            }
        }

        return updatedRun;
    }

    public async Task<UpdateWithPreconditionResult<Run>> UpdateRunTags(RunUpdate runUpdate, string? eTagPrecondition, CancellationToken cancellationToken)
    {
        var res = await _repository.UpdateRunTags(runUpdate, eTagPrecondition, cancellationToken);

        if (res is UpdateWithPreconditionResult<Run>.Updated updated)
        {
            if (_tagUpdateObservers.TryGetValue(updated.Value.Id!.Value, out var observers))
            {
                foreach (var observer in observers)
                {
                    await observer.WriteAsync(updated.Value, cancellationToken);
                }
            }
        }

        return res;

    }

    public void RegisterTagUpdateObserver(long runId, ChannelWriter<Run> observer)
    {
        ImmutableInterlocked.AddOrUpdate(ref _tagUpdateObservers, runId, _ => [observer], (_, list) => list.Add(observer));
    }

    public void UnregisterTagUpdateObserver(long runId, ChannelWriter<Run> observer)
    {
        ImmutableInterlocked.Update(ref _tagUpdateObservers, d =>
        {
            if (d.TryGetValue(runId, out var arr))
            {
                var newArr = arr.Remove(observer);
                return newArr.IsEmpty ? d.Remove(runId) : d.SetItem(runId, newArr);
            }

            return d;
        });
    }
}
