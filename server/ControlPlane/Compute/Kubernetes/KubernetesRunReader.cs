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
    private readonly ILogger<KubernetesRunReader> _logger;

    public KubernetesRunReader(
        IRepository repository,
        RunChangeFeed changeFeed,
        ILogger<KubernetesRunReader> logger)
    {
        _repository = repository;
        _changeFeed = changeFeed;
        _logger = logger;
    }

    public async Task<(IReadOnlyList<Run>, string? nextContinuationToken)> ListRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        (var partialRuns, var nextContinuationToken) = await _repository.GetRuns(limit, since, continuationToken, cancellationToken);

        return (partialRuns.AsReadOnly(), nextContinuationToken);
    }

    public async Task<Run?> GetRun(long id, CancellationToken cancellationToken)
    {
        return await _repository.GetRun(id, cancellationToken);
    }

    public async IAsyncEnumerable<Run> WatchRun(long id, [EnumeratorCancellation] CancellationToken cancellationToken)
    {
        var channel = Channel.CreateBounded<ObservedRunState>(32);
        _changeFeed.RegisterRunObserver(id, channel.Writer);
        try
        {
            var run = await GetRun(id, cancellationToken);
            if (run is null)
            {
                yield break;
            }

            if (run.Status!.Value.IsTerminal())
            {
                yield return run;
                yield break;
            }

            await foreach (var state in channel.Reader.ReadAllAsync(cancellationToken))
            {
                var updatedRun = state.UpdateRun(run);
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
