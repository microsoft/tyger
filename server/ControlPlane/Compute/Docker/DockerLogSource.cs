// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.IO.Pipelines;
using Docker.DotNet;
using Docker.DotNet.Models;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Logging;

namespace Tyger.ControlPlane.Compute.Docker;

public class DockerLogSource : ILogSource
{
    private readonly DockerClient _client;
    private readonly ILogArchive _logArchive;
    private readonly IRepository _repository;

    public DockerLogSource(DockerClient client, ILogArchive logArchive, IRepository repository)
    {
        _client = client;
        _logArchive = logArchive;
        _repository = repository;
    }

    public async Task<Pipeline?> GetLogs(long runId, GetLogsOptions options, CancellationToken cancellationToken)
    {
        var run = await _repository.GetRun(runId, cancellationToken);
        switch (run)
        {
            case null:
                return null;
            case { LogsArchivedAt: null }:
                var containers = await _client.Containers.ListContainersAsync(new ContainersListParameters()
                {
                    All = true,
                    Filters = new Dictionary<string, IDictionary<string, bool>>
                    {
                        {"label", new Dictionary<string, bool>{{ $"tyger-run={runId}", true } } }
                    }
                }, cancellationToken);

                if (containers.Count == 0)
                {
                    return null;
                }

                var pipelineSources = containers.ToAsyncEnumerable()
                    .SelectAwait(async c => await GetContainerLogs(
                        c.ID,
                        c.Labels.TryGetValue("tyger-run-container-name", out var prefix) ? $"[{prefix}]" : null,
                        options with { IncludeTimestamps = true },
                        cancellationToken))
                    .ToEnumerable()
                    .ToArray();

                LogMerger logMerger;
                if (options.Follow)
                {
                    var liveLogMerger = new LiveLogMerger();
                    liveLogMerger.Activate(cancellationToken, pipelineSources);
                    logMerger = liveLogMerger;
                }
                else
                {
                    logMerger = new FixedLogMerger(cancellationToken, pipelineSources);
                }

                var pipeline = new Pipeline(logMerger);
                if (!options.IncludeTimestamps)
                {
                    pipeline.AddElement(new LogLineFormatter(false, null));
                }

                return pipeline;
            default:
                return await _logArchive.GetLogs(runId, options, cancellationToken);
        }
    }

    private async Task<IPipelineSource> GetContainerLogs(string containerId, string? prefix, GetLogsOptions options, CancellationToken cancellationToken)
    {
        async Task<IPipelineSource> GetSingleStreamLogs(bool stdout)
        {
            var muliplexedStream = await _client.Containers.GetContainerLogsAsync(containerId, tty: false, new()
            {
                ShowStdout = stdout,
                ShowStderr = !stdout,
                Follow = options.Follow,
                Tail = options.TailLines?.ToString() ?? "all",
                Timestamps = options.IncludeTimestamps,
                Since = options.Since?.ToUnixTimeSeconds().ToString(),
            }, cancellationToken);

            var pipe = new Pipe();

            async Task Copy()
            {
                try
                {
                    await muliplexedStream.CopyOutputToAsync(
                        stdin: Stream.Null,
                        stdout: stdout ? pipe.Writer.AsStream() : Stream.Null,
                        stderr: stdout ? Stream.Null : pipe.Writer.AsStream(),
                        cancellationToken);
                }
                finally
                {
                    pipe.Writer.Complete();
                }
            }

            _ = Copy();

            var pipeline = new Pipeline(new SimplePipelineSource(pipe.Reader));
            pipeline.AddElement(new DockerTimestampedLogReformatter());
            return pipeline;
        }

        var stdout = await GetSingleStreamLogs(true);
        var stderr = await GetSingleStreamLogs(false);
        LogMerger logMerger;
        if (options.Follow)
        {
            var liveLogMerger = new LiveLogMerger();
            liveLogMerger.Activate(cancellationToken, stdout, stderr);
            logMerger = liveLogMerger;
        }
        else
        {
            logMerger = new FixedLogMerger(cancellationToken, stdout, stderr);
        }

        var pipeline = new Pipeline(logMerger);
        if (!string.IsNullOrEmpty(prefix))
        {
            pipeline.AddElement(new LogLineFormatter(options.IncludeTimestamps, prefix));
        }

        return pipeline;
    }
}
