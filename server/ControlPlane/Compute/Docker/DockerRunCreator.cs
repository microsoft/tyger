// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Collections.Immutable;
using System.ComponentModel.DataAnnotations;
using System.Formats.Tar;
using System.Security.Cryptography.X509Certificates;
using System.Text.RegularExpressions;
using System.Web;
using Docker.DotNet;
using Docker.DotNet.Models;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.Common.Buffers;
using Tyger.ControlPlane.Buffers;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;
using Tyger.ControlPlane.ServiceMetadata;
using static Tyger.Common.Unix.Interop;

namespace Tyger.ControlPlane.Compute.Docker;

public partial class DockerRunCreator : RunCreatorBase, IRunCreator, IHostedService, ICapabilitiesContributor
{
    public const string EphemeralBufferSocketPathLabelKey = "tyger-ephemeral-buffer-socket-path";

    private readonly DockerClient _client;
    private readonly ILogger<DockerRunCreator> _logger;

    private readonly BufferOptions _bufferOptions;
    private readonly DockerOptions _dockerSecretOptions;
    private readonly string? _dataPlaneSocketPath;

    private bool _supportsGpu;

    public DockerRunCreator(
        DockerClient client,
        IRepository repository,
        BufferManager bufferManager,
        IOptions<BufferOptions> bufferOptions,
        IOptions<DockerOptions> dockerSecretOptions,
        IOptions<LocalBufferStorageOptions> localBufferStorageOptions,
        ILogger<DockerRunCreator> logger)
    : base(repository, bufferManager)
    {
        _client = client;
        _logger = logger;
        _bufferOptions = bufferOptions.Value;
        _dockerSecretOptions = dockerSecretOptions.Value;

        if (localBufferStorageOptions.Value?.DataPlaneEndpoint is { } localDpEndpoint)
        {
            if (localDpEndpoint.Scheme is "http+unix" or "https+unix")
            {
                _dataPlaneSocketPath = localDpEndpoint.AbsolutePath.Split(":")[0];
            }
        }
    }

    public Capabilities GetCapabilities() => _supportsGpu ? Capabilities.Gpu : Capabilities.None;

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

        try
        {
            await _client.Images.InspectImageAsync(jobCodespec.Image, cancellationToken: cancellationToken);
        }
        catch (DockerImageNotFoundException)
        {
            throw new ValidationException($"The image '{jobCodespec.Image}' was not found on the system. Run `docker pull {jobCodespec.Image}` and try again.");
        }

        bool needsGpu = false;
        if (jobCodespec.Resources?.Gpu is ResourceQuantity q && q.ToDecimal() != 0)
        {
            needsGpu = true;
            if (!_supportsGpu)
            {
                throw new ValidationException("The Docker engine does not have the NVIDIA runtime installed, which is required for GPU support.");
            }
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

        if (newRun.Job.Buffers != null)
        {
            foreach ((var bufferParameterName, var bufferId) in newRun.Job.Buffers)
            {
                if (bufferId.StartsWith("temp-", StringComparison.Ordinal))
                {
                    var newBufferId = $"run-{run.Id}-{bufferId}";
                    newRun.Job.Buffers[bufferParameterName] = newBufferId;
                    (var write, _) = bufferMap[bufferParameterName];
                    var accessUri = await BufferManager.CreateBufferAccessUrl(newBufferId, write, cancellationToken);
                    bufferMap[bufferParameterName] = (write, accessUri!.Uri);
                }
            }
        }

        var relativeSecretsPath = run.Id.ToString()!;
        var relativePipesPath = Path.Combine(relativeSecretsPath, "pipes");
        var relativeAccessFilesPath = Path.Combine(relativeSecretsPath, "access-files");
        var relativeTombstonePath = Path.Combine(relativeSecretsPath, "tombstone");

        var absoluteSecretsBase = _dockerSecretOptions.RunSecretsPath;
        var absoluteContainerSecretsBase = "/run/secrets";

        var env = jobCodespec.Env ?? [];

        Directory.CreateDirectory(Path.Combine(absoluteSecretsBase, relativePipesPath));
        Directory.CreateDirectory(Path.Combine(absoluteSecretsBase, relativeAccessFilesPath));
        Directory.CreateDirectory(Path.Combine(absoluteSecretsBase, relativeTombstonePath));

        var labels = ImmutableDictionary<string, string>.Empty.Add("tyger-run", run.Id?.ToString()!);

        var unixSocketsForBuffers = bufferMap.Where(b => b.Value.sasUri.Scheme is "http+unix" or "https+unix")
            .Select(b => b.Value.sasUri.AbsolutePath.Split(":")[0])
            .Distinct()
            .ToList();

        var startContainersTasks = new List<Task>();

        foreach ((var bufferParameterName, (bool write, Uri accessUri)) in bufferMap)
        {
            var sidecarLabels = labels.Add("tyger-run-container-name", $"{bufferParameterName}-buffer-sidecar");

            var pipeName = bufferParameterName + ".pipe";
            var pipePath = Path.Combine(absoluteSecretsBase, relativePipesPath, pipeName);
            MkFifo(pipePath, 0x1FF);
            ChMod(pipePath, 0x1FF);

            var containerPipePath = Path.Combine(absoluteContainerSecretsBase, relativePipesPath, Path.GetFileName(pipePath));
            env[$"{bufferParameterName.ToUpperInvariant()}_PIPE"] = containerPipePath;

            var accessFileName = bufferParameterName + ".access";
            var accessFilePath = Path.Combine(absoluteSecretsBase, relativeAccessFilesPath, accessFileName);
            File.WriteAllText(accessFilePath, accessUri.ToString());
            var containerAccessFilePath = Path.Combine(absoluteContainerSecretsBase, relativeAccessFilesPath, accessFileName);

            var args = new List<string>();

            bool isRelay = HttpUtility.ParseQueryString(accessUri.Query).Get("relay") is "true";
            string? relaySocketPath = null;
            if (isRelay)
            {
                if (accessUri.Scheme != "http+unix")
                {
                    throw new InvalidOperationException("Relay is only supported for http+unix URIs");
                }

                relaySocketPath = accessUri.AbsolutePath.Split(':')[0];

                sidecarLabels = sidecarLabels.Add(EphemeralBufferSocketPathLabelKey, relaySocketPath);

                args.AddRange([
                    "relay",
                    write ? "write" : "read",
                    "--listen",
                    $"unix://{relaySocketPath}",
                    "--primary-cert",
                    "/primary-signing-key-public.pem",
                ]);
                if (!string.IsNullOrEmpty(_bufferOptions.SecondarySigningPrivateKeyPath))
                {
                    args.AddRange(["--secondary-cert", "/secondary-signing-key-public.pem"]);
                }

                var unqualifiedBufferId = BufferManager.GetUnqualifiedBufferId(run.Job.Buffers![bufferParameterName]);

                args.AddRange(["--buffer", unqualifiedBufferId]);
            }
            else
            {
                args.AddRange([write ? "write" : "read", containerAccessFilePath,]);
            }

            args.AddRange([
                write ? "-i" : "-o",
                containerPipePath,
                "--tombstone",
                Path.Combine(absoluteContainerSecretsBase, relativeTombstonePath, "tombstone.txt"),
                "--log-format",
                "json",
            ]);

            var sidecarContainerParameters = new CreateContainerParameters
            {
                Image = _bufferOptions.BufferSidecarImage,
                Name = $"tyger-run-{run.Id}-sidecar-{bufferParameterName}",
                Labels = sidecarLabels,
                Cmd = args,
                HostConfig = new()
                {
                    Mounts =
                    [
                        new()
                        {
                            Source = Path.Combine(absoluteSecretsBase, relativePipesPath),
                            Target = Path.Combine(absoluteContainerSecretsBase, relativePipesPath),
                            Type = "bind",
                            ReadOnly = false,
                        },
                        new()
                        {
                            Source = Path.Combine(absoluteSecretsBase, relativeAccessFilesPath, accessFileName),
                            Target = Path.Combine(absoluteContainerSecretsBase, relativeAccessFilesPath, accessFileName),
                            Type = "bind",
                            ReadOnly = true,
                        },
                        new()
                        {
                            Source = Path.Combine(absoluteSecretsBase, relativeTombstonePath),
                            Target = Path.Combine(absoluteContainerSecretsBase, relativeTombstonePath),
                            Type = "bind",
                            ReadOnly = true,
                        }
                    ],
                },
            };

            if (isRelay)
            {
                // Write out a 0-byte file at the socket path. This will help the client
                // distinguish between the case of the relay server not having started
                // vs exited.
                File.WriteAllBytes(relaySocketPath!, Array.Empty<byte>());
                var socketDir = Path.GetDirectoryName(relaySocketPath)!;
                sidecarContainerParameters.HostConfig.Mounts.Add(new()
                {
                    Source = socketDir,
                    Target = socketDir,
                    Type = "bind",
                    ReadOnly = false,
                });
                if (_dataPlaneSocketPath != null)
                {
                    // use the same ownership as the data plane socket
                    Stat(_dataPlaneSocketPath, out var stat);
                    sidecarContainerParameters.User = $"{stat.Uid}:{stat.Gid}";
                }
            }
            else if (accessUri.Scheme is "http+unix" or "https+unix")
            {
                var dataPlaneSocket = accessUri.AbsolutePath.Split(':')[0];
                sidecarContainerParameters.HostConfig.Mounts.Add(new()
                {
                    Source = dataPlaneSocket,
                    Target = dataPlaneSocket,
                    Type = "bind",
                    ReadOnly = false,
                });

                foreach (var sock in unixSocketsForBuffers)
                {
                    Stat(sock, out var stat);
                    var uid = stat.Uid.ToString();
                    if (sidecarContainerParameters.User is { Length: > 0 } && sidecarContainerParameters.User != uid)
                    {
                        throw new InvalidOperationException("All data plane sockets must have the same owner");
                    }

                    sidecarContainerParameters.User = uid;
                }
            }

            async Task StartSidecar()
            {
                var createResponse = await _client.Containers.CreateContainerAsync(sidecarContainerParameters, cancellationToken);
                await _client.Containers.StartContainerAsync(createResponse.ID, null, cancellationToken);
            }

            startContainersTasks.Add(StartSidecar());
        }

        var mainContainerParameters = new CreateContainerParameters
        {
            Image = jobCodespec.Image,
            Name = $"tyger-run-{run.Id}-main",
            WorkingDir = jobCodespec.WorkingDir,
            Env = env.Select(e => $"{e.Key}={e.Value}").ToList(),
            Cmd = jobCodespec.Args?.Select(a => ExpandVariables(a, env))?.ToList(),
            Entrypoint = jobCodespec.Command is { Length: > 0 } ? jobCodespec.Command.Select(a => ExpandVariables(a, env)).ToList() : null,
            Labels = bufferMap.Count == 0 ? labels : labels.Add("tyger-run-container-name", $"main"),
            HostConfig = new()
            {
                DeviceRequests = needsGpu ? [
                    new()
                    {
                        Count = -1,
                        Capabilities = [["gpu"]]
                    }
                ] : [],
                Mounts =
                [
                    new()
                    {
                        Source = Path.Combine(absoluteSecretsBase, relativePipesPath),
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

        startContainersTasks.Add(_client.Containers.StartContainerAsync(containerId, null, cancellationToken));

        try
        {
            foreach (var task in startContainersTasks)
            {
                await task;
            }
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

    public async Task StartAsync(CancellationToken cancellationToken)
    {
        Directory.CreateDirectory(_dockerSecretOptions.RunSecretsPath);

        var systemInfo = await _client.System.GetSystemInfoAsync(cancellationToken);
        _supportsGpu = systemInfo.Runtimes?.ContainsKey("nvidia") == true;

        await AddPublicSigningKeyToBufferSidecarImage(cancellationToken);
    }

    private async Task AddPublicSigningKeyToBufferSidecarImage(CancellationToken cancellationToken)
    {
        var tarStream = new MemoryStream();
        using (var tw = new TarWriter(tarStream, leaveOpen: true))
        {
            var entry = new PaxTarEntry(TarEntryType.RegularFile, "primary-signing-key-public.pem")
            {
                DataStream = GetPublicPemStream(_bufferOptions.PrimarySigningPrivateKeyPath),
                Mode = UnixFileMode.UserRead | UnixFileMode.UserWrite | UnixFileMode.GroupRead | UnixFileMode.GroupWrite | UnixFileMode.OtherRead | UnixFileMode.OtherWrite,
                ModificationTime = DateTimeOffset.UnixEpoch,
            };
            tw.WriteEntry(entry);

            if (!string.IsNullOrEmpty(_bufferOptions.SecondarySigningPrivateKeyPath))
            {
                entry = new PaxTarEntry(TarEntryType.RegularFile, "secondary-signing-key-public.pem")
                {
                    DataStream = GetPublicPemStream(_bufferOptions.SecondarySigningPrivateKeyPath),
                    Mode = UnixFileMode.UserRead | UnixFileMode.UserWrite | UnixFileMode.GroupRead | UnixFileMode.GroupWrite | UnixFileMode.OtherRead | UnixFileMode.OtherWrite,
                    ModificationTime = DateTimeOffset.UnixEpoch,
                };
                tw.WriteEntry(entry);
            }
        }

        tarStream.Position = 0;

        var createResp = await _client.Containers.CreateContainerAsync(new CreateContainerParameters
        {
            Image = _bufferOptions.BufferSidecarImage,
        }, cancellationToken);

        try
        {
            await _client.Containers.ExtractArchiveToContainerAsync(createResp.ID, new() { Path = "/" }, tarStream, cancellationToken);
            var commitResponse = await _client.Images.CommitContainerChangesAsync(new() { ContainerID = createResp.ID }, cancellationToken);
            _bufferOptions.BufferSidecarImage = commitResponse.ID;
        }
        finally
        {
            await _client.Containers.RemoveContainerAsync(createResp.ID, new() { Force = true }, cancellationToken);
        }
    }

    private static MemoryStream GetPublicPemStream(string path)
    {
        var key = DigitalSignature.CreateAsymmetricAlgorithmFromPem(path).ExportSubjectPublicKeyInfoPem();

        var pemStream = new MemoryStream();
        using (var sw = new StreamWriter(pemStream, leaveOpen: true))
        {
            sw.Write(key);
        }

        pemStream.Position = 0;
        return pemStream;
    }

    public Task StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;

    [GeneratedRegex(@"\$\(([^)]+)\)|\$\$([^)]+)")]
    private static partial Regex EnvironmentVariableExpansionRegex();
}
