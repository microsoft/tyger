using System.ComponentModel.DataAnnotations;
using System.Globalization;
using System.Text.Json;
using System.Text.Json.Serialization;
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

public record BufferParameters(string[]? Inputs, string[]? Outputs) : ModelBase;

public record CodespecResources : ModelBase
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

public record Codespec : ModelBase, IValidatableObject
{
    public BufferParameters? Buffers { get; init; }
    [Required]
    public string Image { get; init; } = "";
    public string[]? Command { get; init; }
    public string[]? Args { get; init; }
    public string? WorkingDir { get; init; }
    public Dictionary<string, string>? Env { get; init; }
    public CodespecResources? Resources { get; init; }

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
    }
}

public record Run : ModelBase
{
    public string? Id { get; init; }
    public Dictionary<string, string>? Buffers { get; init; }
    [Required]
    public string Codespec { get; init; } = "";
    public string? Status { get; init; }
}

public record ErrorBody
{
    public ErrorBody(string code, string message) => Error = new ErrorInfo(code, message);

    public ErrorInfo Error { get; init; }
    public record ErrorInfo(string Code, string Message);
}
