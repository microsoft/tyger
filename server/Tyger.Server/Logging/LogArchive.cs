using System.Buffers;
using System.ComponentModel.DataAnnotations;
using System.Globalization;
using System.IO.Pipelines;
using Azure.Core;
using Azure.Storage.Blobs;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;

namespace Tyger.Server.Logging;

public static class LogArchiveRegistration
{
    public static void AddLogArchive(this IServiceCollection services)
    {
        services.AddOptions<LogArchiveOptions>().BindConfiguration("logArchive").ValidateDataAnnotations().ValidateOnStart();
        services.AddSingleton<ILogArchive, LogArchive>();
        services.AddHealthChecks().AddCheck<LogArchive>("logArchive");
    }
}

public class LogArchiveOptions
{
    [Required]
    public string StorageAccountEndpoint { get; set; } = null!;
}

public class LogArchive : ILogArchive, IHealthCheck
{
    private const string LineCountMetadataKey = "lineCount";
    private readonly BlobContainerClient _containerClient;
    private readonly ILogger<LogArchive> _logger;

    public LogArchive(IOptions<LogArchiveOptions> options, TokenCredential tokenCredential, ILogger<LogArchive> logger)
    {
        _containerClient = new BlobServiceClient(new Uri(options.Value.StorageAccountEndpoint), tokenCredential).GetBlobContainerClient("runs");
        _logger = logger;
    }

    public async Task ArchiveLogs(long runId, Pipeline pipeline, CancellationToken cancellationToken)
    {
        var blobClient = GetLogsBlobClient(runId);

        var pipe = new Pipe();
        _ = pipeline.Process(pipe.Writer, cancellationToken);

        using var lineCountingStream = new LineCountingReadStream(pipe.Reader.AsStream());
        await blobClient.UploadAsync(lineCountingStream, overwrite: true, cancellationToken: cancellationToken);
        await blobClient.SetMetadataAsync(new Dictionary<string, string> { { LineCountMetadataKey, lineCountingStream.LineCount.ToString(CultureInfo.InvariantCulture) } }, cancellationToken: cancellationToken);

        _logger.ArchivedLogsForRun(runId);
    }

    public async Task<Pipeline?> GetLogs(long runId, GetLogsOptions options, CancellationToken cancellationToken)
    {
        _logger.RetrievingAchivedLogsForRun(runId);
        try
        {
            var response = await GetLogsBlobClient(runId).DownloadStreamingAsync(cancellationToken: cancellationToken);
            var pipeline = new Pipeline(new SimplePipelineSource(response.Value.Content));

            if (options.IncludeTimestamps && options.TailLines == null && options.Since == null)
            {
                // fast path: the logs can be returned without modification
                return pipeline;
            }

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

            return pipeline.AddElement(GetLogFilterPipelineElement(options.IncludeTimestamps, skipLines, options.Since));
        }
        catch (Azure.RequestFailedException e) when (e.ErrorCode == "BlobNotFound")
        {
            return null;
        }
    }

    public static IPipelineElement GetLogFilterPipelineElement(bool includeTimestamps, int skipLines, DateTimeOffset? since) =>
        new LogFilter(includeTimestamps, skipLines, since);

    private BlobClient GetLogsBlobClient(long runId)
    {
        return _containerClient.GetBlobClient(runId.ToString(CultureInfo.InvariantCulture));
    }

    public async Task<HealthCheckResult> CheckHealthAsync(HealthCheckContext context, CancellationToken cancellationToken = default)
    {
        await GetLogsBlobClient(-1).ExistsAsync(cancellationToken);
        return HealthCheckResult.Healthy();
    }

    private sealed class LogFilter : IPipelineElement
    {
        private readonly bool _includeTimestamps;

        private int _skipLines;
        private DateTimeOffset? _since;

        public LogFilter(bool includeTimestamps, int skipLines, DateTimeOffset? since)
        {
            _includeTimestamps = includeTimestamps;
            _skipLines = skipLines;
            _since = since;
        }

        public async Task Process(PipeReader reader, PipeWriter writer, CancellationToken cancellationToken)
        {
            bool atBeginningOfLine = true;

            while (true)
            {
                ReadResult result = await reader.ReadAsync(cancellationToken);
                ReadOnlySequence<byte> buffer = result.Buffer;

                var position = ProcessBuffer(buffer, ref atBeginningOfLine, writer);
                await writer.FlushAsync(cancellationToken);

                if (result.IsCompleted)
                {
                    break;
                }

                // Tell the PipeReader how much of the buffer has been consumed.
                reader.AdvanceTo(position, buffer.End);
            }
        }

        private SequencePosition ProcessBuffer(in ReadOnlySequence<byte> sequence, ref bool atBeginningOfLine, PipeWriter writer)
        {
            var reader = new SequenceReader<byte>(sequence);
            while (reader.Remaining > 0)
            {
                if (_skipLines > 0)
                {
                    // skip to the end of the current line

                    if (reader.TryAdvanceTo((byte)'\n', advancePastDelimiter: true))
                    {
                        atBeginningOfLine = true;
                        _skipLines--;
                        continue;
                    }

                    reader.AdvanceToEnd();
                    return reader.Position;
                }

                if (atBeginningOfLine && (_since != null || !_includeTimestamps))
                {
                    // whatever comes before the first space is the timestamp
                    var timestampStartPosition = reader.Position;
                    if (!reader.TryAdvanceTo((byte)' ', advancePastDelimiter: true))
                    {
                        return reader.Position;
                    }

                    atBeginningOfLine = false;
                    var timestampSequence = sequence.Slice(timestampStartPosition, reader.Position);
                    if (_since != null)
                    {
                        if (!TimestampParser.TryParseTimestampFromSequence(timestampSequence, out var timestamp))
                        {
                            throw new InvalidOperationException("Failed to parse logs");
                        }

                        if (timestamp > _since.Value)
                        {
                            // all following lines will have timestamps >= so we can clear
                            // this so we don't have to parse more timestamps.
                            _since = null;
                        }
                        else
                        {
                            _skipLines = 1;
                            continue;
                        }
                    }

                    if (_includeTimestamps)
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
    }
}
