// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Database.Migrations;

namespace Tyger.ControlPlane.Compute.Docker;

public class DockerReplicaDatabaseVersionProvider : IReplicaDatabaseVersionProvider
{
    public IAsyncEnumerable<(Uri, DatabaseVersion)> GetDatabaseVersionsOfReplicas(CancellationToken cancellationToken)
    {
        return AsyncEnumerable.Empty<(Uri, DatabaseVersion)>();
    }
}
