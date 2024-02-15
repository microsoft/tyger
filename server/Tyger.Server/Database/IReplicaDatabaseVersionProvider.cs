using Tyger.Server.Database.Migrations;

namespace Tyger.Server.Database;

public interface IReplicaDatabaseVersionProvider
{
    IAsyncEnumerable<(Uri, DatabaseVersion)> GetDatabaseVersionsOfReplicas(CancellationToken cancellationToken);
}
