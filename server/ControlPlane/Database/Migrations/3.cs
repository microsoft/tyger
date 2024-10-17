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
                CREATE TABLE IF NOT EXISTS run_idempotency_keys (
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

        using (var cmd = dataSource.CreateCommand("""
            ALTER TABLE runs
            ADD IF NOT EXISTS status text GENERATED ALWAYS AS (run->>'status') STORED;
            """))
        {
            logger.LogInformation("Adding status column to runs");
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }

        using (var cmd = dataSource.CreateCommand(
            """
            DROP INDEX IF EXISTS idx_runs_created_at_id;
            """))
        {
            logger.LogInformation("Dropping idx_runs_created_at_id");
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }

        using (var cmd = dataSource.CreateCommand(
            """
            CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_runs_not_final
            ON runs (created_at)
            INCLUDE (status)
            WHERE final = false;
            """))
        {
            logger.LogInformation("Creating idx_runs_not_final");
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }

        using (var cmd = dataSource.CreateCommand(
            """
            CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_runs_created_at
            ON runs (created_at)
            INCLUDE (status);
            """))
        {
            logger.LogInformation("Creating idx_runs_created_at");
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }

        using (var cmd = dataSource.CreateCommand(
            """
            CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_runs_status
            ON runs (status);
            """))
        {
            logger.LogInformation("Creating idx_runs_status");
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }
    }
}
