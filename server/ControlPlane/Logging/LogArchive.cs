// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Buffers;
using System.Globalization;
using System.IO.Compression;
using System.IO.Pipelines;
using System.Text.RegularExpressions;
using Azure.Core;
using Azure.Storage.Blobs;
using Microsoft.Extensions.Diagnostics.HealthChecks;
using Microsoft.Extensions.Options;

namespace Tyger.ControlPlane.Logging;

public static class Logs
{
    public static void AddLogArchive(this IHostApplicationBuilder builder)
    {
        if (builder.Configuration.GetSection("logArchive").Exists())
        {
            builder.Services.AddSingleton<LogArchive>();
            builder.Services.AddSingleton<ILogArchive>(sp => sp.GetRequiredService<LogArchive>());

            switch (cloud: builder.Configuration.GetSection("logArchive:cloudStorage").Exists(), local: builder.Configuration.GetSection("logArchive:localStorage").Exists())
            {
                case (cloud: true, local: false):
                    builder.Services.AddOptions<CloudLogArchiveOptions>().BindConfiguration("logArchive:cloudStorage").ValidateDataAnnotations().ValidateOnStart();
                    builder.Services.AddSingleton<AzureStorageLogArchiveProvider>();
                    builder.Services.AddSingleton<ILogArchiveProvider>(sp => sp.GetRequiredService<AzureStorageLogArchiveProvider>());
                    builder.Services.AddSingleton<IHostedService>(sp => sp.GetRequiredService<AzureStorageLogArchiveProvider>());
                    builder.Services.AddHealthChecks().AddCheck<AzureStorageLogArchiveProvider>("logArchive");
                    return;
                case (cloud: false, local: true):
                    builder.Services.AddOptions<LocalLogArchiveOptions>().BindConfiguration("logArchive:localStorage").ValidateDataAnnotations().ValidateOnStart();
                    builder.Services.AddSingleton<LocalLogArchiveProvider>();
                    builder.Services.AddSingleton<ILogArchiveProvider>(sp => sp.GetRequiredService<LocalLogArchiveProvider>());
                    return;
                case (cloud: true, local: true):
                    throw new InvalidOperationException("Only one of 'logArchive:cloudStorage' and 'logArchive:fileStorage' can be specified");
                case (cloud: false, local: false):
                    break;
            }
        }

        builder.Services.AddSingleton<ILogArchive, NullLogArchive>();
    }
}

public class CloudLogArchiveOptions
{
    public string StorageAccountEndpoint { get; set; } = null!;
}

public class LocalLogArchiveOptions
{
    public string LogsDirectory { get; set; } = null!;
}

public interface ILogArchiveProvider
{
    Task StoreLogs(long runId, Stream stream, CancellationToken cancellationToken);
    Task<(Stream stream, long lineCount)?> GetLogs(long runId, CancellationToken cancellationToken);
}

public class AzureStorageLogArchiveProvider : ILogArchiveProvider, IHostedService, IHealthCheck
{
    private const string LineCountMetadataKey = "lineCount";
    private readonly BlobContainerClient _containerClient;

    public AzureStorageLogArchiveProvider(IOptions<CloudLogArchiveOptions> options, TokenCredential tokenCredential)
    {
        _containerClient = new BlobServiceClient(new Uri(options.Value.StorageAccountEndpoint), tokenCredential).GetBlobContainerClient("runs");
    }

    public async Task StoreLogs(long runId, Stream stream, CancellationToken cancellationToken)
    {
        var blobClient = GetLogsBlobClient(runId);

        using var lineCountingStream = new LineCountingReadStream(stream);
        await blobClient.UploadAsync(lineCountingStream, overwrite: true, cancellationToken: cancellationToken);
        await blobClient.SetMetadataAsync(
            new Dictionary<string, string> { { LineCountMetadataKey, lineCountingStream.LineCount.ToString(CultureInfo.InvariantCulture) } },
            cancellationToken: cancellationToken);
    }

    public async Task<(Stream stream, long lineCount)?> GetLogs(long runId, CancellationToken cancellationToken)
    {
        var blobClient = _containerClient.GetBlobClient(runId.ToString(CultureInfo.InvariantCulture));
        try
        {
            var response = await blobClient.DownloadStreamingAsync(cancellationToken: cancellationToken);
            return (
                response.Value.Content,
                long.Parse(response.Value.Details.Metadata[LineCountMetadataKey])
            );
        }
        catch (Azure.RequestFailedException e) when (e.ErrorCode == "BlobNotFound")
        {
            return null;
        }
    }

    public async Task<HealthCheckResult> CheckHealthAsync(HealthCheckContext context, CancellationToken cancellationToken = default)
    {
        await GetLogsBlobClient(-1).ExistsAsync(cancellationToken);
        return HealthCheckResult.Healthy();
    }

    async Task IHostedService.StartAsync(CancellationToken cancellationToken)
    {
        await _containerClient.CreateIfNotExistsAsync(cancellationToken: cancellationToken);
    }

    Task IHostedService.StopAsync(CancellationToken cancellationToken) => Task.CompletedTask;

    private BlobClient GetLogsBlobClient(long runId)
    {
        return _containerClient.GetBlobClient(runId.ToString(CultureInfo.InvariantCulture));
    }
}

public partial class LocalLogArchiveProvider : ILogArchiveProvider
{
    private readonly string _logsDirectory;
    private readonly ILogger<LocalLogArchiveProvider> _logger;

    public LocalLogArchiveProvider(IOptions<LocalLogArchiveOptions> options, ILogger<LocalLogArchiveProvider> logger)
    {
        _logsDirectory = options.Value.LogsDirectory;
        _logger = logger;

        Directory.CreateDirectory(_logsDirectory);
    }

    public async Task StoreLogs(long runId, Stream stream, CancellationToken cancellationToken)
    {
        var tempFilePath = Path.Combine(_logsDirectory, $"tmp-{Guid.NewGuid()}.gz");

        try
        {
            int lineCount = 0;
            using (var fileStream = File.OpenWrite(tempFilePath))
            using (var gzipStream = new GZipStream(fileStream, CompressionLevel.Optimal))
            using (var lineCountingStream = new LineCountingReadStream(stream))
            {
                await lineCountingStream.CopyToAsync(gzipStream, cancellationToken);
                lineCount = lineCountingStream.LineCount;
            }

            var finalFilePath = Path.Combine(_logsDirectory, $"{runId.ToString(CultureInfo.InvariantCulture)}-{lineCount}.gz");
            File.Move(tempFilePath, finalFilePath, overwrite: true);
        }
        finally
        {
            try
            {
                if (File.Exists(tempFilePath))
                {
                    File.Delete(tempFilePath);
                }
            }
            catch
            {
            }
        }
    }

    public Task<(Stream stream, long lineCount)?> GetLogs(long runId, CancellationToken cancellationToken)
    {
        foreach (var path in Directory.GetFiles(_logsDirectory, $"{runId.ToString(CultureInfo.InvariantCulture)}-*.gz", SearchOption.TopDirectoryOnly))
        {
            if (FileNameRegex().Match(Path.GetFileName(path)) is { Success: true } match)
            {
                var lineCount = long.Parse(match.Groups[2].Value);
                var fileStream = File.OpenRead(path);
                var decompressStream = new GZipStream(fileStream, CompressionMode.Decompress);
                return Task.FromResult<(Stream stream, long lineCount)?>((decompressStream, lineCount));
            }
            else
            {
                _logger.LocalLogFileDoesNotHaveExpectedName(path);
            }
        }

        return Task.FromResult<(Stream stream, long lineCount)?>(null);
    }

    [GeneratedRegex(@"^(\d+)-(\d+)\.gz$")]
    private static partial Regex FileNameRegex();
}

public class LogArchive : ILogArchive
{
    private readonly ILogArchiveProvider _provider;
    private readonly ILogger<LogArchive> _logger;

    public LogArchive(ILogArchiveProvider provider, ILogger<LogArchive> logger)
    {
        _provider = provider;
        _logger = logger;
    }

    public async Task ArchiveLogs(long runId, Pipeline pipeline, CancellationToken cancellationToken)
    {
        var pipe = new Pipe();
        _ = pipeline.Process(pipe.Writer, cancellationToken);
        await _provider.StoreLogs(runId, pipe.Reader.AsStream(), cancellationToken);
        _logger.ArchivedLogsForRun(runId);
    }

    public async Task<Pipeline?> GetLogs(long runId, GetLogsOptions options, CancellationToken cancellationToken)
    {
        _logger.RetrievingArchivedLogsForRun(runId);
        var result = await _provider.GetLogs(runId, cancellationToken);
        if (result == null)
        {
            return null;
        }

        var pipeline = new Pipeline(new SimplePipelineSource(result.Value.stream));

        if (options.IncludeTimestamps && options.TailLines == null && options.Since == null)
        {
            // fast path: the logs can be returned without modification
            return pipeline;
        }

        // Turn the TailLines parameter into a number of lines to skip by using the line count
        // that we stored as metadata on the blob.
        // This allows us to avoid buffering potentially large amounts of the log.
        long skipLines = 0;
        if (options.TailLines.HasValue)
        {
            skipLines = Math.Max(result.Value.lineCount - options.TailLines.Value, 0);
        }

        return pipeline.AddElement(GetLogFilterPipelineElement(options.IncludeTimestamps, skipLines, options.Since));
    }

    public static IPipelineElement GetLogFilterPipelineElement(bool includeTimestamps, long skipLines, DateTimeOffset? since) =>
        new LogFilter(includeTimestamps, skipLines, since);

    private sealed class LogFilter : IPipelineElement
    {
        private readonly bool _includeTimestamps;

        private long _skipLines;
        private DateTimeOffset? _since;

        public LogFilter(bool includeTimestamps, long skipLines, DateTimeOffset? since)
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

public class NullLogArchive : ILogArchive
{
    public Task ArchiveLogs(long runId, Pipeline pipeline, CancellationToken cancellationToken)
    {
        return Task.CompletedTask;
    }

    public Task<Pipeline?> GetLogs(long runId, GetLogsOptions options, CancellationToken cancellationToken)
    {
        return Task.FromResult<Pipeline?>(null);
    }
}
