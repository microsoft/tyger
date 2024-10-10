// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using k8s;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;
using static Tyger.ControlPlane.Compute.Kubernetes.KubernetesMetadata;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public class KubernetesRunUpdater : IRunUpdater
{
    private readonly IKubernetes _client;
    private readonly IRepository _repository;
    private readonly KubernetesApiOptions _k8sOptions;
    private readonly ILogger<KubernetesRunUpdater> _logger;

    public KubernetesRunUpdater(
        IRepository repository,
        IKubernetes client,
        IOptions<KubernetesApiOptions> k8sOptions,
        ILogger<KubernetesRunUpdater> logger)
    {
        _repository = repository;
        _logger = logger;
        _k8sOptions = k8sOptions.Value;
        _client = client;
    }

    public async Task<Run?> CancelRun(long id, CancellationToken cancellationToken)
    {
        var run = await _repository.GetRun(id, cancellationToken);

        if (run is null || run.Status.IsTerminal())
        {
            return run;
        }

        Run updatedRun = run with
        {
            Status = RunStatus.Canceled
        };

        await _repository.UpdateRunFromObservedState(new ObservedRunState(updatedRun), cancellationToken: cancellationToken);
        _logger.CancelingRun(id);

        return updatedRun;
    }
}
