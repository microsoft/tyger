// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Globalization;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Text.RegularExpressions;
using Generator.Equals;
using k8s.Models;

namespace Tyger.ControlPlane.Model;

public record ModelBase : IJsonOnDeserialized
{
#pragma warning disable IDE0032 // Use auto property
    private IDictionary<string, JsonElement>? _extraProperties;

    [JsonExtensionData]
    [Obsolete("Only for serialization")]
    public IDictionary<string, JsonElement>? ExtraProperties { get => _extraProperties; set => _extraProperties = value; }
#pragma warning restore IDE0032 // Use auto property

    // Workaround for https://github.com/dotnet/runtime/issues/37483 to ensure that we unrecognized
    // fields are not silently ignored during deserialization.
    void IJsonOnDeserialized.OnDeserialized()
    {
        if (_extraProperties != null)
        {
            throw new JsonException($"Object contains unrecognized properties {string.Join(", ", _extraProperties.Keys.Select(k => $"'{k}'"))}.");
        }
    }
}

public record Buffer : ModelBase
{
    public string Id { get; init; } = "";

    public string ETag { get; init; } = "";

    public DateTimeOffset CreatedAt { get; init; }

    public IDictionary<string, string>? Tags { get; init; }
}

public record BufferAccess(Uri Uri) : ModelBase;

public record ServiceMetadata(string? Authority = null, string? Audience = null, string? CliAppUri = null, IEnumerable<string>? Capabilities = null) : ModelBase;

[Equatable]
public partial record BufferParameters(
    [property: UnorderedEquality] IReadOnlyList<string>? Inputs,
    [property: UnorderedEquality] IReadOnlyList<string>? Outputs) : ModelBase;

[Equatable]
public partial record OvercommittableResources : ModelBase
{
    [JsonConverter(typeof(QuantityConverter))]
    public ResourceQuantity? Cpu { get; init; }

    [JsonConverter(typeof(QuantityConverter))]
    public ResourceQuantity? Memory { get; init; }
}

[Equatable]
public partial record CodespecResources : ModelBase
{
    public OvercommittableResources? Limits { get; init; }
    public OvercommittableResources? Requests { get; init; }

    [JsonConverter(typeof(QuantityConverter))]
    public ResourceQuantity? Gpu { get; init; }
}

public enum CodespecKind
{
    Job,
    Worker
}

[Equatable]
[JsonConverter(typeof(CodespecConverter))]
public abstract partial record Codespec : ModelBase, ICodespecRef
{
    protected Codespec(CodespecKind kind) => Kind = kind;

    /// <summary>
    /// Indicates the codespec kind. Can be either 'job' or 'worker'.
    /// </summary>
    [JsonIgnore(Condition = JsonIgnoreCondition.Never)]
    public CodespecKind Kind { get; init; } = CodespecKind.Job;

    /// <summary>
    /// The name of the codespec. Populated by the system.
    /// Not required during create operations, but if it is, it must match the name in the path.
    /// </summary>
    public string? Name { get; init; }

    /// <summary>
    /// The version of the codespec. Populated by the system. Ignored during create operations.
    /// </summary>
    public int? Version { get; init; }

    /// <summary>
    /// The datetime when the codespec was created. Populated by the system. Ignored during create operations.
    /// </summary>
    public DateTimeOffset? CreatedAt { get; init; }

    /// <summary>
    /// The container image
    /// </summary>
    [Required, Display(Name = "image")]
    public required string Image { get; init; }

    /// <summary>
    /// Overrides the entrypoint of the container image. If not provided, the default entrypoint of the image is used.
    /// </summary>
    [OrderedEquality]
    public IReadOnlyList<string>? Command { get; init; }

    /// <summary>
    /// Specifies the arguments to pass to the entrypoint
    /// </summary>
    [OrderedEquality]
    public IReadOnlyList<string>? Args { get; init; }

    /// <summary>
    /// The working directory of the container.
    /// </summary>
    public string? WorkingDir { get; init; }

    /// <summary>
    /// Environment variables to set in the container
    /// </summary>
    [UnorderedEquality]
    public Dictionary<string, string>? Env { get; init; }

    /// <summary>
    /// The workload identity to use. Only supported in cloud environments.
    /// </summary>
    public string? Identity { get; init; }

    /// <summary>
    /// Container resource requests and limits
    /// </summary>
    public CodespecResources? Resources { get; init; }

    /// <summary>
    /// The maximum number of replicas to run.
    /// </summary>
    public int? MaxReplicas { get; init; }

    public virtual ICodespecRef ToCodespecRef() => this;

    public Codespec WithoutSystemProperties()
    {
        return this with
        {
            Name = null,
            Version = null,
            CreatedAt = null
        };
    }

    public Codespec WithSystemProperties(string name, int version, DateTimeOffset createdAt)
    {
        return this with
        {
            Name = name,
            Version = version,
            CreatedAt = createdAt
        };
    }
}

[Equatable]
public partial record JobCodespec : Codespec, IValidatableObject
{
    public JobCodespec() : base(CodespecKind.Job) { }

    /// <summary>
    /// Buffer parameters that the job can use.
    /// </summary>
    public BufferParameters? Buffers { get; init; }

    public IReadOnlyList<Socket>? Sockets { get; init; }

    public IEnumerable<ValidationResult> Validate(ValidationContext validationContext)
    {
        if (Buffers != null)
        {
            var combined = (Buffers.Inputs ?? Enumerable.Empty<string>()).Concat(Buffers.Outputs ?? Enumerable.Empty<string>());
            foreach (var group in combined.ToLookup(i => i, StringComparer.InvariantCultureIgnoreCase))
            {
                if (string.IsNullOrWhiteSpace(group.Key))
                {
                    yield return new ValidationResult("A buffer name cannot be empty");
                    continue;
                }

                if (group.Count() > 1)
                {
                    yield return new ValidationResult(string.Format(CultureInfo.InvariantCulture, "All buffer names must be unique across inputs and outputs. Buffer names are case-insensitive. '{0}' is duplicated", group.Key));
                }

                if (group.Key.Contains('/'))
                {
                    yield return new ValidationResult(string.Format(CultureInfo.InvariantCulture, "The buffer '{0}' cannot contain '/' in its name.", group.Key));
                }
            }
        }

        if (Sockets != null)
        {
            var buffersUsedBySockets = new HashSet<string>();
            foreach (var socket in Sockets)
            {
                if (socket.Port is <= 0 or > 65535)
                {
                    yield return new ValidationResult("Port must be between 1 and 65535");
                }

                if (!string.IsNullOrEmpty(socket.InputBuffer))
                {
                    if (Buffers?.Inputs is null || !Buffers.Inputs.Contains(socket.InputBuffer, StringComparer.InvariantCultureIgnoreCase))
                    {
                        yield return new ValidationResult($"The input buffer '{socket.InputBuffer}' for socket {socket.Port} is not among the codespec's input buffer parameters");
                    }
                    else if (!buffersUsedBySockets.Add(socket.InputBuffer))
                    {
                        yield return new ValidationResult($"The input buffer '{socket.InputBuffer}' is used by multiple sockets");
                    }
                }

                if (!string.IsNullOrEmpty(socket.OutputBuffer))
                {
                    if (Buffers?.Outputs is null || !Buffers.Outputs.Contains(socket.OutputBuffer, StringComparer.InvariantCultureIgnoreCase))
                    {
                        yield return new ValidationResult($"The output buffer '{socket.OutputBuffer}' for socket {socket.Port} is not among the codespec's output buffer parameters");
                    }
                    else if (!buffersUsedBySockets.Add(socket.OutputBuffer))
                    {
                        yield return new ValidationResult($"The output buffer '{socket.OutputBuffer}' is used by multiple sockets");
                    }
                }

                if (string.IsNullOrEmpty(socket.InputBuffer) && string.IsNullOrEmpty(socket.OutputBuffer))
                {
                    yield return new ValidationResult($"At least one of the input or output buffer must be specified for socket {socket.Port}");
                }
            }

            var bufferEnvironmentVariablesUsedBySockets = buffersUsedBySockets.Select(b => $"{b.ToUpperInvariant()}_PIPE").ToHashSet();

            if (Args != null)
            {
                foreach (var arg in Args)
                {
                    foreach (var result in VerifyNoBufferReferencesUsedBySockets(arg, bufferEnvironmentVariablesUsedBySockets))
                    {
                        yield return result;
                    }
                }
            }

            if (Command != null)
            {
                foreach (var command in Command)
                {
                    foreach (var result in VerifyNoBufferReferencesUsedBySockets(command, bufferEnvironmentVariablesUsedBySockets))
                    {
                        yield return result;
                    }
                }
            }

            if (Env != null)
            {
                foreach (var env in Env.Values)
                {
                    foreach (var result in VerifyNoBufferReferencesUsedBySockets(env, bufferEnvironmentVariablesUsedBySockets))
                    {
                        yield return result;
                    }
                }
            }
        }
    }

    private static IEnumerable<ValidationResult> VerifyNoBufferReferencesUsedBySockets(string input, HashSet<string> bufferEnvironmentVariablesUsedBySockets)
    {
        foreach (Match match in EnvironmentVariableExpansionRegex().Matches(input))
        {
            if (match.Value.StartsWith("$$", StringComparison.Ordinal))
            {
                continue;
            }

            if (bufferEnvironmentVariablesUsedBySockets.Contains(match.Groups[1].Value))
            {
                yield return new ValidationResult($"The buffer reference '{match.Value}' is not valid because it is used by a socket");
            }
        }
    }

    [GeneratedRegex(@"\$\(([^)]+)\)|\$\$([^)]+)")]
    internal static partial Regex EnvironmentVariableExpansionRegex();
}

[Equatable]
public partial record Socket
{
    public int Port { get; init; }
    public string? InputBuffer { get; init; }
    public string? OutputBuffer { get; init; }
}

[Equatable]
public partial record WorkerCodespec : Codespec
{
    public WorkerCodespec() : base(CodespecKind.Worker) { }

    /// <summary>
    /// The name and port of the endpoints that the worker exposes.
    /// </summary>
    [UnorderedEquality]
    public Dictionary<string, int>? Endpoints { get; init; }
}

public record RunCodeTarget : ModelBase
{
    /// <summary>
    /// The codespec to execute. Can be an inline Codespec or a reference to a committed Codespec
    /// in the form 'name' or 'name/versions/version'.
    /// </summary>
    [Required, Display(Name = "codespec")]
    public required ICodespecRef Codespec { get; init; }

    /// <summary>
    /// The targeted node pool
    /// </summary>
    public string? NodePool { get; init; }

    /// <summary>
    /// The number of replicas to run. Defaults to 1.
    /// </summary>
    public int Replicas { get; init; } = 1;
}

public record JobRunCodeTarget : RunCodeTarget
{
    /// <summary>
    /// The IDs of buffers to provide as arguments to the buffer parameters defined in the job codespec.
    /// </summary>
    public Dictionary<string, string>? Buffers { get; init; }

    /// <summary>
    /// Tags to add to any buffer created for a job
    /// </summary>
    public Dictionary<string, string>? Tags { get; init; }
}

[JsonConverter(typeof(CodespecRefConverter))]
public interface ICodespecRef { }

public record CommittedCodespecRef(string Name, int? Version) : ICodespecRef
{
    public string ReferenceString => Version is null ? Name : $"{Name}/versions/{Version}";
}

public enum RunStatus
{
    /// <summary>
    /// The run has been created, but is waiting to start
    /// </summary>
    Pending,

    /// <summary>
    /// The Run is currently running
    /// </summary>
    Running,

    /// <summary>
    /// Indicates that the run has failed, see the StatusReason field for information on why.
    /// </summary>
    Failed,

    /// <summary>
    /// Indicates that the run has completed successfully
    /// </summary>
    Succeeded,

    /// <summary>
    /// The run is in the process of being canceled.
    /// </summary>
    Canceling,

    /// <summary>
    /// The run was canceled.
    /// </summary>
    Canceled,
}

public enum RunKind
{
    User = 0,
    System,
}

public record Run : ModelBase
{
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingDefault)]
    public RunKind Kind { get; init; } = RunKind.User;

    /// <summary>
    /// The run ID. Populated by the system.
    /// </summary>
    public long? Id { get; init; }

    /// <summary>
    /// The status of the run. Populated by the system.
    /// </summary>
    [JsonConverter(typeof(JsonStringEnumConverter))]
    public RunStatus? Status { get; init; }

    /// <summary>
    /// The reason for the status of the run. Populated by the system.
    /// </summary>
    public string? StatusReason { get; init; }

    /// <summary>
    /// The number of replicas are running. Populated by the system.
    /// </summary>
    public int? RunningCount { get; init; }

    /// <summary>
    /// The time the run was created. Populated by the system.
    /// </summary>
    public DateTimeOffset? CreatedAt { get; init; }

    /// <summary>
    /// The time the run finished. Populated by the system.
    /// </summary>
    public DateTimeOffset? FinishedAt { get; init; }

    /// <summary>
    /// The code target for the job.
    /// </summary>
    [Required, Display(Name = "job")]
    public required JobRunCodeTarget Job { get; init; }

    /// <summary>
    /// An optional code target for the worker.
    /// </summary>
    public RunCodeTarget? Worker { get; init; }

    /// <summary>
    /// The maximum number of seconds to wait for the run to complete. If the run does not complete within this time, it will be canceled.
    /// </summary>
    public int? TimeoutSeconds { get; init; } = (int)TimeSpan.FromHours(12).TotalSeconds;

    [JsonIgnore]
    public bool Final { get; init; } = false;

    [JsonIgnore]
    public DateTimeOffset? LogsArchivedAt { get; init; }

    /// <summary>
    /// The name of target cluster.
    /// </summary>
    public string? Cluster { get; init; }

    public Run WithoutSystemProperties()
    {
        return this with
        {
            Id = 0,
            Status = null,
            StatusReason = null,
            RunningCount = null,
            CreatedAt = default,
            FinishedAt = null,
        };
    }
}

public record DatabaseVersionInUse(int Id) : ModelBase;

public record RunPage(IReadOnlyList<Run> Items, Uri? NextLink);

public record CodespecPage(IReadOnlyList<Codespec> Items, Uri? NextLink);

public record BufferPage(IReadOnlyList<Buffer> Items, Uri? NextLink);

public record Cluster(string Name, string Location, IReadOnlyList<NodePool> NodePools);

public record NodePool(string Name, string VmSize);

public record ExportBuffersRequest(string DestinationStorageEndpoint, Dictionary<string, string>? Filters, [property: OpenApiExclude] bool HashIds);

[AttributeUsage(AttributeTargets.Property)]
public class OpenApiExcludeAttribute : Attribute
{
}
