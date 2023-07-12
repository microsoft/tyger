using Tyger.Server.Model;

namespace Tyger.Server.Database;

public interface IRepository
{
    Task<Codespec> UpsertCodespec(string name, Codespec newcodespec, CancellationToken cancellationToken);
    Task<Codespec?> GetLatestCodespec(string name, CancellationToken cancellationToken);
    Task<Codespec?> GetCodespecAtVersion(string name, int version, CancellationToken cancellationToken);

    Task<(IList<Codespec>, string? nextContinuationToken)> GetCodespecs(int limit, string? prefix, string? continuationToken, CancellationToken cancellationToken);
    Task<Run> CreateRun(Run newRun, CancellationToken cancellationToken);
    Task UpdateRun(Run run, bool? resourcesCreated = null, bool? final = null, DateTimeOffset? logsArchivedAt = null, CancellationToken cancellationToken = default);
    Task DeleteRun(long id, CancellationToken cancellationToken);
    Task<(Run run, bool final, DateTimeOffset? logsArchivedAt)?> GetRun(long id, CancellationToken cancellationToken);
    Task<(IList<(Run run, bool final)>, string? nextContinuationToken)> GetRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken);
    Task<IList<Run>> GetPageOfRunsThatNeverGotResources(CancellationToken cancellationToken);
    Task<Model.Buffer?> GetBuffer(string id, CancellationToken cancellationToken);
    Task<Model.Buffer> CreateBuffer(Model.Buffer newBuffer, CancellationToken cancellationToken);
}
