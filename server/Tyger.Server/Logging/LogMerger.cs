using System.Buffers;
using System.IO.Pipelines;

namespace Tyger.Server.Logging;

/// <summary>
/// Performs a merge sort of multiple log streams based on the leading timestamp on each line.
/// </summary>
public class FixedLogMerger : LogMerger
{
    public FixedLogMerger(CancellationToken cancellationToken, params IPipelineSource[] inputPipelines)
        : base(cancellationToken, inputPipelines)
    {
    }

    protected override async Task Merge(PipeWriter writer, CancellationToken cancellationToken)
    {
        var timestamps = new List<DateTimeOffset?>(InputReaders.Count);
        foreach (var inputReader in InputReaders)
        {
            timestamps.Add(await inputReader.PeekTimestamp(cancellationToken));
        }

        while (true)
        {
            if (InputReaders.Count == 1)
            {
                // no more merging required.
                await InputReaders[0].PipeReader.CopyToAsync(writer, cancellationToken);
                return;
            }

            DateTimeOffset? min = null;
            int minIndex = -1;
            for (int i = InputReaders.Count - 1; i >= 0; i--)
            {
                var current = timestamps[i];
                if (current == null)
                {
                    InputReaders.RemoveAt(i);
                    timestamps.RemoveAt(i);
                    minIndex--;
                    continue;
                }

                if (min == null || current < min)
                {
                    min = current;
                    minIndex = i;
                }
            }

            if (min == null)
            {
                return;
            }

            await InputReaders[minIndex].CopyLineTo(writer, cancellationToken);
            timestamps[minIndex] = await InputReaders[minIndex].PeekTimestamp(cancellationToken);
        }
    }
}

/// <summary>
/// Performs an approximate merge sort over a multiple log streams. If differs from
/// FixedLogMerger in the following ways:
/// 1. It merges data as it arrives and does not wait for lines to be available in all sources
/// 2. Because data may be late in arriving, it can end up merging data with a lower timestamp
///    after a later timestamp from another source has already been written
/// 3. It does not start reading and merging until Activate() is called
/// 4. It supports completing a stream when all input streams are drained but not necessarily completed.
/// </summary>
public class LiveLogMerger : LogMerger
{
    private readonly TaskCompletionSource _activated = new();
    private readonly TaskCompletionSource<DateTimeOffset?> _completed = new();

    public LiveLogMerger()
        : base()
    {
    }

    public void Activate(CancellationToken cancellationToken, params IPipelineSource[] inputPipelines)
    {
        InputReaders.AddRange(inputPipelines.Select(s => new TimestampedLineReader(s.GetReader(cancellationToken))));
        _activated.SetResult();
    }

    public void Complete()
    {
        _completed.SetResult(null);
    }

    protected override async Task Merge(PipeWriter writer, CancellationToken cancellationToken)
    {
        await _activated.Task;
        if (InputReaders.Count == 0)
        {
            return;
        }

        var timestampTasks = new Task<DateTimeOffset?>[InputReaders.Count + 1];
        for (int i = 0; i < InputReaders.Count; i++)
        {
            timestampTasks[i] = InputReaders[i].PeekTimestamp(cancellationToken).AsTask();
        }

        timestampTasks[^1] = _completed.Task;

        while (true)
        {
            await Task.WhenAny(timestampTasks);
            DateTimeOffset? min = null;
            int minIndex = -1;
            for (int i = timestampTasks.Length - 2; i >= 0; i--)
            {
                var current = timestampTasks[i];
                switch (current.Status)
                {
                    case TaskStatus.RanToCompletion:
                        var result = current.Result;
                        if (result == null)
                        {
                            if (timestampTasks.Length == 2)
                            {
                                return;
                            }

                            InputReaders.RemoveAt(i);

                            var newTasks = new Task<DateTimeOffset?>[timestampTasks.Length - 1];
                            Array.Copy(timestampTasks, newTasks, i);
                            Array.Copy(timestampTasks, i + 1, newTasks, i, timestampTasks.Length - i - 1);
                            timestampTasks = newTasks;
                            minIndex--;
                            break;
                        }

                        if (timestampTasks.Length == 2)
                        {
                            // this is the only reader left and there is no active async read call on it,
                            // so we can just copy the contents to output writer.
                            await InputReaders[0].PipeReader.CopyToAsync(writer, cancellationToken);
                            return;
                        }

                        if (min == null || result < min)
                        {
                            min = result;
                            minIndex = i;
                        }

                        break;
                    case TaskStatus.Canceled:
                    case TaskStatus.Faulted:
                        await current;
                        throw new InvalidOperationException("Unreachable"); // UnreachableException in .NET 7;
                    default:
                        break;
                }
            }

            if (minIndex < 0)
            {
                if (_completed.Task.IsCompleted)
                {
                    return;
                }

                continue;
            }

            await InputReaders[minIndex].CopyLineTo(writer, cancellationToken);
            timestampTasks[minIndex] = InputReaders[minIndex].PeekTimestamp(cancellationToken).AsTask();
        }
    }
}

public abstract class LogMerger : IPipelineSource
{
    protected List<TimestampedLineReader> InputReaders { get; init; }

    protected LogMerger(CancellationToken cancellationToken, params IPipelineSource[] inputPipelines)
    {
        InputReaders = inputPipelines.Select(s => new TimestampedLineReader(s.GetReader(cancellationToken))).ToList();
    }

    protected LogMerger()
    {
        InputReaders = [];
    }

    public async Task Process(PipeWriter writer, CancellationToken cancellationToken)
    {
        try
        {
            await Merge(writer, cancellationToken);
            await CompleteReaders();
        }
        catch (Exception e)
        {
            await CompleteReaders(e);
            throw;
        }
    }

    protected abstract Task Merge(PipeWriter writer, CancellationToken cancellationToken);

    protected async ValueTask CompleteReaders(Exception? exception = null)
    {
        foreach (var reader in InputReaders)
        {
            await reader.PipeReader.CompleteAsync(exception);
        }
    }

    protected class TimestampedLineReader
    {
        public TimestampedLineReader(PipeReader pipeReader)
        {
            PipeReader = pipeReader;
        }

        public PipeReader PipeReader { get; init; }

        public async ValueTask<DateTimeOffset?> PeekTimestamp(CancellationToken cancellationToken)
        {
            while (true)
            {
                var results = await PipeReader.ReadAsync(cancellationToken);

                var spacePosition = results.Buffer.PositionOf((byte)' ');
                if (spacePosition == null)
                {
                    PipeReader.AdvanceTo(results.Buffer.Start, results.Buffer.End);
                    if (results.IsCompleted)
                    {
                        return null;
                    }

                    continue;
                }

                if (!TimestampParser.TryParseTimestampFromSequence(results.Buffer.Slice(0, spacePosition.Value), out var dateTimeOffset))
                {
                    throw new InvalidOperationException("Unable to parse timestamp");
                }

                PipeReader.AdvanceTo(results.Buffer.Start, spacePosition.Value);

                return dateTimeOffset;
            }
        }

        public async ValueTask CopyLineTo(PipeWriter writer, CancellationToken cancellationToken)
        {
            while (true)
            {
                var results = await PipeReader.ReadAsync(cancellationToken);
                var newlinePosition = results.Buffer.PositionOf((byte)'\n');
                if (newlinePosition == null)
                {
                    foreach (var segment in results.Buffer)
                    {
                        writer.Write(segment.Span);
                    }

                    if (results.IsCompleted)
                    {
                        WriteNewline(writer);
                    }

                    PipeReader.AdvanceTo(results.Buffer.End, results.Buffer.End);

                    await writer.FlushAsync(cancellationToken);

                    if (results.IsCompleted)
                    {
                        return;
                    }

                    continue;
                }

                foreach (var segment in results.Buffer.Slice(0, newlinePosition.Value))
                {
                    writer.Write(segment.Span);
                }

                writer.Write(results.Buffer.Slice(newlinePosition.Value, 1).FirstSpan);
                var afterNewlinePosition = results.Buffer.GetPosition(1, newlinePosition.Value);
                PipeReader.AdvanceTo(afterNewlinePosition, afterNewlinePosition);
                await writer.FlushAsync(cancellationToken);
                return;
            }
        }

        private static void WriteNewline(PipeWriter writer)
        {
            Span<byte> newline = stackalloc[] { (byte)'\n' };
            writer.Write(newline);
        }
    }
}
