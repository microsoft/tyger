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

    public async Task<Run> CreateRun(Run newRun, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.CreateRun(newRun, cancellationToken), cancellationToken);
    }

    public async Task DeleteRun(long id, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.DeleteRun(id, cancellationToken), cancellationToken);
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

    public async Task<IList<Run>> GetPageOfRunsThatNeverGotResources(CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetPageOfRunsThatNeverGotResources(cancellationToken), cancellationToken);
    }

    public async Task<Run?> GetRun(long id, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetRun(id, cancellationToken), cancellationToken);
    }

    public async Task<(IList<Run>, string? nextContinuationToken)> GetRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.GetRuns(limit, since, continuationToken, cancellationToken), cancellationToken);
    }

    public async Task<Buffer?> UpdateBufferById(string id, string eTag, IDictionary<string, string>? tags, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.UpdateBufferById(id, eTag, tags, cancellationToken), cancellationToken);
    }

    public async Task UpdateRun(Run run, CancellationToken cancellationToken, bool? resourcesCreated = null)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.UpdateRun(run, cancellationToken, resourcesCreated), cancellationToken);
    }

    public async Task<Codespec> UpsertCodespec(string name, Codespec newcodespec, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken => await _repository.UpsertCodespec(name, newcodespec, cancellationToken), cancellationToken);
    }
}
