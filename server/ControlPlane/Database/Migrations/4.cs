// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Database.Migrations;

public class Migrator4 : Migrator
{
    public override async Task Apply(Npgsql.NpgsqlDataSource dataSource, ILogger logger, CancellationToken cancellationToken)
    {
        using (var cmd = dataSource.CreateCommand(
            """
            CREATE INDEX IF NOT EXISTS idx_runs_created_at_id_status
            ON runs (created_at, id)
            INCLUDE (status)
            """))
        {
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }

        using (var cmd = dataSource.CreateCommand(
            """
            DROP INDEX IF EXISTS idx_runs_created_at
            """))
        {
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }
    }
}
