// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Database;

public interface IRepository
{
    Task<Codespec> UpsertCodespec(string name, Codespec newcodespec, CancellationToken cancellationToken);
    Task<Codespec?> GetLatestCodespec(string name, CancellationToken cancellationToken);
    Task<Codespec?> GetCodespecAtVersion(string name, int version, CancellationToken cancellationToken);
    Task<(IList<Codespec>, string? nextContinuationToken)> GetCodespecs(int limit, string? prefix, string? continuationToken, CancellationToken cancellationToken);

    Task<Run> CreateRunWithIdempotencyKeyGuard(Run newRun, string idempotencyKey, Func<Run, CancellationToken, Task<Run>> createRun, CancellationToken cancellationToken);
    Task<Run> CreateRun(Run newRun, string? idempotencyKey, CancellationToken cancellationToken);
    Task<Run?> CancelRun(long id, CancellationToken cancellationToken);
    Task UpdateRunAsResourcesCreated(long id, Run? run, CancellationToken cancellationToken);
    Task UpdateRunAsFinal(long id, CancellationToken cancellationToken);
    Task UpdateRunAsLogsArchived(long id, CancellationToken cancellationToken);
    Task UpdateRunFromObservedState(ObservedRunState state, (string leaseName, string holder)? leaseHeldCondition, CancellationToken cancellationToken);
    Task DeleteRun(long id, CancellationToken cancellationToken);
    Task<(Run run, DateTimeOffset? modifiedAt, DateTimeOffset? logsArchivedAt, bool final)?> GetRun(long id, CancellationToken cancellationToken);
    Task<IDictionary<RunStatus, long>> GetRunCounts(DateTimeOffset? since, CancellationToken cancellationToken);
    Task<IDictionary<RunStatus, long>> GetRunCountsWithCallbackForNonFinal(DateTimeOffset? since, Func<Run, CancellationToken, Task<Run>> updateRun, CancellationToken cancellationToken);
    Task<(IList<(Run run, bool final)>, string? nextContinuationToken)> GetRuns(int limit, bool onlyResourcesCreated, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken);
    Task<IList<Run>> GetPageOfRunsWhereResourcesNotCreated(CancellationToken cancellationToken);
    Task ListenForNewRuns(Func<IReadOnlyList<Run>, CancellationToken, Task> processRuns, CancellationToken cancellationToken);
    Task ListenForRunUpdates(DateTimeOffset? since, Func<ObservedRunState, CancellationToken, Task> processRunUpdates, CancellationToken cancellationToken);
    Task PruneRunModifedAtIndex(DateTimeOffset cutoff, CancellationToken cancellationToken);

    Task<bool> CheckBuffersExist(ICollection<string> bufferIds, CancellationToken cancellationToken);
    Task<Model.Buffer?> GetBuffer(string id, string eTag, CancellationToken cancellationToken);
    Task<(IList<Model.Buffer>, string? nextContinuationToken)> GetBuffers(IDictionary<string, string>? tags, int limit, string? continuationToken, CancellationToken cancellationToken);
    Task<Model.Buffer?> UpdateBufferById(string id, string eTag, IDictionary<string, string>? tags, CancellationToken cancellationToken);
    Task<Model.Buffer> CreateBuffer(Model.Buffer newBuffer, CancellationToken cancellationToken);

    Task AcquireAndHoldLease(string leaseName, string holder, Func<bool, ValueTask> onLockStateChange, CancellationToken cancellationToken);
}
