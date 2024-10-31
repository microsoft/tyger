// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Text.Json;
using Npgsql;
using Polly;
using Tyger.ControlPlane.Model;
using Buffer = Tyger.ControlPlane.Model.Buffer;

namespace Tyger.ControlPlane.Database;

public class RepositoryWithRetry : IRepository
{
    private readonly Repository _repository;
    private readonly ResiliencePipeline _resiliencePipeline;

    public RepositoryWithRetry(
        ResiliencePipeline resiliencePipeline,
        NpgsqlDataSource dataSource,
        JsonSerializerOptions serializerOptions,
        ILoggerFactory loggerFactory)
    {
        _repository = new(dataSource, serializerOptions, loggerFactory.CreateLogger<Repository>());
        _resiliencePipeline = resiliencePipeline;
    }

    public async Task<Buffer> CreateBuffer(Buffer newBuffer, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.CreateBuffer(newBuffer, cancellationToken), cancellationToken);
    }

    public async Task<Run> CreateRunWithIdempotencyKeyGuard(Run newRun, string idempotencyKey, Func<Run, CancellationToken, Task<Run>> createRun, CancellationToken cancellationToken)
    {
        // NOTE: not retrying for this method, because we don't want createRun to be called multiple times
        return await _repository.CreateRunWithIdempotencyKeyGuard(newRun, idempotencyKey, createRun, cancellationToken);
    }

    public async Task<Run> CreateRun(Run newRun, string? idempotencyKey, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.CreateRun(newRun, idempotencyKey, cancellationToken), cancellationToken);
    }

    public async Task DeleteRun(long id, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.DeleteRun(id, cancellationToken), cancellationToken);
    }

    public async Task<bool> CheckBuffersExist(ICollection<string> bufferIds, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.CheckBuffersExist(bufferIds, cancellationToken), cancellationToken);
    }

    public async Task<Buffer?> GetBuffer(string id, string eTag, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetBuffer(id, eTag, cancellationToken), cancellationToken);
    }

    public async Task<(IList<Buffer>, string? nextContinuationToken)> GetBuffers(IDictionary<string, string>? tags, int limit, string? continuationToken, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetBuffers(tags, limit, continuationToken, cancellationToken), cancellationToken);
    }

    public async Task<Codespec?> GetCodespecAtVersion(string name, int version, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetCodespecAtVersion(name, version, cancellationToken), cancellationToken);
    }

    public async Task<(IList<Codespec>, string? nextContinuationToken)> GetCodespecs(int limit, string? prefix, string? continuationToken, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetCodespecs(limit, prefix, continuationToken, cancellationToken), cancellationToken);
    }

    public async Task<Codespec?> GetLatestCodespec(string name, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetLatestCodespec(name, cancellationToken), cancellationToken);
    }

    public async Task<IList<Run>> GetPageOfRunsWhereResourcesNotCreated(CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetPageOfRunsWhereResourcesNotCreated(cancellationToken), cancellationToken);
    }

    public async Task<(Run run, DateTimeOffset? modifiedAt, DateTimeOffset? logsArchivedAt, bool final)?> GetRun(long id, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetRun(id, cancellationToken), cancellationToken);
    }

    public async Task<IDictionary<RunStatus, long>> GetRunCounts(DateTimeOffset? since, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetRunCounts(since, cancellationToken), cancellationToken);
    }

    public async Task<IDictionary<RunStatus, long>> GetRunCountsWithCallbackForNonFinal(DateTimeOffset? since, Func<Run, CancellationToken, Task<Run>> updateRun, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetRunCountsWithCallbackForNonFinal(since, updateRun, cancellationToken), cancellationToken);
    }

    public async Task<(IList<(Run run, bool final)>, string? nextContinuationToken)> GetRuns(int limit, bool onlyResourcesCreated, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetRuns(limit, onlyResourcesCreated, since, continuationToken, cancellationToken), cancellationToken);
    }

    public async Task<Run?> CancelRun(long id, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.CancelRun(id, cancellationToken), cancellationToken);
    }

    public async Task<Buffer?> UpdateBufferById(string id, string eTag, IDictionary<string, string>? tags, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.UpdateBufferById(id, eTag, tags, cancellationToken), cancellationToken);
    }

    public async Task UpdateRunAsResourcesCreated(long id, Run? run, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.UpdateRunAsResourcesCreated(id, run, cancellationToken), cancellationToken);
    }

    public async Task UpdateRunAsFinal(long id, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.UpdateRunAsFinal(id, cancellationToken), cancellationToken);
    }

    public async Task UpdateRunAsLogsArchived(long id, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.UpdateRunAsLogsArchived(id, cancellationToken), cancellationToken);
    }

    public async Task UpdateRunFromObservedState(ObservedRunState state, (string leaseName, string holder)? leaseHeldCondition, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.UpdateRunFromObservedState(state, leaseHeldCondition, cancellationToken), cancellationToken);
    }

    public async Task<Codespec> UpsertCodespec(string name, Codespec newcodespec, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.UpsertCodespec(name, newcodespec, cancellationToken), cancellationToken);
    }

    public async Task ListenForNewRuns(Func<IReadOnlyList<Run>, CancellationToken, Task> processRuns, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.ListenForNewRuns(processRuns, cancellationToken), cancellationToken);
    }

    public async Task ListenForRunUpdates(DateTimeOffset? since, Func<ObservedRunState, CancellationToken, Task> processRunUpdates, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.ListenForRunUpdates(since, processRunUpdates, cancellationToken), cancellationToken);
    }

    public async Task PruneRunModifedAtIndex(DateTimeOffset cutoff, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.PruneRunModifedAtIndex(cutoff, cancellationToken), cancellationToken);
    }

    public async Task AcquireAndHoldLease(string leaseName, string holder, Func<bool, ValueTask> onLockStateChange, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.AcquireAndHoldLease(leaseName, holder, onLockStateChange, cancellationToken), cancellationToken);
    }
}
