using System.Buffers;
using System.IO.Pipelines;
using System.Text;

namespace Tyger.Server.Logging;

/// <summary>
/// A pipeline element that formats log lines. It can discard leading timestamps and insert a prefix or context
/// string at the start of the log message
/// </summary>
public class LogLineFormatter : IPipelineElement
{
    private readonly bool _includeTimestamps;
    private readonly ReadOnlyMemory<byte> _contextMemory;

    public LogLineFormatter(bool includeTimestamps, string? context)
    {
        _includeTimestamps = includeTimestamps;
        if (string.IsNullOrEmpty(context))
        {

            _contextMemory = new ReadOnlyMemory<byte>();
        }
        else
        {
            if (!context.EndsWith(' '))
            {
                context += " ";
            }

            _contextMemory = new ReadOnlyMemory<byte>(Encoding.UTF8.GetBytes(context));
        }
    }

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

    private SequencePosition ProcessBuffer(in ReadOnlySequence<byte> sequence, PipeWriter writer, ref bool atBeginningOfLine)
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

                if (_includeTimestamps)
                {
                    foreach (var segment in timestampSequence)
                    {
                        writer.Write(segment.Span);
                    }
                }

                writer.Write(_contextMemory.Span);
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

    public override string ToString() => Encoding.UTF8.GetString(_contextMemory.Span);
}
