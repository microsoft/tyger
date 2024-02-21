// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Buffers;
using System.IO.Pipelines;

namespace Tyger.Server.Logging;

/// <summary>
/// Sometimes the log content that should start with a timestamp will not start with a timestamp. This can happen
/// when there is a problem retrieving the logs but the HTTP response is 200. The log body
/// will then be something like "unable to retrieve container logs for containerd://...". In this case,
/// we prepend a dummy timestamp of 0001-01-01T00:00:00.000000000Z.
/// </summary>
public class KubernetesTimestampedLogReformatter : IPipelineElement
{
    private static readonly Memory<byte> s_emptyTimestampPrefix = System.Text.Encoding.UTF8.GetBytes("0001-01-01T00:00:00.000000000Z ");

    public async Task Process(PipeReader reader, PipeWriter writer, CancellationToken cancellationToken)
    {
        var atBeginningOfLine = true;
        while (true)
        {
            var result = await reader.ReadAsync(cancellationToken);
            var buffer = result.Buffer;

            SequencePosition consumedPosition = ProcessBuffer(buffer, writer, ref atBeginningOfLine);

            await writer.FlushAsync(cancellationToken);

            if (result.IsCompleted)
            {
                break;
            }

            reader.AdvanceTo(consumedPosition, buffer.End);
        }
    }

    private static SequencePosition ProcessBuffer(in ReadOnlySequence<byte> sequence, PipeWriter writer, ref bool atBeginningOfLine)
    {
        var reader = new SequenceReader<byte>(sequence);
        while (reader.Remaining > 0)
        {
            if (atBeginningOfLine)
            {
                // whatever comes before the first space is the timestamp
                var timestampStartPosition = reader.Position;
                if (!reader.TryAdvanceTo((byte)' ', advancePastDelimiter: true))
                {
                    return reader.Position;
                }

                atBeginningOfLine = false;
                var tsSequence = sequence.Slice(timestampStartPosition, reader.Position);
                if (!TimestampParser.TryParseTimestampFromSequence(tsSequence, out _))
                {
                    writer.Write(s_emptyTimestampPrefix.Span);
                }

                foreach (var segment in tsSequence)
                {
                    writer.Write(segment.Span);
                }
            }

            var startPosition = reader.Position;
            if (reader.TryAdvanceTo((byte)'\n', advancePastDelimiter: true))
            {
                atBeginningOfLine = true;
            }
            else
            {
                reader.AdvanceToEnd();
            }

            foreach (var segment in sequence.Slice(startPosition, reader.Position))
            {
                writer.Write(segment.Span);
            }
        }

        return reader.Position;
    }
}
