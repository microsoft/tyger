using Tyger.Server.Model;

namespace Tyger.Server.Database;

public interface IRepository
{
    Task<int> UpsertCodespec(string name, NewCodespec codespec, CancellationToken cancellationToken);
    Task<Codespec?> GetLatestCodespec(string name, CancellationToken cancellationToken);
    Task<Codespec?> GetCodespecAtVersion(string name, int version, CancellationToken cancellationToken);

    Task<Run> CreateRun(NewRun newRun, CancellationToken cancellationToken);
    Task UpdateRun(Run run, bool? resourcesCreated = null, bool? final = null, DateTimeOffset? logsArchivedAt = null, CancellationToken cancellationToken = default);
    Task DeleteRun(long id, CancellationToken cancellationToken);
    Task<(Run run, bool final, DateTimeOffset? logsArchivedAt)?> GetRun(long id, CancellationToken cancellationToken);
    Task<(IList<(Run run, bool final)>, string? nextContinuationToken)> GetRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken);
    Task<IList<Run>> GetPageOfRunsThatNeverGotResources(CancellationToken cancellationToken);
}