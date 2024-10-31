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
    private readonly IRepository _repository;
    private readonly RunChangeFeed _changeFeed;

    public KubernetesRunReader(
        IRepository repository,
        RunChangeFeed changeFeed)
    {
        _repository = repository;
        _changeFeed = changeFeed;
    }

    public async Task<IDictionary<RunStatus, long>> GetRunCounts(DateTimeOffset? since, CancellationToken cancellationToken)
    {
        return await _repository.GetRunCounts(since, cancellationToken);
    }

    public async Task<(IReadOnlyList<Run>, string? nextContinuationToken)> ListRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        var (runInfos, nextContinuationToken) = await _repository.GetRuns(limit, false, since, continuationToken, cancellationToken);
        return (runInfos.Select(er => er.run).ToList(), nextContinuationToken);
    }

    public async Task<(Run run, DateTimeOffset? modifiedAt, DateTimeOffset? logsArchivedAt, bool final)?> GetRun(long id, CancellationToken cancellationToken)
    {
        return await _repository.GetRun(id, cancellationToken);
    }

    public async IAsyncEnumerable<Run> WatchRun(long id, [EnumeratorCancellation] CancellationToken cancellationToken)
    {
        var channel = Channel.CreateBounded<ObservedRunState>(32);
        _changeFeed.RegisterRunObserver(id, channel.Writer);
        try
        {
            if (await _repository.GetRun(id, cancellationToken) is not var (run, lastModifiedAt, _, final))
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

                var updatedRun = state.ApplyToRun(run);
                if (!updatedRun.Equals(run))
                {
                    run = updatedRun;
                    yield return run;
                }

                if (run.Status!.Value.IsTerminal())
                {
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
