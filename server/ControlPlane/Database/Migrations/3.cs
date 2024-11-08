// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Database.Migrations;

#pragma warning disable CA1848 // Use the LoggerMessage delegates. Logging performance is not a concern for database migrations.

// Note that this migration must be run offline
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
                CREATE TABLE IF NOT EXISTS leases (
                    id text PRIMARY KEY,
                    holder text NOT NULL,
                    expiration timestamp with time zone NOT NULL
                )
                """
            ));

            batch.BatchCommands.Add(new(
                """
                ALTER TABLE IF EXISTS tags
                RENAME TO buffer_tags;
                """
            ));

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

        using (var cmd = dataSource.CreateCommand("""
            ALTER TABLE runs
            ADD IF NOT EXISTS modified_at timestamp with time zone NULL DEFAULT NULL;
            """))
        {
            logger.LogInformation("Adding modified_at column to runs");
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }

        using (var cmd = dataSource.CreateCommand("""
            CREATE INDEX IF NOT EXISTS idx_runs_modified_at_not_null
            ON runs (id)
            INCLUDE (modified_at)
            WHERE modified_at IS NOT NULL;
            """))
        {
            logger.LogInformation("Creating idx_runs_modified_at_not_null");
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }

        using (var cmd = dataSource.CreateCommand("""
            ALTER TABLE runs
            ALTER COLUMN id DROP IDENTITY IF EXISTS;
            """))
        {
            logger.LogInformation("Adding status column to runs");
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }

        long maxExistingRunId;
        using (var cmd = dataSource.CreateCommand("""
            SELECT MAX(id) FROM runs;
            """))
        {
            maxExistingRunId = await cmd.ExecuteScalarAsync(cancellationToken) switch
            {
                DBNull => 0,
                long l => l,
                _ => throw new InvalidOperationException("Unexpected result from MAX(id) query")
            };
        }

        using (var cmd = dataSource.CreateCommand($"""
            CREATE SEQUENCE IF NOT EXISTS runs_id_seq
            AS bigint
            START WITH {maxExistingRunId + 1};
            """))
        {
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
            CREATE INDEX IF NOT EXISTS idx_runs_not_final
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
            CREATE INDEX IF NOT EXISTS idx_runs_created_at
            ON runs (created_at)
            INCLUDE (status);
            """))
        {
            logger.LogInformation("Creating idx_runs_created_at");
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }

        using (var cmd = dataSource.CreateCommand(
            """
            CREATE INDEX IF NOT EXISTS idx_runs_status
            ON runs (status);
            """))
        {
            logger.LogInformation("Creating idx_runs_status");
            await cmd.ExecuteNonQueryAsync(cancellationToken);
        }
    }
}
