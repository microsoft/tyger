// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Docker.DotNet;
using Docker.DotNet.Models;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;

namespace Tyger.ControlPlane.Compute.Docker;

public class DockerRunUpdater : IRunUpdater
{
    private readonly IRepository _repository;
    private readonly DockerClient _client;
    private readonly ILogger<DockerRunUpdater> _logger;

    public DockerRunUpdater(
    IRepository repository,
    DockerClient client,
    ILogger<DockerRunUpdater> logger)
    {
        _repository = repository;
        _logger = logger;
        _client = client;
    }
    public async Task<Run?> CancelRun(long id, CancellationToken cancellationToken)
    {
        var run = await _repository.GetRun(id, cancellationToken);

        if (run is null or { Final: true } or { Status: RunStatus.Succeeded or RunStatus.Failed or RunStatus.Canceling or RunStatus.Canceled })
        {
            return run;
        }

        Run updatedRun = run with
        {
            Status = RunStatus.Canceled,
            StatusReason = "Canceled by user"
        };

        await _repository.UpdateRun(updatedRun, cancellationToken: cancellationToken);
        _logger.CancelingRun(id);

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
}
