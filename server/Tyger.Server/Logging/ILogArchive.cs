namespace Tyger.Server.Logging;

public interface ILogArchive : ILogSource
{
    Task ArchiveLogs(long runId, Stream logs, CancellationToken cancellationToken);
}
