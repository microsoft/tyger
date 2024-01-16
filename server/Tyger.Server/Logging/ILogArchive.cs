// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.Server.Logging;

public interface ILogArchive : ILogSource
{
    Task ArchiveLogs(long runId, Pipeline pipeline, CancellationToken cancellationToken);
}
