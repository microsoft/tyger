// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public class KubernetesRunUpdater : IRunUpdater
{
    private readonly Repository _repository;
    private readonly ILogger<KubernetesRunUpdater> _logger;

    public KubernetesRunUpdater(
        Repository repository,
        ILogger<KubernetesRunUpdater> logger)
    {
        _repository = repository;
        _logger = logger;
    }

    public async Task<Run?> CancelRun(long id, CancellationToken cancellationToken)
    {
        _logger.CancelingRun(id);
        return await _repository.CancelRun(id, cancellationToken);
    }
}
