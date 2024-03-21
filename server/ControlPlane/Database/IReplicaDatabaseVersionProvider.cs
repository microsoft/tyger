// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Database.Migrations;

namespace Tyger.ControlPlane.Database;

public interface IReplicaDatabaseVersionProvider
{
    IAsyncEnumerable<(Uri, DatabaseVersion)> GetDatabaseVersionsOfReplicas(CancellationToken cancellationToken);
}
