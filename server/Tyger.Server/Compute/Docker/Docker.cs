using System.Collections.Immutable;
using System.ComponentModel.DataAnnotations;
using System.IO.Pipelines;
using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;
using System.Text.RegularExpressions;
using System.Threading.Channels;
using Azure.Storage.Blobs.Models;
using Docker.DotNet;
using Docker.DotNet.Models;
using Microsoft.Extensions.Options;
using Npgsql.Replication.PgOutput.Messages;
using Tyger.Server.Buffers;
using Tyger.Server.Database;
using Tyger.Server.Database.Migrations;
using Tyger.Server.Logging;
using Tyger.Server.Model;
using Tyger.Server.Runs;

namespace Tyger.Server.Compute.Docker;

public static class Docker
{
    public static void AddDocker(this IHostApplicationBuilder builder)
    {
        builder.Services.AddOptions<DockerSecretOptions>().BindConfiguration("compute:docker").ValidateDataAnnotations().ValidateOnStart();

        builder.Services.AddSingleton(sp => new DockerClientConfiguration().CreateClient());

        builder.Services.AddSingleton<IReplicaDatabaseVersionProvider, DockerReplicaDatabaseVersionProvider>();
        builder.Services.AddSingleton<IRunCreator, DockerRunCreator>();
        builder.Services.AddSingleton(sp => (IHostedService)sp.GetRequiredService<IRunCreator>());
        builder.Services.AddSingleton<IRunReader, DockerRunReader>();
        builder.Services.AddSingleton<IRunUpdater, DockerRunUpdater>();
        builder.Services.AddSingleton<ILogSource, DockerLogSource>();
    }
}

public class DockerSecretOptions
{
    [Required]
    public required string RunSecretsPath { get; set; }
    public string? RunSecretsHostPath { get; set; }
}

public partial class DockerRunCreator : RunCreatorBase, IRunCreator, IHostedService
{
    private readonly DockerClient _client;
    private readonly ILogger<DockerRunCreator> _logger;
    private readonly string _bufferSidecarImage;
    private readonly DockerSecretOptions _dockerSecretOptions;

    public DockerRunCreator(
        DockerClient client,
        IRepository repository,
        BufferManager bufferManager,
        IOptions<BufferOptions> bufferOptions,
        IOptions<DockerSecretOptions> dockerSecretOptions,
        ILogger<DockerRunCreator> logger)
    : base(repository, bufferManager)
    {
        _client = client;
        _logger = logger;
        _bufferSidecarImage = bufferOptions.Value.BufferSidecarImage;
        _dockerSecretOptions = dockerSecretOptions.Value;
    }

    public async Task<Run> CreateRun(Run newRun, CancellationToken cancellationToken)
    {
        if (newRun.Worker != null)
        {
            throw new ValidationException("Runs with workers are only supported on Kubernetes");
        }

        if (await GetCodespec(newRun.Job.Codespec, cancellationToken) is not JobCodespec jobCodespec)
        {
            throw new ArgumentException($"The codespec for the job is required to be a job codespec");
        }

        newRun = newRun with
        {
            Cluster = null,
            Job = newRun.Job with
            {
                Codespec = jobCodespec.ToCodespecRef()
            }
        };

        if (newRun.Job.Buffers == null)
        {
            newRun = newRun with { Job = newRun.Job with { Buffers = [] } };
        }

        if (newRun.Job.Tags == null)
        {
            newRun = newRun with { Job = newRun.Job with { Tags = [] } };
        }

        var bufferMap = await GetBufferMap(jobCodespec.Buffers, newRun.Job.Buffers, newRun.Job.Tags, cancellationToken);

        try
        {
            await _client.Images.InspectImageAsync(jobCodespec.Image, cancellationToken: cancellationToken);
        }
        catch (DockerImageNotFoundException)
        {
            throw new ValidationException($"The image '{jobCodespec.Image}' was not found on the system. Run `docker pull {jobCodespec.Image}` and try again.");
        }

        var run = await Repository.CreateRun(newRun, cancellationToken);

        var relativeSecretsPath = run.Id.ToString()!;
        var relativePipesPath = Path.Combine(relativeSecretsPath, "pipes");
        var relativeAccessFilesPath = Path.Combine(relativeSecretsPath, "access-files");
        var relativeTombstonePath = Path.Combine(relativeSecretsPath, "tombstone");

        var absoluteSecretsBase = _dockerSecretOptions.RunSecretsPath;
        var absoluteHostSecretsBase = string.IsNullOrEmpty(_dockerSecretOptions.RunSecretsHostPath) ? absoluteSecretsBase : _dockerSecretOptions.RunSecretsHostPath;
        var absoluteContainerSecretsBase = "/run/secrets";

        var env = jobCodespec.Env ?? [];

        Directory.CreateDirectory(Path.Combine(absoluteSecretsBase, relativePipesPath));
        Directory.CreateDirectory(Path.Combine(absoluteSecretsBase, relativeAccessFilesPath));
        Directory.CreateDirectory(Path.Combine(absoluteSecretsBase, relativeTombstonePath));

        var labels = ImmutableDictionary<string, string>.Empty.Add("tyger-run", run.Id?.ToString()!);

        foreach ((var bufferName, (bool write, Uri accessUri)) in bufferMap)
        {
            var pipeName = bufferName + ".pipe";
            var pipePath = Path.Combine(absoluteSecretsBase, relativePipesPath, pipeName);
            MkFifo(pipePath, 0x1FF);
            ChMod(pipePath, 0x1FF);

            var containerPipePath = Path.Combine(absoluteContainerSecretsBase, relativePipesPath, Path.GetFileName(pipePath));
            env[$"{bufferName.ToUpperInvariant()}_PIPE"] = containerPipePath;

            var accessFileName = bufferName + ".access";
            var accessFilePath = Path.Combine(absoluteSecretsBase, relativeAccessFilesPath, accessFileName);
            File.WriteAllText(accessFilePath, accessUri.ToString());
            var containerAccessFilePath = Path.Combine(absoluteContainerSecretsBase, relativeAccessFilesPath, accessFileName);

            var sidecarContainerParameters = new CreateContainerParameters
            {
                Image = _bufferSidecarImage,
                Name = $"tyger-run-{run.Id}-sidecar-{bufferName}",
                Labels = labels.Add("tyger-run-container-name", $"{bufferName}-buffer-sidecar"),
                Cmd =
                [
                    write ? "write" : "read",
                    containerAccessFilePath,
                    write ? "-i" : "-o",
                    containerPipePath,
                    "--tombstone",
                    Path.Combine(absoluteContainerSecretsBase, relativeTombstonePath, "tombstone.txt")
                ],
                HostConfig = new()
                {
                    Mounts =
                    [
                        new()
                        {
                            Source = Path.Combine(absoluteHostSecretsBase, relativePipesPath),
                            Target = Path.Combine(absoluteContainerSecretsBase, relativePipesPath),
                            Type = "bind",
                            ReadOnly = false,
                        },
                        new()
                        {
                            Source = Path.Combine(absoluteHostSecretsBase, relativeAccessFilesPath, accessFileName),
                            Target = Path.Combine(absoluteContainerSecretsBase, relativeAccessFilesPath, accessFileName),
                            Type = "bind",
                            ReadOnly = true,
                        },
                        new()
                        {
                            Source = Path.Combine(absoluteHostSecretsBase, relativeTombstonePath),
                            Target = Path.Combine(absoluteContainerSecretsBase, relativeTombstonePath),
                            Type = "bind",
                            ReadOnly = true,
                        }
                    ],
                    NetworkMode = "host"
                },
            };

            var sidecarCreateResponse = await _client.Containers.CreateContainerAsync(sidecarContainerParameters, cancellationToken);
            await _client.Containers.StartContainerAsync(sidecarCreateResponse.ID, null, cancellationToken);
        }

        var mainContainerParameters = new CreateContainerParameters
        {
            Image = jobCodespec.Image,
            Name = $"tyger-run-{run.Id}-main",
            WorkingDir = jobCodespec.WorkingDir,
            Env = env.Select(e => $"{e.Key}={e.Value}").ToList(),
            Cmd = jobCodespec.Args?.Select(a => ExpandVariables(a, env))?.ToList(),
            Entrypoint = jobCodespec.Command is { Length: > 0 } ? jobCodespec.Command.Select(a => ExpandVariables(a, env)).ToList() : null,
            Labels = labels.Add("tyger-run-container-name", $"main"),
            HostConfig = new()
            {
                Mounts =
                [
                    new()
                    {
                        Source = Path.Combine(absoluteHostSecretsBase, relativePipesPath),
                        Target = Path.Combine(absoluteContainerSecretsBase, relativePipesPath),
                        Type = "bind",
                        ReadOnly = false,
                    }
                ]
            }
        };

        var createResponse = await _client.Containers.CreateContainerAsync(mainContainerParameters, cancellationToken);
        var containerId = createResponse.ID;

        var monitorCancellation = new CancellationTokenSource();

        void WriteTombstone()
        {
            try
            {
                monitorCancellation.Cancel();
            }
            catch
            {
            }

            File.WriteAllText(Path.Combine(absoluteSecretsBase, relativeTombstonePath, "tombstone.txt"), "tombstone");
        }

        _ = _client.System.MonitorEventsAsync(new ContainerEventsParameters()
        {
            Filters = new Dictionary<string, IDictionary<string, bool>>
            {
                {"container", new Dictionary<string, bool>{{ containerId, true } } }
            }
        }, new Progress<Message>(m =>
        {
            if (m.Action is "die" or "destroy" or "stop" or "kill")
            {
                WriteTombstone();
            }
        }), monitorCancellation.Token);

        try
        {
            await _client.Containers.StartContainerAsync(containerId, null, cancellationToken);
        }
        catch (DockerApiException e)
        {
            WriteTombstone();

            throw new ValidationException($"Failed to start the run: {e.Message}");
            throw;
        }

        await Repository.UpdateRun(run, resourcesCreated: true, cancellationToken: cancellationToken);
        _logger.CreatedRun(run.Id!.Value);
        return run with { Status = RunStatus.Running };
    }

    [LibraryImport("libSystem.Native", EntryPoint = "SystemNative_MkFifo", SetLastError = true, StringMarshalling = StringMarshalling.Utf8)]
    private static partial int MkFifo(string pathName, uint mode);

    [LibraryImport("libSystem.Native", EntryPoint = "SystemNative_ChMod", SetLastError = true, StringMarshalling = StringMarshalling.Utf8)]
    internal static partial int ChMod(string path, int mode);

    public static string ExpandVariables(string input, IDictionary<string, string> environment)
    {
        return EnvironmentVariableExpansionRegex().Replace(input, match =>
        {
            if (match.Value.StartsWith("$$", StringComparison.Ordinal))
            {
                // Escaped variable, remove one $
                return $"${match.Groups[1].Value}";
            }
            else
            {
                string variable = match.Groups[1].Value;
                if (environment.TryGetValue(variable, out string? value))
                {
                    return value!;
                }
                else
                {
                    return match.Value;
                }
            }
        });
    }

    public Task StartAsync(CancellationToken cancellationToken)
    {
        Directory.CreateDirectory(_dockerSecretOptions.RunSecretsPath);
        return Task.CompletedTask;
    }

    public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;

    [GeneratedRegex(@"\$\(([^)]+)\)|\$\$([^)]+)")]
    private static partial Regex EnvironmentVariableExpansionRegex();
}

public class DockerRunReader : IRunReader
{
    private readonly DockerClient _client;
    private readonly IRepository _repository;
    private readonly ILogger<DockerRunReader> _logger;

    public DockerRunReader(DockerClient client, IRepository repository, ILogger<DockerRunReader> logger)
    {
        _client = client;
        _repository = repository;
        _logger = logger;
    }

    public async Task<Run?> GetRun(long id, CancellationToken cancellationToken)
    {
        if (await _repository.GetRun(id, cancellationToken) is not (Run run, var final, _))
        {
            return null;
        }

        if (final)
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
        if (partialRuns.All(r => r.final))
        {
            return (partialRuns.Select(r => r.run).ToList(), nextContinuationToken);
        }

        for (int i = 0; i < partialRuns.Count; i++)
        {
            (var run, var final) = partialRuns[i];
            if (!final)
            {
                partialRuns[i] = (await GetRun(run.Id!.Value, cancellationToken) ?? run, false);
            }
        }

        return (partialRuns.Select(r => r.run).ToList(), nextContinuationToken);
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

    private static Run UpdateRunFromContainers(Run run, IReadOnlyList<ContainerInspectResponse> containers)
    {
        var expectedCountainerCount = (run.Job.Buffers?.Count ?? 0) + 1;

        if (containers.Count != expectedCountainerCount)
        {
            return run with { Status = RunStatus.Failed };
        }

        if (containers.Any(c => c.State.Running))
        {
            return run with { Status = RunStatus.Running };
        }

        if (containers.All(c => c.State.Status == "exited" && c.State.ExitCode == 0))
        {
            return run with { Status = RunStatus.Succeeded };
        }

        return run with { Status = RunStatus.Failed };
    }
}

public class DockerRunUpdater : IRunUpdater
{
    public Task<Run?> CancelRun(long id, CancellationToken cancellationToken)
    {
        throw new NotImplementedException();
    }
}

public class DockerLogSource : ILogSource
{
    private readonly DockerClient _client;

    public DockerLogSource(DockerClient client)
    {
        _client = client;
    }

    public async Task<Pipeline?> GetLogs(long runId, GetLogsOptions options, CancellationToken cancellationToken)
    {
        var containers = await _client.Containers.ListContainersAsync(new ContainersListParameters()
        {
            All = true,
            Filters = new Dictionary<string, IDictionary<string, bool>>
            {
                {"label", new Dictionary<string, bool>{{ $"tyger-run={runId}", true } } }
            }
        }, cancellationToken);

        if (containers.Count == 0)
        {
            return null;
        }

        var pipelineSources = containers.ToAsyncEnumerable()
            .SelectAwait(async c => await GetContainerLogs(
                c.ID,
                c.Labels.TryGetValue("tyger-run-container-name", out var prefix) ? $"[{prefix}]" : null,
                options with { IncludeTimestamps = true },
                cancellationToken))
            .ToEnumerable()
            .ToArray();

        LogMerger logMerger;
        if (options.Follow)
        {
            var liveLogMerger = new LiveLogMerger();
            liveLogMerger.Activate(cancellationToken, pipelineSources);
            logMerger = liveLogMerger;
        }
        else
        {
            logMerger = new FixedLogMerger(cancellationToken, pipelineSources);
        }

        var pipeline = new Pipeline(logMerger);
        if (!options.IncludeTimestamps)
        {
            pipeline.AddElement(new LogLineFormatter(false, null));
        }

        return pipeline;
    }

    private async Task<IPipelineSource> GetContainerLogs(string containerId, string? prefix, GetLogsOptions options, CancellationToken cancellationToken)
    {
        async Task<IPipelineSource> GetSingleStreamLogs(bool stdout)
        {
            var muliplexedStream = await _client.Containers.GetContainerLogsAsync(containerId, tty: false, new()
            {
                ShowStdout = stdout,
                ShowStderr = !stdout,
                Follow = options.Follow,
                Tail = options.TailLines?.ToString() ?? "all",
                Timestamps = options.IncludeTimestamps,
                Since = options.Since?.ToUnixTimeSeconds().ToString(),
            }, cancellationToken);

            var pipe = new Pipe();

            async Task Copy()
            {
                try
                {
                    await muliplexedStream.CopyOutputToAsync(
                        stdin: Stream.Null,
                        stdout: stdout ? pipe.Writer.AsStream() : Stream.Null,
                        stderr: stdout ? Stream.Null : pipe.Writer.AsStream(),
                        cancellationToken);
                }
                finally
                {
                    pipe.Writer.Complete();
                }
            }

            _ = Copy();

            return new SimplePipelineSource(pipe.Reader);
        }

        var stdout = await GetSingleStreamLogs(true);
        var stderr = await GetSingleStreamLogs(false);
        LogMerger logMerger;
        if (options.Follow)
        {
            var liveLogMerger = new LiveLogMerger();
            liveLogMerger.Activate(cancellationToken, stdout, stderr);
            logMerger = liveLogMerger;
        }
        else
        {
            logMerger = new FixedLogMerger(cancellationToken, stdout, stderr);
        }

        var pipeline = new Pipeline(logMerger);
        if (!string.IsNullOrEmpty(prefix))
        {
            pipeline.AddElement(new LogLineFormatter(options.IncludeTimestamps, prefix));
        }

        return pipeline;
    }
}

public class DockerReplicaDatabaseVersionProvider : IReplicaDatabaseVersionProvider
{
    public IAsyncEnumerable<(Uri, DatabaseVersion)> GetDatabaseVersionsOfReplicas(CancellationToken cancellationToken)
    {
        return AsyncEnumerable.Empty<(Uri, DatabaseVersion)>();
    }
}
