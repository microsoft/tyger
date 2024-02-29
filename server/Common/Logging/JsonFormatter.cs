// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Diagnostics;
using System.Globalization;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.Json;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Logging.Console;
using Microsoft.Extensions.Options;

namespace Tyger.Logging;

/// <summary>
/// Writes out log entries as ndjson. The formatting is different from the
/// built-in JsonConsoleFormatter in order to make searching the logs easier and
/// to filter out some noisy properties.
/// </summary>
internal sealed class JsonFormatter : ConsoleFormatter
{
    public static readonly string FormatterName = typeof(JsonFormatter).FullName!;
    private readonly ConsoleFormatterOptions _formatterOptions;
    private static readonly HashSet<string> s_scopeFieldsToIgnore =
    [
        "ConnectionId",
        "RequestPath",
        "{OriginalFormat}"
    ];
    private static readonly HashSet<string> s_stateFieldsToIgnore =
    [
        "{OriginalFormat}"
    ];

    public JsonFormatter(IOptions<ConsoleFormatterOptions> options)
        : base(FormatterName)
    {
        _formatterOptions = options.Value;
    }

    public override void Write<TState>(in LogEntry<TState> logEntry, IExternalScopeProvider? scopeProvider, TextWriter textWriter)
    {
        string? message = logEntry.Formatter?.Invoke(logEntry.State, logEntry.Exception);
        if (message is null)
        {
            return;
        }

        using var output = new PooledByteBufferWriter(1024);
        using var writer = new Utf8JsonWriter(output, new JsonWriterOptions { Indented = false });
        writer.WriteStartObject();
        writer.WriteString("timestamp", DateTimeOffset.UtcNow);
        writer.WriteNumber("level", (int)logEntry.LogLevel);
        writer.WriteString("category", $"{logEntry.Category}[{logEntry.EventId}]");

        scopeProvider?.ForEachScope((object? scope, object? state) =>
        {
            if (scope is IEnumerable<KeyValuePair<string, object>> values)
            {
                foreach (var value in values)
                {
                    if (!s_scopeFieldsToIgnore.Contains(value.Key))
                    {
                        WriteItemCheckKeyCase(writer, value.Key, value.Value);
                    }
                }
            }
        }, null);

        writer.WriteString("message", message);
        if (logEntry.Exception != null)
        {
            writer.WriteString("exception", logEntry.Exception.ToString());
        }

        if (logEntry.State is IEnumerable<KeyValuePair<string, object>> args)
        {
            writer.WriteStartObject("args");
            foreach (var arg in args)
            {
                if (!s_stateFieldsToIgnore.Contains(arg.Key))
                {
                    WriteItem(writer, arg.Key, arg.Value);
                }
            }

            writer.WriteEndObject();
        }

        Activity? activity = Activity.Current;
        if (activity != null)
        {
            bool hasBaggage = false;
            foreach (var baggageEntry in activity.Baggage)
            {
                if (!hasBaggage)
                {
                    writer.WriteStartObject("baggage");
                    hasBaggage = true;
                }

                writer.WriteString(baggageEntry.Key, baggageEntry.Value);
            }

            if (hasBaggage)
            {
                writer.WriteEndObject();
            }
        }

        writer.WriteEndObject();
        writer.Flush();
        textWriter.WriteLine(Encoding.UTF8.GetString(output.WrittenMemory.Span));
    }

    private static void WriteItemCheckKeyCase(Utf8JsonWriter writer, string key, object value)
    {
        if (char.IsUpper(key[0]))
        {
            Span<char> camelCasedKey = stackalloc char[key.Length];
            key.CopyTo(camelCasedKey);
            camelCasedKey[0] = char.ToLowerInvariant(camelCasedKey[0]);
            WriteItem(writer, camelCasedKey, value);
        }
        else
        {
            WriteItem(writer, key, value);
        }
    }

    private static void WriteItem(Utf8JsonWriter writer, ReadOnlySpan<char> key, object value)
    {
        switch (value)
        {
            case string stringValue:
                writer.WriteString(key, stringValue);
                break;
            case DateTime dateValue:
                writer.WriteString(key, dateValue);
                break;
            case DateTimeOffset dateValue:
                writer.WriteString(key, dateValue);
                break;
            case bool boolValue:
                writer.WriteBoolean(key, boolValue);
                break;
            case byte byteValue:
                writer.WriteNumber(key, byteValue);
                break;
            case sbyte sbyteValue:
                writer.WriteNumber(key, sbyteValue);
                break;
            case char charValue:
                writer.WriteString(key, MemoryMarshal.CreateSpan(ref charValue, 1));
                break;
            case decimal decimalValue:
                writer.WriteNumber(key, decimalValue);
                break;
            case double doubleValue:
                writer.WriteNumber(key, doubleValue);
                break;
            case float floatValue:
                writer.WriteNumber(key, floatValue);
                break;
            case int intValue:
                writer.WriteNumber(key, intValue);
                break;
            case uint uintValue:
                writer.WriteNumber(key, uintValue);
                break;
            case long longValue:
                writer.WriteNumber(key, longValue);
                break;
            case ulong ulongValue:
                writer.WriteNumber(key, ulongValue);
                break;
            case short shortValue:
                writer.WriteNumber(key, shortValue);
                break;
            case ushort ushortValue:
                writer.WriteNumber(key, ushortValue);
                break;
            case null:
                writer.WriteNull(key);
                break;
            default:
                writer.WriteString(key, Convert.ToString(value, CultureInfo.InvariantCulture));
                break;
        }
    }
}
