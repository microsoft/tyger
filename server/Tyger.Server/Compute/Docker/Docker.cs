using Tyger.Server.Database;
using Tyger.Server.Database.Migrations;
using Tyger.Server.Logging;
using Tyger.Server.Model;
using Tyger.Server.Runs;

namespace Tyger.Server.Compute.Docker;

public static class Docker
{
    public static void AddDocker(this IHostApplicationBuilder builder)
    {
        builder.Services.AddSingleton<IReplicaDatabaseVersionProvider, DockerReplicaDatabaseVersionProvider>();

        builder.Services.AddSingleton<IRunCreator, DockerRunCreator>();
        builder.Services.AddSingleton<IRunReader, DockerRunReader>();
        builder.Services.AddSingleton<IRunUpdater, DockerRunUpdater>();
        builder.Services.AddSingleton<ILogSource, DockerLogSource>();
    }
}

public class DockerReplicaDatabaseVersionProvider : IReplicaDatabaseVersionProvider
{
    public IAsyncEnumerable<(Uri, DatabaseVersion)> GetDatabaseVersionsOfReplicas(CancellationToken cancellationToken)
    {
        return AsyncEnumerable.Empty<(Uri, DatabaseVersion)>();
    }
}

public class DockerRunReader : IRunReader
{
    public Task<Run?> GetRun(long id, CancellationToken cancellationToken)
    {
        throw new NotImplementedException();
    }

    public Task<(IReadOnlyList<Run>, string? nextContinuationToken)> ListRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        throw new NotImplementedException();
    }

    public IAsyncEnumerable<Run> WatchRun(long id, CancellationToken cancellationToken)
    {
        throw new NotImplementedException();
    }
}

public class DockerRunCreator : IRunCreator
{
    public Task<Run> CreateRun(Run newRun, CancellationToken cancellationToken)
    {
        throw new NotImplementedException();
    }
}

public class DockerRunUpdater : IRunUpdater
{
    public Task<Run?> CancelRun(long id, CancellationToken cancellationToken)
    {
        throw new NotImplementedException();
    }
}

public class DockerLogSource : ILogSource
{
    public Task<Pipeline?> GetLogs(long runId, GetLogsOptions options, CancellationToken cancellationToken)
    {
        throw new NotImplementedException();
    }
}
