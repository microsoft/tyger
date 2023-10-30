using k8s;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.Server.Database;
using Tyger.Server.Model;
using static Tyger.Server.Kubernetes.KubernetesMetadata;

namespace Tyger.Server.Kubernetes;

public class RunUpdater
{
    private readonly IKubernetes _client;
    private readonly IRepository _repository;
    private readonly KubernetesOptions _k8sOptions;
    private readonly ILogger<RunUpdater> _logger;

    public RunUpdater(
        IRepository repository,
        IKubernetes client,
        IOptions<KubernetesOptions> k8sOptions,
        ILogger<RunUpdater> logger)
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

        Run newRun = run with
        {
            Status = RunStatus.Canceling
        };

        await _repository.UpdateRun(newRun, cancellationToken: cancellationToken);
        _logger.CancelingRun(id);

        var annotation = new Dictionary<string, string>
        {
            { "Status", "Canceling" }
        };
        await _client.BatchV1.PatchNamespacedJobAsync(
                    new V1Patch(new { metadata = new { Annotations = annotation } }, V1Patch.PatchType.MergePatch),
                    JobNameFromRunId(id),
                    _k8sOptions.Namespace, cancellationToken: cancellationToken);

        return newRun;
    }
}
