using System.ComponentModel.DataAnnotations;
using System.Globalization;
using System.Text.Json;
using System.Text.Json.Serialization;
using Generator.Equals;
using k8s.Models;

namespace Tyger.Server.Model;

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

public record Buffer(string Id) : ModelBase;

public record BufferAccess(Uri Uri) : ModelBase;

public record Metadata(string? Authority = null, string? Audience = null) : ModelBase;

[Equatable]
public partial record BufferParameters(
    [property: UnorderedEquality] string[]? Inputs,
    [property: UnorderedEquality] string[]? Outputs) : ModelBase;

[Equatable]
public partial record CodespecResources : ModelBase
{
    [JsonConverter(typeof(QuantityConverter))]
    public ResourceQuantity? Cpu { get; init; }

    [JsonConverter(typeof(QuantityConverter))]
    public ResourceQuantity? Memory { get; init; }

    [JsonConverter(typeof(QuantityConverter))]
    public ResourceQuantity? Gpu { get; init; }

    public class QuantityConverter : JsonConverter<ResourceQuantity>
    {
        public override ResourceQuantity? Read(ref Utf8JsonReader reader, Type typeToConvert, JsonSerializerOptions options)
        {
            try
            {
                if (reader.TokenType == JsonTokenType.Number)
                {
                    return new ResourceQuantity(reader.GetDecimal().ToString(CultureInfo.InvariantCulture));
                }

                var valueString = reader.GetString();
                return string.IsNullOrEmpty(valueString) ? null : new ResourceQuantity(valueString);
            }
            catch (Exception e) when (e is FormatException or ArgumentException)
            {
                throw new JsonException(e.Message, e);
            }
        }

        public override void Write(Utf8JsonWriter writer, ResourceQuantity value, JsonSerializerOptions options)
        {
            writer.WriteStringValue(value?.ToString());
        }
    }
}

public enum CodespecKind
{
    Job,
    Worker
}

[Equatable]
public partial record NewCodespec : ModelBase, IValidatableObject
{
    public CodespecKind Kind { get; init; } = CodespecKind.Job;

    [Required]
    public string Image { get; init; } = "";

    [OrderedEquality]
    public string[]? Command { get; init; }

    [OrderedEquality]
    public string[]? Args { get; init; }

    public string? WorkingDir { get; init; }

    [UnorderedEquality]
    public Dictionary<string, string>? Env { get; init; }

    public CodespecResources? Resources { get; init; }

    public int MaxReplicas { get; init; } = 1;

    public BufferParameters? Buffers { get; init; }

    public IEnumerable<ValidationResult> Validate(ValidationContext validationContext)
    {
        if (Kind == CodespecKind.Worker && Buffers is { Inputs.Length: > 0 } or { Outputs.Length: > 0 })
        {
            yield return new ValidationResult("Buffers are only supported on job codespecs");
        }

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
    }

    /// <summary>
    /// Performs a shallow clone of this object, but the new object will be of type
    /// NewCodepec, even if the current instance is of a derived type.
    /// </summary>
    public NewCodespec SliceAsNewCodespec() => new(this);
}

public record Codespec : NewCodespec
{
    public Codespec(NewCodespec newCodespec, string name, int version, DateTimeOffset createdAt)
    : base(newCodespec)
    {
        Name = name;
        Version = version;
        CreatedAt = createdAt;
    }

    public string Name { get; init; }
    public int Version { get; init; }

    public string NormalizedRef() => $"{Name}/versions/{Version}";

    public DateTimeOffset CreatedAt { get; init; }
}

public record RunCodeTarget
{
    [Required]
    public string Codespec { get; init; } = "";

    public Dictionary<string, string>? Buffers { get; init; }

    public string? NodePool { get; init; }

    public int Replicas { get; init; } = 1;
}

public record Run : NewRun
{
    public Run() { }
    public Run(NewRun newRun) : base(newRun) { }

    public long Id { get; init; }
    public string Status { get; init; } = null!;
    public string? Reason { get; init; }
    public int? RunningCount { get; init; }
    public DateTimeOffset CreatedAt { get; init; }
    public DateTimeOffset? FinishedAt { get; init; }
}

public record NewRun : ModelBase
{
    [Required]
    public RunCodeTarget Job { get; init; } = null!;

    public RunCodeTarget? Worker { get; init; }

    public int? TimeoutSeconds { get; init; } = (int)TimeSpan.FromHours(12).TotalMilliseconds;

    public string? Cluster { get; init; }
}

public record RunStatus(string Phase, int PendingCount, int RunningCount);

public record RunPage(IReadOnlyList<Run> Items, Uri? NextLink);

public record CodespecPage(IList<Codespec> Items, Uri? NextLink);

public record Cluster(string Name, string Region, IReadOnlyList<NodePool> NodePools);

public record NodePool(string Name, string VmSize);

public record ErrorBody
{
    public ErrorBody(string code, string message) => Error = new ErrorInfo(code, message);

    public ErrorInfo Error { get; init; }
    public record ErrorInfo(string Code, string Message);
}
