// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Database.Migrations;

#pragma warning disable CA1848 // Use the LoggerMessage delegates. Logging performance is not a concern for database migrations.
public class Migrator3 : Migrator
{
    public override async Task Apply(Npgsql.NpgsqlDataSource dataSource, ILogger logger, CancellationToken cancellationToken)
    {
        await using (var batch = dataSource.CreateBatch())
        {
            batch.BatchCommands.Add(new(
                """
                CREATE TABLE run_idempotency_keys (
                    idempotency_key text PRIMARY KEY NOT NULL,
                    run_id bigint NULL
                );
                """));

            batch.BatchCommands.Add(new(
                """
                ALTER TABLE runs
                ADD COLUMN IF NOT EXISTS etag bigint NOT NULL DEFAULT 0;
                """));

            await batch.ExecuteNonQueryAsync(cancellationToken);
        }

        using (var cmd = dataSource.CreateCommand(
            """
            DROP INDEX IF EXISTS idx_runs_created_at_id;
            """))
        {
            logger.LogInformation("Dropping idx_runs_created_at_id");
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }

        // await using var batch = dataSource.CreateBatch();

        // batch.BatchCommands.Add(new("""
        //     CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_runs_status_not_final
        //     ON runs ((run->>'status'), created_at)
        //     WHERE final = false;
        //     """));

        // batch.BatchCommands.Add(new("""
        //     CREATE INDEX idx_runs_pending_resources_not_created
        //     ON runs (resources_created, created_at)
        //     WHERE resources_created = false AND final = false and (run->>'status') = 'pending';
        //     """));

        // batch.BatchCommands.Add(new("""
        //     CREATE INDEX idx_runs_pending_resources_not_created
        //     ON runs (created_at)
        //     WHERE resources_created = true AND final = false and (run->>'status') = 'pending';
        //     """));

        // await batch.ExecuteNonQueryAsync(cancellationToken);
    }
}
