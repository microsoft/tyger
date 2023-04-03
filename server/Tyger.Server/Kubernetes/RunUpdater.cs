using System.Globalization;
using System.Net;
using System.Runtime.CompilerServices;
using System.Text.RegularExpressions;
using k8s;
using k8s.Autorest;
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
        IKubernetes client,
        IRepository repository,
        IOptions<KubernetesOptions> k8sOptions,
        ILogger<RunUpdater> logger)
    {
        _client = client;
        _repository = repository;
        _k8sOptions = k8sOptions.Value;
        _logger = logger;
    }

    public async Task<Run?> CancelRun(long id, CancellationToken cancellationToken)
    {
        if (await _repository.GetRun(id, cancellationToken) is not (Run run, var final, _))
        {
            return null;
        }

        if (final || run.Status is "Succeeded" or "Failed")
        {
            return run;
        }

        Run newRun = run with
        {
            Status = "Failed",
            StatusReason = "Canceled"
        };

        await _repository.UpdateRun(newRun, cancellationToken: cancellationToken);
        _logger.LogInformation("Canceling job {0}", id);

        return newRun;
    }
}
