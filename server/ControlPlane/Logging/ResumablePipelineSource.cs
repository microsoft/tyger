// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Buffers;
using System.IO.Pipelines;

namespace Tyger.ControlPlane.Logging;

/// <summary>
/// A pipeline source that reads from a new inner IPipelineSource. Then the inner source fails with an IOException,
/// it creates a new source and starts reading from it starting from the last timestamp observed.
/// </summary>
public class ResumablePipelineSource : IPipelineSource
{
    private readonly Func<GetLogsOptions, Task<IPipelineSource?>> _innerSourceFactory;
    private readonly GetLogsOptions _options;
    private readonly ILogger<ResumablePipelineSource> _logger;
    private DateTimeOffset _lastReadTimestamp;

    public ResumablePipelineSource(Func<GetLogsOptions, Task<IPipelineSource?>> innerSourceFactory, GetLogsOptions options, ILogger<ResumablePipelineSource> logger)
    {
        _innerSourceFactory = innerSourceFactory;
        if (!options.IncludeTimestamps)
        {
            throw new ArgumentException($"{nameof(GetLogsOptions)}.{nameof(options.IncludeTimestamps)} must be true", nameof(options));
        }

        _options = options;
        _logger = logger;
    }

    public async Task Process(PipeWriter writer, CancellationToken cancellationToken)
    {
        var reader = await GetPipeReader(cancellationToken);
        if (reader == null)
        {
            return;
        }

        try
        {
            var atBeginningOfLine = true;
            while (true)
            {
                ReadResult readResult;
                try
                {
                    readResult = await reader.ReadAsync(cancellationToken);
                }
                catch (IOException e)
                {
                    _logger.ResumingLogsAfterException(e);

                    reader = await GetPipeReader(cancellationToken);
                    if (reader == null)
                    {
                        return;
                    }

                    if (!atBeginningOfLine)
                    {
                        WriteNewline(writer);
                        atBeginningOfLine = true;
                    }

                    continue;
                }

                var buffer = readResult.Buffer;

                SequencePosition consumedPosition = ProcessBuffer(buffer, writer, ref atBeginningOfLine);

                await writer.FlushAsync(cancellationToken);

                if (readResult.IsCompleted)
                {
                    await reader.CompleteAsync();
                    break;
                }

                reader.AdvanceTo(consumedPosition, buffer.End);
            }
        }
        catch (Exception e)
        {
            if (reader != null)
            {
                await reader.CompleteAsync(e);
            }
        }
    }

    private async Task<PipeReader?> GetPipeReader(CancellationToken cancellationToken)
    {
        var currentOptions = _lastReadTimestamp == default ? _options : _options with { Since = _lastReadTimestamp };
        IPipelineSource? pipelineSource = await _innerSourceFactory(currentOptions);
        if (pipelineSource == null)
        {
            return null;
        }

        return pipelineSource.GetReader(cancellationToken);
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
                if (!TimestampParser.TryParseTimestampFromSequence(timestampSequence, out _lastReadTimestamp))
                {
                    throw new InvalidOperationException("Unable to parse timestamp");
                }

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

    private static void WriteNewline(PipeWriter writer)
    {
        Span<byte> newline = stackalloc[] { (byte)'\n' };
        writer.Write(newline);
    }
}
