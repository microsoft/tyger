// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Logging;

public interface ILogArchive : ILogSource
{
    Task ArchiveLogs(long runId, Pipeline pipeline, CancellationToken cancellationToken);
}
