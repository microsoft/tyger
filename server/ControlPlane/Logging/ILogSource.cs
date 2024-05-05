// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Logging;

public interface ILogSource
{
    Task<Pipeline?> GetLogs(long runId, GetLogsOptions options, CancellationToken cancellationToken);
}

public record GetLogsOptions
{
    public bool IncludeTimestamps { get; init; }
    public int? TailLines { get; init; }
    public DateTimeOffset? Since { get; init; }
    public bool Follow { get; init; }
}
