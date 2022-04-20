using System.Globalization;
using System.IO.Pipelines;
using System.Net;
using System.Text.Json;
using System.Text.RegularExpressions;
using k8s;
using k8s.Autorest;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.Server.Buffers;
using Tyger.Server.Database;
using Tyger.Server.Logging;
using Tyger.Server.Model;
using Tyger.Server.StorageServer;
using ValidationException = System.ComponentModel.DataAnnotations.ValidationException;

namespace Tyger.Server.Kubernetes;

// Currently, each run has a single pod. The states a run can be in are:
// 1. Created in the DB, no pod yet
// 2. Pod created -> Database updated
// 3. Pod terminated (succeeded or failed) -> record in DB finalized -> pod deleted
// 4. Pod timed out -> record in DB finalized -> pod deleted
// 5. Pod deleted externally before the run completed.
//
// Normal state transition is 1 -> 2 -> 3
// During state 1, the run is not externally visible
// During state 2, the status of the run is determined by querying the database and augmenting the state with information from the pod
// States 3, 4, and 5 are observed in a background loop. Once the DB has been updated, the status of the run is obtained solely from the database.

public interface IKubernetesManager
{
    Task<Run> CreateRun(NewRun newRun, CancellationToken cancellationToken);
    Task<(IReadOnlyList<Run>, string? nextContinuationToken)> GetRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken);
    Task<Run?> GetRun(long id, CancellationToken cancellationToken);
    Task SweepRuns(CancellationToken cancellationToken);
}

public sealed class KubernetesManager : IKubernetesManager, ILogSource, IHostedService, IDisposable
{
    private const string SecretMountPath = "/etc/buffer-sas-tokens";

    private static readonly TimeSpan s_minDurationAfterArchivingBeforeDeletingPod = TimeSpan.FromSeconds(30);

    private static readonly Regex s_nodePoolFromNodeName = new("^aks-([a-zA-Z0-9]+)-", RegexOptions.Compiled);

    private readonly k8s.Kubernetes _client;
    private readonly IRepository _repository;
    private readonly BufferManager _bufferManager;
    private readonly ILogArchive _logArchive;
    private readonly KubernetesOptions _k8sOptions;
    private readonly StorageServerOptions _storageServerOptions;
    private readonly ILogger<KubernetesManager> _logger;
    private Task? _backgroundTask;
    private CancellationTokenSource? _backgroundCancellationTokenSource;

    public KubernetesManager(
        k8s.Kubernetes client,
        IRepository repository,
        BufferManager bufferManager,
        IOptions<KubernetesOptions> k8sOptions,
        IOptions<StorageServerOptions> storageServerOptions,
        ILogArchive logArchive,
        ILogger<KubernetesManager> logger)
    {
        _client = client;
        _repository = repository;
        _bufferManager = bufferManager;
        _logArchive = logArchive;
        _k8sOptions = k8sOptions.Value;
        _storageServerOptions = storageServerOptions.Value;
        _logger = logger;
    }

    public async Task<Run> CreateRun(NewRun newRun, CancellationToken cancellationToken)
    {
        (var codespec, var normalizedCodespecRef) = await GetCodespec(newRun.Codespec, cancellationToken);

        string? targetNodePool = null;
        bool targetsGpuNodePool = false;
        if (newRun.ComputeTarget != null)
        {
            IEnumerable<ClusterOptions> candidateClusters;
            if (!string.IsNullOrEmpty(newRun.ComputeTarget.Cluster))
            {
                if (!_k8sOptions.Clusters.TryGetValue(newRun.ComputeTarget.Cluster, out var cluster))
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Unknown cluster '{0}'", newRun.ComputeTarget.Cluster));
                }

                candidateClusters = new[] { cluster };
            }
            else
            {
                candidateClusters = _k8sOptions.Clusters.Values;
            }

            if (!string.IsNullOrEmpty(newRun.ComputeTarget.NodePool))
            {
                targetNodePool = newRun.ComputeTarget.NodePool;
                var pool = candidateClusters.Aggregate(default(NodePoolOptions), (found, curr) => curr.UserNodePools.TryGetValue(targetNodePool, out var np) ? np : found);
                if (pool == null)
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Unknown nodepool '{0}'", targetNodePool));
                }

                targetsGpuNodePool = DoesVmHaveSupportedGpu(pool.VmSize);

                if (!targetsGpuNodePool && codespec.Resources?.Gpu is ResourceQuantity q && q.ToDecimal() != 0)
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Nodepool '{0}' does not have GPUs and cannot satisfy GPU request '{1}'", targetNodePool, q));
                }
            }
        }

        Dictionary<string, Uri> bufferMap = await GetBufferMap(codespec.Buffers ?? new(null, null), newRun.Buffers ?? new(), cancellationToken);

        var run = await _repository.CreateRun(
            newRun with { Codespec = normalizedCodespecRef, TimeoutSeconds = newRun.TimeoutSeconds ?? (int)TimeSpan.FromHours(12).TotalSeconds },
            cancellationToken);

        var k8sId = PodNameFromRunId(run.Id);

        var labels = new Dictionary<string, string> { { "tyger", "run" } };
        var secret = new V1Secret
        {
            Metadata = new()
            {
                Name = k8sId,
                Labels = labels
            },
            StringData = bufferMap.ToDictionary(p => p.Key, p => p.Value.ToString()),
        };

        secret = await _client.CreateNamespacedSecretAsync(secret, _k8sOptions.Namespace, cancellationToken: cancellationToken);
        _logger.CreatedSecret(k8sId);

        var env = new List<V1EnvVar> { new("MRD_STORAGE_URI", _storageServerOptions.Uri) };
        if (codespec.Env != null)
        {
            env.AddRange(codespec.Env.Select(p => new V1EnvVar(p.Key, p.Value)));
        }

        env.AddRange(bufferMap.Select(p => new V1EnvVar($"{p.Key.ToUpperInvariant()}_BUFFER_URI_FILE", $"{SecretMountPath}/{p.Key}")));

        var container = new V1Container
        {
            Name = "runner",
            Image = codespec.Image,
            Command = codespec.Command,
            Args = codespec.Args,
            Env = env,
            VolumeMounts = new V1VolumeMount[] {
                new()
                {
                    Name = "buffers",
                    MountPath = SecretMountPath,
                    ReadOnlyProperty = true
                },
            }
        };

        if (codespec.Resources != null)
        {
            var resources = new Dictionary<string, ResourceQuantity>();
            if (codespec.Resources.Cpu != null)
            {
                resources["cpu"] = codespec.Resources.Cpu;
            }

            if (codespec.Resources.Memory != null)
            {
                resources["memory"] = codespec.Resources.Memory;
            }

            if (codespec.Resources.Gpu != null)
            {
                resources["nvidia.com/gpu"] = codespec.Resources.Gpu;
            }

            container.Resources = new() { Limits = resources, Requests = resources };
        }

        var pod = new V1Pod
        {
            Metadata = new()
            {
                Name = k8sId,
                Annotations = new Dictionary<string, string>
                {
                    { "run", JsonSerializer.Serialize(run) },
                },
                Labels = labels,
                OwnerReferences = new V1OwnerReference[]
                {
                    new(secret.ApiVersion, secret.Kind, secret.Metadata.Name, secret.Metadata.Uid)
                },
                Finalizers = new[] { "research.microsoft.com/tyger-finalizer" }
            },
            Spec = new()
            {
                Containers = new[] { container },
                RestartPolicy = "OnFailure",
                Volumes = new V1Volume[]
                {
                    new()
                    {
                        Name = "buffers",
                        Secret = new() {SecretName = k8sId}
                    }
                },
                Tolerations = new List<V1Toleration>
                {
                    new() { Key = "tyger", OperatorProperty= "Equal", Value = "run", Effect = "NoSchedule" } // allow this to run on a user nodepools
                },
                NodeSelector = new Dictionary<string, string> { { "tyger", "run" } } // require this to run on a user nodepool
            }
        };

        if (targetNodePool != null)
        {
            pod.Spec.NodeSelector.Add("agentpool", targetNodePool);
        }

        if (codespec.Resources?.Gpu != null || targetsGpuNodePool)
        {
            pod.Spec.Tolerations.Add(new() { Key = "sku", OperatorProperty = "Equal", Value = "gpu", Effect = "NoSchedule" });
        }

        await _client.CreateNamespacedPodAsync(pod, _k8sOptions.Namespace, cancellationToken: cancellationToken);
        await _repository.UpdateRun(run, podCreated: true, cancellationToken: cancellationToken);

        _logger.CreatedRun(k8sId);
        return run;
    }

    private static bool DoesVmHaveSupportedGpu(string vmSize)
    {
        return vmSize.StartsWith("Standard_N", StringComparison.OrdinalIgnoreCase) &&
            !vmSize.EndsWith("_v4", StringComparison.OrdinalIgnoreCase); // unsupported AMD GPU
    }

    private async Task<(Codespec codespec, string normalizedRef)> GetCodespec(string codespecRef, CancellationToken cancellationToken)
    {
        var codespecTokens = codespecRef.Split("/versions/");
        Codespec? codespec;
        int version;

        switch (codespecTokens.Length)
        {
            case 1:
                var reponse = await _repository.GetLatestCodespec(codespecTokens[0], cancellationToken);
                if (reponse == null)
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The codespec '{0}' was not found", codespecTokens[0]));
                }

                (codespec, version) = reponse.Value;
                break;
            case 2:
                if (!int.TryParse(codespecTokens[1], out version))
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "'{0}' is not a valid codespec version", codespecTokens[1]));
                }

                codespec = await _repository.GetCodespecAtVersion(codespecTokens[0], version, cancellationToken);
                if (codespec != null)
                {
                    break;
                }

                // See if it's just the version number that was not found
                var latestResponse = await _repository.GetLatestCodespec(codespecTokens[0], cancellationToken);
                if (latestResponse == null)
                {
                    throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The codespec '{0}' was not found", codespecTokens[0]));
                }

                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The version '{0}' of codespec '{1}' was not found. The latest version is '{2}'.", version, codespecTokens[0], latestResponse.Value.version));

            default:
                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The codespec '{0}' is invalid. The value should be in the form '<codespec_name>' or '<codespec_name>/versions/<version_number>'.", codespecRef));
        }

        var normalizedRef = $"{codespecTokens[0]}/versions/{version}";
        _logger.FoundCodespec(normalizedRef);
        return (codespec, normalizedRef);
    }

    private async Task<Dictionary<string, Uri>> GetBufferMap(BufferParameters parameters, Dictionary<string, string> arguments, CancellationToken cancellationToken)
    {
        arguments = new Dictionary<string, string>(arguments, StringComparer.OrdinalIgnoreCase);
        IEnumerable<(string param, bool writeable)> combinedParameters = (parameters.Inputs?.Select(param => (param, false)) ?? Enumerable.Empty<(string, bool)>())
            .Concat(parameters.Outputs?.Select(param => (param, true)) ?? Enumerable.Empty<(string, bool)>());

        var outputMap = new Dictionary<string, Uri>();

        foreach (var param in combinedParameters)
        {
            if (!arguments.TryGetValue(param.param, out var bufferId))
            {
                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The run is missing required buffer argument '{0}'", param.param));
            }

            var bufferAccess = await _bufferManager.CreateBufferAccessString(bufferId, param.writeable, cancellationToken);
            if (bufferAccess == null)
            {
                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The buffer '{0}' was not found", bufferId));
            }

            outputMap[param.param] = bufferAccess.Uri;
            arguments.Remove(param.param);
        }

        foreach (var arg in arguments)
        {
            throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Buffer argument '{0}' does not correspond to a buffer parameter on the codespec", arg));
        }

        return outputMap;
    }

    private static string PodNameFromRunId(long id) => $"run-{id}";

    private static long RunIdFromPodName(string podName) => long.Parse(podName[4..], CultureInfo.InvariantCulture);

    public async Task<(IReadOnlyList<Run>, string? nextContinuationToken)> GetRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        (var partialRuns, var nextContinuationToken) = await _repository.GetRuns(limit, since, continuationToken, cancellationToken);
        var finalRuns = partialRuns.Select(r => r.run).ToList();
        if (partialRuns.All(r => r.final))
        {
            return (finalRuns, nextContinuationToken);
        }

        var podIdsToFind = new Dictionary<string, int>();

        for (int i = 0; i < partialRuns.Count; i++)
        {
            (var run, var final) = partialRuns[i];
            if (!final)
            {
                podIdsToFind.Add(PodNameFromRunId(run.Id), i);
            }
        }

        string? k8sContinuation = null;
        List<int>? zombieRunIndexes = null;
        do
        {
            var pods = await _client.ListNamespacedPodAsync(_k8sOptions.Namespace, continueParameter: k8sContinuation, labelSelector: "tyger=run", cancellationToken: cancellationToken);
            foreach (var pod in pods.Items)
            {
                if (podIdsToFind.TryGetValue(pod.Metadata.Name, out var index))
                {
                    if (GetRunFromPod(pod) is Run run)
                    {
                        finalRuns[index] = run;
                    }
                    else
                    {
                        (zombieRunIndexes ??= new()).Add(index);
                    }

                    podIdsToFind.Remove(pod.Metadata.Name);
                    if (podIdsToFind.Count == 0)
                    {
                        goto Done;
                    }
                }
            }

            k8sContinuation = pods.Metadata.ContinueProperty;
        } while (k8sContinuation != null);

Done:

        foreach ((var missingPodId, var index) in podIdsToFind)
        {
            _logger.RunMissingPod(missingPodId);
            (zombieRunIndexes ??= new()).Add(index);
        }

        if (zombieRunIndexes != null)
        {
            zombieRunIndexes.Sort();
            for (int i = zombieRunIndexes.Count - 1; i >= 0; i--)
            {
                finalRuns.RemoveAt(zombieRunIndexes[i]);
            }
        }

        return (finalRuns, nextContinuationToken);
    }

    public async Task<Run?> GetRun(long id, CancellationToken cancellationToken)
    {
        var repositoryResponse = await _repository.GetRun(id, cancellationToken);
        if (repositoryResponse == null)
        {
            return null;
        }

        if (repositoryResponse.Value.final)
        {
            return repositoryResponse.Value.run;
        }

        try
        {
            var pod = await _client.ReadNamespacedPodAsync(PodNameFromRunId(id), _k8sOptions.Namespace, cancellationToken: cancellationToken);
            return GetRunFromPod(pod);
        }
        catch (HttpOperationException e) when (e.Response.StatusCode == HttpStatusCode.NotFound)
        {
            _logger.RunMissingPod(PodNameFromRunId(id));
            return null;
        }
    }

    public async Task<bool> TryGetLogs(long runId, GetLogsOptions options, PipeWriter outputWriter, CancellationToken cancellationToken)
    {
        (var runExists, var logsArchivedAt) = await _repository.AreRunLogsArchived(runId, cancellationToken);
        switch (runExists, logsArchivedAt)
        {
            case (false, _):
                return false;
            case (true, null):
                if (await GetLogsFromPod(runId, options, cancellationToken) is Stream podStream)
                {
                    await podStream.CopyToAsync(outputWriter, cancellationToken);
                    return true;
                }

                return false;
            default:
                return await _logArchive.TryGetLogs(runId, options, outputWriter, cancellationToken);
        }
    }

    // We need to do the HTTP request ourselves because the sinceTime parameter is missing https://github.com/kubernetes-client/csharp/issues/829
    private async Task<Stream?> GetLogsFromPod(long id, GetLogsOptions options, CancellationToken cancellationToken)
    {
Start:
        var qs = QueryString.Empty;
        if (options.IncludeTimestamps)
        {
            qs = qs.Add("timestamps", "true");
        }

        if (options.TailLines.HasValue)
        {
            qs = qs.Add("tailLines", options.TailLines.Value.ToString(CultureInfo.InvariantCulture));
        }

        if (options.Since.HasValue)
        {
            qs = qs.Add("sinceTime", options.Since.Value.ToString("o"));
        }

        if (options.Follow)
        {
            qs = qs.Add("follow", "true");
        }

        if (options.Previous)
        {
            qs = qs.Add("previous", "true");
        }

        var uri = new Uri(_client.BaseUri, $"api/v1/namespaces/{_k8sOptions.Namespace}/pods/{PodNameFromRunId(id)}/log{qs.ToUriComponent()}");

        var requestMessage = new HttpRequestMessage(HttpMethod.Get, uri);
        if (_client.Credentials != null)
        {
            cancellationToken.ThrowIfCancellationRequested();
            await _client.Credentials.ProcessHttpRequestAsync(requestMessage, cancellationToken).ConfigureAwait(false);
        }

        var response = await _client.HttpClient.SendAsync(requestMessage, HttpCompletionOption.ResponseHeadersRead, cancellationToken);

        switch (response.StatusCode)
        {
            case HttpStatusCode.OK:
                var logs = await response.Content.ReadAsStreamAsync(cancellationToken);
                if (options.IncludeTimestamps)
                {
                    return TimestampedLogReformatter.WithReformattedLines(logs, cancellationToken);
                }

                return logs;
            case HttpStatusCode.NoContent:
                return new MemoryStream(Array.Empty<byte>());
            case HttpStatusCode.NotFound:
                return null;
            case HttpStatusCode.BadRequest when options.Previous:
                throw new ValidationException("Logs from a previous execution were not found.");
            case HttpStatusCode.BadRequest:
                // likely means the pod has not started yet.

                string resourceVersion;
                try
                {
                    var pod = await _client.ReadNamespacedPodAsync(PodNameFromRunId(id), _k8sOptions.Namespace, cancellationToken: cancellationToken);
                    if (IsPodRunningOrTerminated(pod))
                    {
                        goto Start;
                    }

                    if (!options.Follow)
                    {
                        // no logs yet.
                        return new MemoryStream(Array.Empty<byte>());
                    }

                    resourceVersion = pod.Metadata.ResourceVersion;
                }
                catch (HttpOperationException e) when (e.Response.StatusCode == HttpStatusCode.NotFound)
                {
                    return null;
                }

                var podlistResp = _client.ListNamespacedPodWithHttpMessagesAsync(
                        _k8sOptions.Namespace,
                        fieldSelector: $"metadata.name={PodNameFromRunId(id)}",
                        watch: true,
                        resourceVersion: resourceVersion,
                        cancellationToken: cancellationToken);

                await foreach (var (type, item) in podlistResp.WatchAsync<V1Pod, V1PodList>())
                {
                    if (type == WatchEventType.Deleted)
                    {
                        return null;
                    }

                    if (IsPodRunningOrTerminated(item))
                    {
                        goto Start;
                    }
                }

                goto Start;

            default:
                throw new InvalidOperationException($"Unexpected status code '{response.StatusCode} from cluster. {await response.Content.ReadAsStringAsync(cancellationToken)}");
        }
    }

    private static bool IsPodRunningOrTerminated(V1Pod pod)
    {
        return pod.Status.ContainerStatuses is { Count: > 0 } && pod.Status.ContainerStatuses[0].State.Waiting == null;
    }

    private Run? GetRunFromPod(V1Pod pod)
    {
        var serializedRun = pod.Metadata.Annotations?["run"];
        Run? run;
        if (serializedRun == null || (run = JsonSerializer.Deserialize<Run>(serializedRun)) == null)
        {
            _logger.InvalidRunAnnotation(pod.Metadata.Name);
            return null;
        }

        if (!string.IsNullOrEmpty(pod.Spec.NodeName) && s_nodePoolFromNodeName.Match(pod.Spec.NodeName) is { Success: true } m)
        {
            run = run with { ComputeTarget = new(_k8sOptions.Clusters.Keys.Single(), m.Groups[1].Value) };
        }

        return GetRunUpdatedWithStatusAndTimes(run, pod);
    }

    private Run GetRunUpdatedWithStatusAndTimes(Run run, V1Pod pod)
    {
        if (pod.Status.ContainerStatuses?.Count == 1)
        {
            var state = pod.Status.ContainerStatuses[0].State;
            if (state.Waiting != null)
            {
                return run with { Status = state.Waiting.Reason };
            }

            if (state.Running != null)
            {
                return run with { Status = "Running", StartedAt = state.Running.StartedAt };
            }

            if (state.Terminated != null)
            {
                return run with { Status = state.Terminated.Reason, StartedAt = state.Terminated.StartedAt, FinishedAt = state.Terminated.FinishedAt };
            }
        }

        if (pod.Status.Phase == "Pending")
        {
            return run with { Status = pod.Status.Phase };
        }

        _logger.UnableToDeterminePodPhase(pod.Metadata.Name);
        return run;
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

        // wait for the background task to complete, but give up once the cancellation token is cancelled.
        var tcs = new TaskCompletionSource<bool>();
        cancellationToken.Register(s => ((TaskCompletionSource<bool>)s!).SetResult(true), tcs);
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
            var runs = await _repository.GetPageOfRunsThatNeverGotAPod(cancellationToken);
            if (runs.Count == 0)
            {
                break;
            }

            foreach (var run in runs)
            {
                _logger.DeletingRunThatNeverCreatedAPod(run.Id);
                await DeleteRunResources(run.Id, cancellationToken);
                await _repository.DeleteRun(run.Id, cancellationToken);
            }
        }

        // Now go though the list of pods and update database records if terminated
        string? continuation = null;
        do
        {
            var pods = await _client.ListNamespacedPodAsync(_k8sOptions.Namespace, continueParameter: continuation, labelSelector: "tyger=run", cancellationToken: cancellationToken);

            foreach (var pod in pods.Items)
            {
                switch (GetRunFromPod(pod), pod)
                {
                    case (null, _):
                        await _repository.DeleteRun(RunIdFromPodName(pod.Metadata.Name), cancellationToken);
                        await DeleteRunResources(RunIdFromPodName(pod.Metadata.Name), cancellationToken);
                        break;
                    case (var run, { Status.Phase: "Succeeded" or "Failed" }):
                        switch ((await _repository.AreRunLogsArchived(run.Id, cancellationToken)).timeArchived)
                        {
                            case null:
                                await ArchiveLogs(run, cancellationToken);
                                break;
                            case var time when DateTimeOffset.UtcNow - time > s_minDurationAfterArchivingBeforeDeletingPod:
                                _logger.FinalizingTerminatedRun(run.Id, run.Status);
                                await _repository.UpdateRun(run, final: true, cancellationToken: cancellationToken);
                                await DeleteRunResources(run.Id, cancellationToken);
                                break;
                            default:
                                break;
                        }

                        break;
                    default:
                        break;
                }
            }

            continuation = pods.Metadata.ContinueProperty;
        } while (continuation != null);

        // now clean up timed out jobs
        while (true)
        {
            var runs = await _repository.GetPageOfTimedOutRuns(cancellationToken);
            if (runs.Count == 0)
            {
                break;
            }

            foreach (var run in runs)
            {
                switch ((await _repository.AreRunLogsArchived(run.Id, cancellationToken)).timeArchived)
                {
                    case null:
                        await ArchiveLogs(run, cancellationToken);
                        break;
                    case var time when DateTimeOffset.UtcNow - time > s_minDurationAfterArchivingBeforeDeletingPod:
                        _logger.FinalizingTimedOutRun(run.Id);
                        await DeleteRunResources(run.Id, cancellationToken);
                        await _repository.UpdateRun(run with { Status = "TimedOut" }, final: true, cancellationToken: cancellationToken);
                        break;
                    default:
                        break;
                }
            }
        }

        _logger.BackgroundSweepCompleted();
    }

    private async Task ArchiveLogs(Run run, CancellationToken cancellationToken)
    {
        using var logStream = await GetLogsFromPod(run.Id, new GetLogsOptions { IncludeTimestamps = true }, cancellationToken);
        if (logStream is null)
        {
            return;
        }

        await _logArchive.ArchiveLogs(run.Id, logStream, cancellationToken);
        await _repository.UpdateRun(run, logsArchivedAt: DateTimeOffset.UtcNow, cancellationToken: cancellationToken);
    }

    private async Task DeleteRunResources(long runId, CancellationToken cancellationToken)
    {
        string podName = PodNameFromRunId(runId);

        try
        {
            // clear finalizer on Pod
            await _client.PatchNamespacedPodAsync(
                new V1Patch(new { metadata = new { finalizers = Array.Empty<string>() } }, V1Patch.PatchType.MergePatch),
                podName,
                _k8sOptions.Namespace,
                cancellationToken: cancellationToken);
        }
        catch (HttpOperationException e) when (e.Response.StatusCode == HttpStatusCode.NotFound)
        {
        }

        try
        {
            await _client.DeleteNamespacedSecretAsync(podName, _k8sOptions.Namespace, propagationPolicy: "Foreground", cancellationToken: cancellationToken);
        }
        catch (HttpOperationException e) when (e.Response.StatusCode == HttpStatusCode.NotFound)
        {
            try
            {
                await _client.DeleteNamespacedPodAsync(podName, _k8sOptions.Namespace, propagationPolicy: "Foreground", cancellationToken: cancellationToken);
            }
            catch (HttpOperationException e2) when (e2.Response.StatusCode == HttpStatusCode.NotFound)
            {
            }
        }
    }

    public void Dispose()
    {
        if (_backgroundTask is { IsCompleted: true })
        {
            _backgroundTask.Dispose();
        }
    }
}
