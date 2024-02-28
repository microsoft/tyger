// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Buffers;
using System.IO.Pipelines;

namespace Tyger.ControlPlane.Logging;

/// <summary>
/// A pipeline element that can be terminated before the input PipeReader is completed.
/// No exception is thrown when terminated, the output is simply truncated.
/// Assumes that the input lines are prefixed with a timestamp. The output will not be
/// broken within a timestamp.
/// </summary>
public class TerminablePipelineElement : IPipelineElement
{
    private PipeReader? _reader;

    public void Terminate()
    {
        if (_reader == null)
        {
            throw new InvalidOperationException("Pipeline cannot be terminated when it has not started.");
        }

        _reader.CancelPendingRead();
    }

    public async Task Process(PipeReader reader, PipeWriter writer, CancellationToken cancellationToken)
    {
        _reader = reader;
        var atBeginningOfLine = true;
        while (true)
        {
            var result = await reader.ReadAsync(cancellationToken);
            if (result.IsCanceled)
            {
                return;
            }

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
                var timestampSequence = sequence.Slice(timestampStartPosition, reader.Position);

                foreach (var segment in timestampSequence)
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
