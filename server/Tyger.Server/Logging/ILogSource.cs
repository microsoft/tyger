using System.IO.Pipelines;

namespace Tyger.Server.Logging;

public interface ILogSource
{
    Task<bool> TryGetLogs(long runId, GetLogsOptions options, PipeWriter outputWriter, CancellationToken cancellationToken);
}

public record GetLogsOptions
{
    public bool IncludeTimestamps { get; init; }
    public int? TailLines { get; init; }
    public DateTimeOffset? Since { get; init; }
    public bool Follow { get; init; }
    public bool Previous { get; init; }
}
