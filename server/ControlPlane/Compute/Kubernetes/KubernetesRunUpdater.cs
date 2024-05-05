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
        if (await _repository.GetRun(id, cancellationToken) is not (Run run, var final, _))
        {
            return null;
        }

        if (final || run.Status is RunStatus.Succeeded or RunStatus.Failed or RunStatus.Canceling or RunStatus.Canceled)
        {
            return run;
        }

        Run updatedRun = run with
        {
            Status = RunStatus.Canceling
        };

        await _repository.UpdateRun(updatedRun, cancellationToken: cancellationToken);
        _logger.CancelingRun(id);

        var annotation = new Dictionary<string, string>
        {
            { "Status", "Canceling" }
        };
        await _client.BatchV1.PatchNamespacedJobAsync(
                    new V1Patch(new { metadata = new { Annotations = annotation } }, V1Patch.PatchType.MergePatch),
                    JobNameFromRunId(id),
                    _k8sOptions.Namespace, cancellationToken: cancellationToken);

        return updatedRun;
    }
}
