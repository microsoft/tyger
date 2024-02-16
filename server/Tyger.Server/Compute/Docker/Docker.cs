using System.ComponentModel.DataAnnotations;
using System.Runtime.InteropServices;
using System.Text.RegularExpressions;
using Docker.DotNet;
using Docker.DotNet.Models;
using Microsoft.Extensions.Options;
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
    private readonly string _bufferSidecarImage;
    private readonly DockerSecretOptions _dockerSecretOptions;

    public DockerRunCreator(
        DockerClient client,
        IRepository repository,
        BufferManager bufferManager,
        IOptions<BufferOptions> bufferOptions,
        IOptions<DockerSecretOptions> dockerSecretOptions)
    : base(repository, bufferManager)
    {
        _client = client;
        _bufferSidecarImage = bufferOptions.Value.BufferSidecarImage;
        _dockerSecretOptions = dockerSecretOptions.Value;
    }

    public async Task<Run> CreateRun(Run newRun, CancellationToken cancellationToken)
    {
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

        var run = await Repository.CreateRun(newRun, cancellationToken);

        var relativeSecretsPath = run.Id.ToString()!;
        var relativePipesPath = Path.Combine(relativeSecretsPath, "pipes");
        var relativeAccessFilesPath = Path.Combine(relativeSecretsPath, "access-files");

        var absoluteSecretsBase = _dockerSecretOptions.RunSecretsPath;
        var absoluteHostSecretsBase = string.IsNullOrEmpty(_dockerSecretOptions.RunSecretsHostPath) ? absoluteSecretsBase : _dockerSecretOptions.RunSecretsHostPath;
        var absoluteContainerSecretsBase = "/run/secrets";

        var env = jobCodespec.Env ?? [];

        Directory.CreateDirectory(Path.Combine(absoluteSecretsBase, relativePipesPath));
        Directory.CreateDirectory(Path.Combine(absoluteSecretsBase, relativeAccessFilesPath));

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
                Cmd =
                [
                    write ? "write" : "read",
                    containerAccessFilePath,
                    write ? "-i" : "-o",
                    containerPipePath,
                    "--tombstone",
                    "/tmp/tombstone.txt"
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
            Labels = new Dictionary<string, string>(){
                { "tyger-run", run.Id?.ToString()! },
            },
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

        var container = await _client.Containers.InspectContainerAsync(createResponse.ID, cancellationToken);

        // var cancellation = new CancellationTokenSource();
        // var linkedCancellationToken = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken, cancellation.Token).Token;

        // var monitorTask = _client.System.MonitorEventsAsync(new ContainerEventsParameters()
        // {
        //     Filters = new Dictionary<string, IDictionary<string, bool>>
        //     {
        //         {"container", new Dictionary<string, bool>{{ container.ID, true } } }
        //     }
        // }, new Progress<Message>(m => Console.WriteLine($"Status = {m.Status}, {m.Action} {m.Actor} {m.ID} ")), linkedCancellationToken);

        await _client.Containers.StartContainerAsync(createResponse.ID, null, cancellationToken);

        // await monitorTask;
        return run;
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
    public Task<Run?> GetRun(long id, CancellationToken cancellationToken)
    {
        throw new NotImplementedException();
    }

    public Task<(IReadOnlyList<Run>, string? nextContinuationToken)> ListRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        throw new NotImplementedException();
    }

    public IAsyncEnumerable<Run> WatchRun(long id, CancellationToken cancellationToken)
    {
        throw new NotImplementedException();
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
    public Task<Pipeline?> GetLogs(long runId, GetLogsOptions options, CancellationToken cancellationToken)
    {
        throw new NotImplementedException();
    }
}

public class DockerReplicaDatabaseVersionProvider : IReplicaDatabaseVersionProvider
{
    public IAsyncEnumerable<(Uri, DatabaseVersion)> GetDatabaseVersionsOfReplicas(CancellationToken cancellationToken)
    {
        return AsyncEnumerable.Empty<(Uri, DatabaseVersion)>();
    }
}
