// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Globalization;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Text.RegularExpressions;
using k8s.Models;

namespace Tyger.ControlPlane.Model;

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

internal sealed class CodespecConverter : JsonConverter<Codespec>
{
    public override Codespec? Read(ref Utf8JsonReader reader, Type typeToConvert, JsonSerializerOptions options)
    {
        var deserializedObj = JsonElement.ParseValue(ref reader);
        var discriminator = options.PropertyNamingPolicy == JsonNamingPolicy.CamelCase ? "kind" : "Kind";

        if (!deserializedObj.TryGetProperty(discriminator, out var property))
        {
            throw new JsonException($"Missing discriminator property '{discriminator}'.");
        }

        CodespecKind kind;
        try
        {
            kind = property.Deserialize<CodespecKind>(options);
        }
        catch (JsonException)
        {
            throw new JsonException($"Invalid value for the property '{discriminator}'. It can be either 'job' or 'worker'.");
        }

        return kind switch
        {
            CodespecKind.Job => deserializedObj.Deserialize<JobCodespec>(options),
            CodespecKind.Worker => deserializedObj.Deserialize<WorkerCodespec>(options),
            _ => throw new JsonException($"Unsupported value for the property '{discriminator}'."),
        };
    }

    public override void Write(Utf8JsonWriter writer, Codespec value, JsonSerializerOptions options)
    {
        switch (value)
        {
            case JobCodespec job:
                JsonSerializer.Serialize(writer, job, options);
                break;

            case WorkerCodespec worker:
                JsonSerializer.Serialize(writer, worker, options);
                break;

            default:
                throw new InvalidOperationException($"{value.Kind} is not a supported value");
        }
    }
}

public class CodespecRefConverter : JsonConverter<ICodespecRef>
{
    public override ICodespecRef? Read(ref Utf8JsonReader reader, Type typeToConvert, JsonSerializerOptions options)
    {
        if (reader.TokenType == JsonTokenType.String)
        {
            var stringRef = reader.GetString();
            var match = Regex.Match(stringRef ?? "", @"^(?<name>[a-z0-9\-._]+)(/versions/(?<version>[0-9]+))?$");

            if (!match.Success)
            {
                throw new JsonException(
                    string.Format(CultureInfo.InvariantCulture,
                    "The codespec reference '{0}' is invalid. It should be in the form '<codespec_name>' or '<codespec_name>/versions/<version_number>'.",
                    stringRef));
            }

            var name = match.Groups["name"].Value!;
            int? version = null;
            if (match.Groups["version"].Success)
            {
                if (!int.TryParse(match.Groups["version"].Value, CultureInfo.InvariantCulture, out var versionNumber))
                {
                    throw new JsonException(
                        string.Format(CultureInfo.InvariantCulture,
                        "The codespec reference '{0}' is invalid. The version number '{1}' is not a valid integer.",
                        stringRef, match.Groups["version"].Value));
                }

                version = versionNumber;
            }

            return new CommittedCodespecRef(name, version);
        }

        if (reader.TokenType == JsonTokenType.StartObject)
        {
            var codespec = JsonSerializer.Deserialize<Codespec>(ref reader, options);
            return codespec! with { Name = null, Version = null, CreatedAt = null };
        }

        throw new JsonException("Expected string or object");
    }

    public override void Write(Utf8JsonWriter writer, ICodespecRef value, JsonSerializerOptions options)
    {
        if (value is CommittedCodespecRef named)
        {
            writer.WriteStringValue(named.ReferenceString);
        }
        else if (value is Codespec inline)
        {
            JsonSerializer.Serialize(writer, inline, options);
        }
        else
        {
            throw new InvalidOperationException("Unexpected type of ICodespecRef");
        }
    }
}
