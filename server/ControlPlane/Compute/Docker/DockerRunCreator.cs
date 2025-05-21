// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Collections.Immutable;
using System.ComponentModel.DataAnnotations;
using System.Formats.Tar;
using System.Web;
using Docker.DotNet;
using Docker.DotNet.Models;
using k8s.Models;
using Microsoft.Extensions.Options;
using Tyger.Common.Buffers;
using Tyger.ControlPlane.Buffers;
using Tyger.ControlPlane.Codespecs;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;
using Tyger.ControlPlane.ServiceMetadata;
using static Tyger.Common.Unix.Interop;

namespace Tyger.ControlPlane.Compute.Docker;

public partial class DockerRunCreator : RunCreatorBase, IRunCreator, IHostedService, ICapabilitiesContributor
{
    public const string ContainerNameLabelKey = "tyger-run-container-name";
    public const string EphemeralBufferSocketPathLabelKey = "tyger-ephemeral-buffer-socket-path";
    public const string EphemeralBufferIdLabelKey = "tyger-ephemeral-buffer-id";
    public const string SocketCountLabelKey = "tyger-socket-count";

    private readonly DockerClient _client;
    private readonly CodespecReader _codespecReader;
    private readonly DockerEphemeralBufferProvider _ephemeralBufferProvider;
    private readonly ILogger<DockerRunCreator> _logger;

    private readonly BufferOptions _bufferOptions;
    private readonly DockerOptions _dockerOptions;
    private readonly string? _dataPlaneSocketPath;
    private readonly byte[] _publicSigningKeysTarBytes;

    public DockerRunCreator(
        DockerClient client,
        Repository repository,
        CodespecReader codespecReader,
        BufferManager bufferManager,
        DockerEphemeralBufferProvider ephemeralBufferProvider,
        IOptions<BufferOptions> bufferOptions,
        IOptions<DockerOptions> dockerOptions,
        IOptions<LocalBufferStorageOptions> localBufferStorageOptions,
        ILogger<DockerRunCreator> logger)
    : base(repository, bufferManager)
    {
        _client = client;
        _codespecReader = codespecReader;
        _ephemeralBufferProvider = ephemeralBufferProvider;
        _logger = logger;
        _bufferOptions = bufferOptions.Value;
        _dockerOptions = dockerOptions.Value;

        if (localBufferStorageOptions.Value?.DataPlaneEndpoint is { } localDpEndpoint)
        {
            if (localDpEndpoint.Scheme is "http+unix" or "https+unix")
            {
                _dataPlaneSocketPath = localDpEndpoint.AbsolutePath.Split(":")[0];
            }
        }

        _publicSigningKeysTarBytes = CreatePublicSigningKeyArchive();
    }

    // Create the tarball of the public signing keys that we will copy into sidecar containers
    private byte[] CreatePublicSigningKeyArchive()
    {
        using var tarStream = new MemoryStream();
        using var tw = new TarWriter(tarStream, leaveOpen: true);
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

        return tarStream.ToArray();
    }

    public Capabilities GetCapabilities() =>
        Capabilities.EphemeralBuffers |
        Capabilities.Docker |
        (_dockerOptions.GpuSupport ? Capabilities.Gpu : Capabilities.None);

    public async Task<Run> CreateRun(Run run, string? idempotencyKey, CancellationToken cancellationToken)
    {
        if (run.Worker != null)
        {
            throw new ValidationException("Runs with workers are only supported on Kubernetes");
        }

        if (await _codespecReader.GetCodespec(run.Job.Codespec, cancellationToken) is not JobCodespec jobCodespec)
        {
            throw new ArgumentException($"The codespec for the job is required to be a job codespec");
        }

        Validator.ValidateObject(jobCodespec, new(jobCodespec));

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
            if (!_dockerOptions.GpuSupport)
            {
                throw new ValidationException("The Docker engine does not have the NVIDIA runtime installed, which is required for GPU support.");
            }
        }

        run = run with
        {
            Cluster = null,
            Job = run.Job with
            {
                Codespec = jobCodespec.ToCodespecRef()
            }
        };

        if (run.Job.Buffers == null)
        {
            run = run with { Job = run.Job with { Buffers = [] } };
        }

        if (run.Job.Tags == null)
        {
            run = run with { Job = run.Job with { Tags = [] } };
        }

        await ProcessBufferArguments(jobCodespec.Buffers, run.Job.Buffers, run.Job.Tags, run.Job.BufferTtl, cancellationToken);

        run = await Repository.CreateRun(run, idempotencyKey, cancellationToken);

        var bufferMap = await GetBufferMap(jobCodespec.Buffers, run.Job.Buffers!, run.BufferAccessTtl, cancellationToken);
        string mainContainerName = MainContainerName(run.Id!.Value);

        if (run.Job.Buffers != null)
        {
            foreach ((var bufferParameterName, var bufferId) in run.Job.Buffers)
            {
                if (bufferId.StartsWith("temp-", StringComparison.Ordinal))
                {
                    var bufferIdWithoutPrefix = bufferId[5..];
                    var newBufferId = $"run-{run.Id}-{bufferId}";
                    run.Job.Buffers[bufferParameterName] = newBufferId;
                    (var write, _) = bufferMap[bufferParameterName];
                    var unqualifiedBufferId = BufferManager.GetUnqualifiedBufferId(newBufferId);
                    var sasQueryString = _ephemeralBufferProvider.GetSasQueryString(unqualifiedBufferId, write, run.BufferAccessTtl);
                    var accessUri = new Uri($"http+unix://{_dockerOptions.EphemeralBuffersPath}/{bufferIdWithoutPrefix}:{sasQueryString}");
                    bufferMap[bufferParameterName] = (write, accessUri);
                }
            }
        }

        var relativeSecretsPath = run.Id.ToString()!;
        var relativePipesPath = Path.Combine(relativeSecretsPath, "pipes");
        var relativeAccessFilesPath = Path.Combine(relativeSecretsPath, "access-files");
        var relativeTombstonePath = Path.Combine(relativeSecretsPath, "tombstone");

        var absoluteSecretsBase = _dockerOptions.RunSecretsPath;

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

        string RelativePipePath(string bufferParameterName)
        {
            return Path.Combine(relativePipesPath, bufferParameterName + ".pipe");
        }

        foreach ((var bufferParameterName, (bool write, Uri accessUrl)) in bufferMap)
        {
            var sidecarLabels = labels.Add(ContainerNameLabelKey, $"{bufferParameterName}-buffer-sidecar");

            var pipePath = Path.Combine(absoluteSecretsBase, RelativePipePath(bufferParameterName));
            MkFifo(pipePath, 0x1FF);
            ChMod(pipePath, 0x1FF);

            var containerPipePath = Path.Combine(absoluteContainerSecretsBase, RelativePipePath(bufferParameterName));
            if (jobCodespec.Sockets?.Any(s => s.InputBuffer == bufferParameterName || s.OutputBuffer == bufferParameterName) != true)
            {
                env[$"{bufferParameterName.ToUpperInvariant()}_PIPE"] = containerPipePath;
            }

            var accessFileName = bufferParameterName + ".access";
            var accessFilePath = Path.Combine(absoluteSecretsBase, relativeAccessFilesPath, accessFileName);
            File.WriteAllText(accessFilePath, accessUrl.ToString());
            var containerAccessFilePath = Path.Combine(absoluteContainerSecretsBase, relativeAccessFilesPath, accessFileName);

            var args = new List<string>();

            bool isRelay = HttpUtility.ParseQueryString(accessUrl.Query).Get("relay") is "true";
            string? relaySocketPath = null;
            if (isRelay)
            {
                var unqualifiedBufferId = BufferManager.GetUnqualifiedBufferId(run.Job.Buffers![bufferParameterName]);
                sidecarLabels = sidecarLabels.Add(EphemeralBufferIdLabelKey, unqualifiedBufferId);

                if (accessUrl.Scheme != "http+unix")
                {
                    throw new InvalidOperationException("Relay is only supported for http+unix URIs");
                }

                relaySocketPath = accessUrl.AbsolutePath.Split(':')[0];

                // Create a placeholder file for the relay socket. This is so that attempts to connect to the socket will return
                // a connection refused error instead of a file not found error, so that the client can distinguish between the
                // case of the relay server not having started vs exited.
                File.Create(relaySocketPath).Close();

                sidecarLabels = sidecarLabels.Add(EphemeralBufferSocketPathLabelKey, relaySocketPath);

                args.AddRange([
                    "relay",
                    write ? "output" : "input",
                    "--listen",
                    $"unix://{relaySocketPath}",
                    "--listen",
                    $"http://:8080",
                    "--primary-public-signing-key",
                    "/primary-signing-key-public.pem",
                ]);
                if (!string.IsNullOrEmpty(_bufferOptions.SecondarySigningPrivateKeyPath))
                {
                    args.AddRange(["--secondary-public-signing-key", "/secondary-signing-key-public.pem"]);
                }

                args.AddRange(["--buffer", unqualifiedBufferId]);
            }
            else
            {
                args.AddRange([write ? "output" : "input", containerAccessFilePath,]);
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
                ExposedPorts = isRelay ? new Dictionary<string, EmptyStruct> { ["8080/tcp"] = default } : null,
                HostConfig = new()
                {
                    Mounts =
                    [
                        new()
                        {
                            Source = TranslateToHostPath(Path.Combine(absoluteSecretsBase, relativePipesPath)),
                            Target = Path.Combine(absoluteContainerSecretsBase, relativePipesPath),
                            Type = "bind",
                            ReadOnly = false,
                        },
                        new()
                        {
                            Source = TranslateToHostPath(Path.Combine(absoluteSecretsBase, relativeAccessFilesPath)),
                            Target = Path.Combine(absoluteContainerSecretsBase, relativeAccessFilesPath),
                            Type = "bind",
                            ReadOnly = true,
                        },
                        new()
                        {
                            Source = TranslateToHostPath(Path.Combine(absoluteSecretsBase, relativeTombstonePath)),
                            Target = Path.Combine(absoluteContainerSecretsBase, relativeTombstonePath),
                            Type = "bind",
                            ReadOnly = true,
                        }
                    ],
                    PortBindings = isRelay ? new Dictionary<string, IList<PortBinding>>()
                    {
                        ["8080/tcp"] = [new() { HostIP = "127.0.0.1" }],
                    } : null,
                },
            };

            if (isRelay)
            {
                // Write out a 0-byte file at the socket path. This will help the client
                // distinguish between the case of the relay server not having started
                // vs exited.
                File.WriteAllBytes(relaySocketPath!, []);
                var socketDir = Path.GetDirectoryName(relaySocketPath)!;
                sidecarContainerParameters.HostConfig.Mounts.Add(new()
                {
                    Source = TranslateToHostPath(socketDir),
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
            else if (accessUrl.Scheme is "http+unix" or "https+unix")
            {
                var dataPlaneSocket = accessUrl.AbsolutePath.Split(':')[0];
                sidecarContainerParameters.HostConfig.Mounts.Add(new()
                {
                    Source = TranslateToHostPath(dataPlaneSocket),
                    Target = dataPlaneSocket,
                    Type = "bind",
                    ReadOnly = false,
                });

                foreach (var sock in unixSocketsForBuffers)
                {
                    if (Stat(sock, out var stat) == 0)
                    {
                        var uid = stat.Uid.ToString();
                        if (sidecarContainerParameters.User is { Length: > 0 } && sidecarContainerParameters.User != uid)
                        {
                            throw new InvalidOperationException("All data plane sockets must have the same owner");
                        }

                        sidecarContainerParameters.User = uid;
                    }
                }
            }

            startContainersTasks.Add(CreateAndStartContainer(sidecarContainerParameters, isRelay, cancellationToken));
        }

        if (jobCodespec.Sockets != null)
        {
            foreach (var socket in jobCodespec.Sockets)
            {
                var sidecarLabels = labels.Add(ContainerNameLabelKey, $"socket-{socket.Port}-sidecar");
                var sidecarContainerParameters = new CreateContainerParameters
                {
                    Image = _bufferOptions.BufferSidecarImage,
                    Name = $"tyger-run-{run.Id}-sidecar-socket-{socket.Port}",
                    Labels = sidecarLabels,
                    Cmd = [
                        "socket-adapt",
                        "--address",
                        $"{mainContainerName}:{socket.Port}",
                        "--input",
                        string.IsNullOrEmpty(socket.InputBuffer) ? "" : Path.Combine(absoluteContainerSecretsBase, RelativePipePath(socket.InputBuffer)),
                        "--output",
                        string.IsNullOrEmpty(socket.OutputBuffer) ? "" : Path.Combine(absoluteContainerSecretsBase, RelativePipePath(socket.OutputBuffer)),
                        "--tombstone",
                        Path.Combine(absoluteContainerSecretsBase, relativeTombstonePath, "tombstone.txt"),
                        "--log-format",
                        "json",
                    ],
                    HostConfig = new()
                    {
                        Mounts =
                        [
                            new()
                            {
                                Source = TranslateToHostPath(Path.Combine(absoluteSecretsBase, relativePipesPath)),
                                Target = Path.Combine(absoluteContainerSecretsBase, relativePipesPath),
                                Type = "bind",
                                ReadOnly = false,
                            },
                            new()
                            {
                                Source = TranslateToHostPath(Path.Combine(absoluteSecretsBase, relativeTombstonePath)),
                                Target = Path.Combine(absoluteContainerSecretsBase, relativeTombstonePath),
                                Type = "bind",
                                ReadOnly = true,
                            }
                        ],
                        NetworkMode = jobCodespec.Sockets?.Count > 0 ? _dockerOptions.NetworkName : null,
                    },
                };

                startContainersTasks.Add(CreateAndStartContainer(sidecarContainerParameters, false, cancellationToken));
            }
        }

        var mainContainerLabels = bufferMap.Count == 0 ? labels : labels.Add(ContainerNameLabelKey, "main");
        if (jobCodespec.Sockets?.Count > 0)
        {
            mainContainerLabels = mainContainerLabels.Add(SocketCountLabelKey, jobCodespec.Sockets.Count.ToString());
        }

        var mainContainerParameters = new CreateContainerParameters
        {
            Image = jobCodespec.Image,
            Name = mainContainerName,
            WorkingDir = jobCodespec.WorkingDir,
            Env = env.Select(e => $"{e.Key}={ExpandVariables(e.Value, env)}").ToList(),
            Cmd = jobCodespec.Args?.Select(a => ExpandVariables(a, env))?.ToList(),
            Entrypoint = jobCodespec.Command is { Count: > 0 } ? jobCodespec.Command.Select(a => ExpandVariables(a, env)).ToList() : null,
            Labels = mainContainerLabels,
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
                        Source = TranslateToHostPath(Path.Combine(absoluteSecretsBase, relativePipesPath)),
                        Target = Path.Combine(absoluteContainerSecretsBase, relativePipesPath),
                        Type = "bind",
                        ReadOnly = false,
                    }
                ],
                NetworkMode = jobCodespec.Sockets?.Count > 0 ? _dockerOptions.NetworkName : null,
            }
        };

        var mainContainerCreateResponse = await _client.Containers.CreateContainerAsync(mainContainerParameters, cancellationToken);
        var mainContainerId = mainContainerCreateResponse.ID;

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
                {"container", new Dictionary<string, bool>{{ mainContainerId, true } } }
            }
        }, new Progress<Message>(m =>
        {
            if (m.Action is "die" or "destroy" or "stop" or "kill")
            {
                WriteTombstone();
            }
        }), monitorCancellation.Token);

        var startMainContainerTask = _client.Containers.StartContainerAsync(mainContainerId, null, cancellationToken);
        startContainersTasks.Add(startMainContainerTask);

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

        await Repository.UpdateRunAsResourcesCreated(run.Id!.Value, run, cancellationToken: cancellationToken);

        _logger.CreatedRun(run.Id!.Value);
        return run with { Status = RunStatus.Running };
    }

    // Start background task to refresh the buffer access URLs
    public async Task<bool> UpdateRunSecret(Run run, CancellationToken cancellationToken)
    {
        var absoluteSecretsBase = _dockerOptions.RunSecretsPath;
        var relativeSecretsPath = run.Id.ToString()!;
        var relativeAccessFilesPath = Path.Combine(relativeSecretsPath, "access-files");

        var bufferAccessTtl = run.BufferAccessTtl ?? LocalSasHandler.DefaultAccessTtl;
        var accessFilesDirectory = Path.Combine(absoluteSecretsBase, relativeAccessFilesPath);

        if (run.Job.Codespec is not JobCodespec jobCodespec)
        {
            return false;
        }

        if (!Directory.Exists(accessFilesDirectory))
        {
            // The directory was deleted by DockerRunSweeper, stop refreshing
            return false;
        }

        var bufferMap = await GetBufferMap(jobCodespec.Buffers, run.Job.Buffers!, bufferAccessTtl, cancellationToken);

        foreach (var (bufferParameterName, (write, accessUrl)) in bufferMap)
        {
            var accessFileName = bufferParameterName + ".access";
            var accessFilePath = Path.Combine(accessFilesDirectory, accessFileName);
            var tempFilePath = Path.Combine(accessFilesDirectory, accessFileName + ".tmp");

            try
            {
                // Write to temporary file first, then move it atomically to the target location
                File.WriteAllText(tempFilePath, accessUrl.ToString());
                File.Move(tempFilePath, accessFilePath, overwrite: true);
            }
            catch (DirectoryNotFoundException)
            {
                // The directory was deleted by DockerRunSweeper, stop refreshing
                return false;
            }
        }

        _logger.UpdatedRunSecret(run.Id!.Value);

        return true;
    }

    internal static string MainContainerName(long runId)
    {
        return $"tyger-run-{runId}-main";
    }

    private async Task CreateAndStartContainer(CreateContainerParameters sidecarContainerParameters, bool copySingingPublicKeys, CancellationToken cancellationToken)
    {
        var createResponse = await _client.Containers.CreateContainerAsync(sidecarContainerParameters, cancellationToken);
        if (copySingingPublicKeys)
        {
            await _client.Containers.ExtractArchiveToContainerAsync(createResponse.ID, new() { Path = "/" }, new MemoryStream(_publicSigningKeysTarBytes), cancellationToken);
        }

        await _client.Containers.StartContainerAsync(createResponse.ID, null, cancellationToken);
    }

    public static string ExpandVariables(string input, IDictionary<string, string> environment)
    {
        return JobCodespec.EnvironmentVariableExpansionRegex().Replace(input, match =>
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

    public override Task StartAsync(CancellationToken cancellationToken)
    {
        Directory.CreateDirectory(_dockerOptions.RunSecretsPath);
        return Task.CompletedTask;
    }

    protected override Task ExecuteAsync(CancellationToken stoppingToken) => Task.CompletedTask;

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

    private string TranslateToHostPath(string path)
    {
        foreach (var (source, dest) in _dockerOptions.HostPathTranslations)
        {
            if (path.StartsWith(source, StringComparison.Ordinal))
            {
                return path.Replace(source, dest);
            }

            // source ends with a '/'
            if (path.Length + 1 == source.Length && path.Equals(source[..^1], StringComparison.Ordinal))
            {
                return dest[..^1];
            }
        }

        return path;
    }
}
