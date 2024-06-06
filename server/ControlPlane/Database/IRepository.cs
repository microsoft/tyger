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
    Task<Run> CreateRun(Run newRun, CancellationToken cancellationToken);
    Task UpdateRun(Run run, bool? resourcesCreated = null, CancellationToken cancellationToken = default);
    Task DeleteRun(long id, CancellationToken cancellationToken);
    Task<Run?> GetRun(long id, CancellationToken cancellationToken);
    Task<(IList<Run>, string? nextContinuationToken)> GetRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken);
    Task<IList<Run>> GetPageOfRunsThatNeverGotResources(CancellationToken cancellationToken);
    Task<Model.Buffer?> GetBuffer(string id, string eTag, CancellationToken cancellationToken);
    Task<(IList<Model.Buffer>, string? nextContinuationToken)> GetBuffers(IDictionary<string, string>? tags, int limit, string? continuationToken, CancellationToken cancellationToken);
    Task<Model.Buffer?> UpdateBufferById(string id, string eTag, IDictionary<string, string>? tags, CancellationToken cancellationToken);
    Task<Model.Buffer> CreateBuffer(Model.Buffer newBuffer, CancellationToken cancellationToken);
}
