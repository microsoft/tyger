using System.Buffers;
using System.Buffers.Text;
using System.ComponentModel.DataAnnotations;
using System.Globalization;
using System.IO.Pipelines;
using Azure.Storage.Blobs;
using Microsoft.Extensions.Options;

namespace Tyger.Server.Logging;

public static class LogArchiveRegistration
{
    public static void AddLogArchive(this IServiceCollection services)
    {
        services.AddOptions<LogArchiveOptions>().BindConfiguration("logArchive").ValidateDataAnnotations().ValidateOnStart();
        services.AddSingleton<ILogArchive, LogArchive>();
    }
}

public class LogArchiveOptions
{
    [Required]
    public string StorageAccountConnectionString { get; set; } = null!;
}

public class LogArchive : ILogArchive
{
    private const string LineCountMetadataKey = "lineCount";
    private readonly string _storageAccountConnectionString;
    private readonly ILogger<LogArchive> _logger;

    public LogArchive(IOptions<LogArchiveOptions> options, ILogger<LogArchive> logger)
    {
        _storageAccountConnectionString = options.Value.StorageAccountConnectionString;
        _logger = logger;
    }

    public async Task ArchiveLogs(long runId, Stream logs, CancellationToken cancellationToken)
    {
        var blobClient = GetLogsBlobClient(runId);

        using var lineCountingStream = new LineCountingReadStream(logs);
        await blobClient.UploadAsync(lineCountingStream, overwrite: true, cancellationToken: cancellationToken);
        await blobClient.SetMetadataAsync(new Dictionary<string, string> { { LineCountMetadataKey, lineCountingStream.LineCount.ToString(CultureInfo.InvariantCulture) } }, cancellationToken: cancellationToken);

        _logger.ArchivedLogsForRun(runId);
    }

    public async Task<bool> TryGetLogs(long runId, GetLogsOptions options, PipeWriter outputWriter, CancellationToken cancellationToken)
    {
        if (options.Previous)
        {
            throw new ValidationException("Logs from a previous execution were not found.");
        }

        _logger.RetrievingAchivedLogsForRun(runId);

        var blobClient = GetLogsBlobClient(runId);
        try
        {
            if (options.IncludeTimestamps && options.TailLines == null && options.Since == null)
            {
                // fast path: the logs can be returned without modification
                await blobClient.DownloadToAsync(outputWriter.AsStream(), cancellationToken);
                return true;
            }

            var response = await GetLogsBlobClient(runId).DownloadStreamingAsync(cancellationToken: cancellationToken);

            // Turn the TailLines parameter into a number of lines to skip by using the line count
            // that we stored as metadata on the blob.
            // This allows us to avoid buffering potentially large amounts of the log.
            int skipLines = 0;
            if (options.TailLines.HasValue &&
                response.Value.Details.Metadata.TryGetValue(LineCountMetadataKey, out var lineCountString) &&
                int.TryParse(lineCountString, out var lineCount))
            {
                skipLines = lineCount - options.TailLines.Value;
            }

            using Stream logs = response.Value.Content;
            await WriteFilteredLogStream(logs, options.IncludeTimestamps, skipLines, options.Since, outputWriter, cancellationToken);
            return true;
        }
        catch (Azure.RequestFailedException e) when (e.ErrorCode == "BlobNotFound")
        {
            return false;
        }
    }

    public static async Task WriteFilteredLogStream(Stream logs, bool timestamps, int skipLines, DateTimeOffset? since, PipeWriter outputWriter, CancellationToken cancellationToken)
    {
        var reader = PipeReader.Create(logs);
        bool atBeginningOfLine = true;

        while (true)
        {
            ReadResult result = await reader.ReadAsync(cancellationToken);
            ReadOnlySequence<byte> buffer = result.Buffer;

            var position = ProcessBuffer(buffer, timestamps, since, ref skipLines, ref atBeginningOfLine, outputWriter);
            await outputWriter.FlushAsync(cancellationToken);

            if (result.IsCompleted)
            {
                break;
            }

            // Tell the PipeReader how much of the buffer has been consumed.
            reader.AdvanceTo(position, buffer.End);
        }

        await reader.CompleteAsync();
    }

    private static SequencePosition ProcessBuffer(in ReadOnlySequence<byte> sequence, bool includeTimestamps, DateTimeOffset? since, ref int skipLines, ref bool atBeginningOfLine, PipeWriter writer)
    {
        var reader = new SequenceReader<byte>(sequence);
        while (reader.Remaining > 0)
        {
            if (skipLines > 0)
            {
                // skip to the end of the current line

                if (reader.TryAdvanceTo((byte)'\n', advancePastDelimiter: true))
                {
                    atBeginningOfLine = true;
                    skipLines--;
                    continue;
                }

                reader.AdvanceToEnd();
                return reader.Position;
            }

            if (atBeginningOfLine && (since != null || !includeTimestamps))
            {
                // whatever comes before the first space is the timestamp
                var timestampStartPosition = reader.Position;
                if (!reader.TryAdvanceTo((byte)' ', advancePastDelimiter: true))
                {
                    return reader.Position;
                }

                atBeginningOfLine = false;
                var timestampSequence = sequence.Slice(timestampStartPosition, reader.Position);
                if (since != null)
                {
                    if (!TryParseTimestampFromSequence(timestampSequence, out var timestamp))
                    {
                        throw new InvalidOperationException("Failed to parse logs");
                    }

                    if (timestamp > since.Value)
                    {
                        // all following lines will have timestamps >= so we can clear
                        // this so we don't have to parse more timestamps.
                        since = null;
                    }
                    else
                    {
                        skipLines = 1;
                        continue;
                    }
                }

                if (includeTimestamps)
                {
                    foreach (var segment in timestampSequence)
                    {
                        writer.Write(segment.Span);
                    }
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

    private BlobClient GetLogsBlobClient(long runId)
    {
        return new BlobClient(_storageAccountConnectionString, "runs", runId.ToString(CultureInfo.InvariantCulture));
    }

    private static bool TryParseTimestampFromSpan(in ReadOnlySpan<byte> byteSpan, out DateTimeOffset timestamp)
    {
        return Utf8Parser.TryParse(byteSpan, out timestamp, out _, 'O');
    }

    private static bool TryParseTimestampFromSequence(in ReadOnlySequence<byte> sequence, out DateTimeOffset timestamp)
    {
        if (sequence.IsSingleSegment)
        {
            return TryParseTimestampFromSpan(sequence.FirstSpan, out timestamp);
        }

        Span<byte> dateSpan = stackalloc byte[(int)sequence.Length];
        sequence.CopyTo(dateSpan);
        return TryParseTimestampFromSpan(dateSpan, out timestamp);
    }
}
