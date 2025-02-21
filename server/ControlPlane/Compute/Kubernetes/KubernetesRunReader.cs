// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Runtime.CompilerServices;
using System.Threading.Channels;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.Runs;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public partial class KubernetesRunReader : IRunReader

{
    private readonly Repository _repository;
    private readonly RunChangeFeed _changeFeed;
    private readonly ILogger<KubernetesRunReader> _logger;

    public KubernetesRunReader(Repository repository, RunChangeFeed changeFeed, ILogger<KubernetesRunReader> logger)
    {
        _repository = repository;
        _changeFeed = changeFeed;
        _logger = logger;
    }

    public async Task<IDictionary<RunStatus, long>> GetRunCounts(DateTimeOffset? since, Dictionary<string, string>? tags, CancellationToken cancellationToken)
    {
        return await _repository.GetRunCounts(since, tags, cancellationToken);
    }

    public async Task<(IReadOnlyList<Run>, string? nextContinuationToken)> ListRuns(GetRunsOptions options, CancellationToken cancellationToken)
    {
        var (runInfos, nextContinuationToken) = await _repository.GetRuns(options, cancellationToken);
        return (runInfos.Select(er => er.run).ToList(), nextContinuationToken);
    }

    public async Task<(Run run, DateTimeOffset? modifiedAt, DateTimeOffset? logsArchivedAt, bool final, int tagsVersion)?> GetRun(long id, CancellationToken cancellationToken)
    {
        return await _repository.GetRun(id, cancellationToken);
    }

    public async IAsyncEnumerable<Run> WatchRun(long id, [EnumeratorCancellation] CancellationToken cancellationToken)
    {
        var channel = Channel.CreateBounded<ObservedRunState>(32);
        _changeFeed.RegisterRunObserver(id, channel.Writer);
        try
        {
            if (await _repository.GetRun(id, cancellationToken) is not var (run, lastModifiedAt, _, final, tagsVersion))
            {
                yield break;
            }

            yield return run;

            if (run.Status.IsTerminal())
            {
                yield break;
            }

            await foreach (var state in channel.Reader.ReadAllAsync(cancellationToken))
            {
                if (state.DatabaseUpdatedAt is not null)
                {
                    if (lastModifiedAt is null || state.DatabaseUpdatedAt > lastModifiedAt)
                    {
                        lastModifiedAt = state.DatabaseUpdatedAt;
                    }
                    else
                    {
                        // guard against out-of-order updates
                        continue;
                    }
                }

                Run updatedRun;
                if (state.TagsVersion > tagsVersion)
                {
                    if (await _repository.GetRun(run.Id!.Value, cancellationToken) is not var (latestRun, latestLastModifiedAt, _, _, latestTagsVersion))
                    {
                        yield break;
                    }

                    updatedRun = latestRun;
                    lastModifiedAt = latestLastModifiedAt;
                    tagsVersion = latestTagsVersion;
                }
                else
                {
                    updatedRun = state.ApplyToRun(run);
                }

                if (!updatedRun.Equals(run))
                {
                    run = updatedRun;
                    yield return run;
                }

                if (run.Status!.Value.IsTerminal())
                {
                    _logger.WatchReachedTerminalState(run.Status!.Value, run.Id!.Value);
                    yield break;
                }
            }
        }
        finally
        {
            _changeFeed.UnregisterRunObserver(id, channel.Writer);
        }
    }
}
