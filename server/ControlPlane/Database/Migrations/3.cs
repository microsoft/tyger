// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Database.Migrations;

public class Migrator3 : Migrator
{
    public override async Task Apply(Npgsql.NpgsqlDataSource dataSource, ILogger logger, CancellationToken cancellationToken)
    {
        await using var batch = dataSource.CreateBatch();

        batch.BatchCommands.Add(new("""

            """));

        batch.BatchCommands.Add(new("""
            ALTER TABLE runs
            ADD COLUMN IF NOT EXISTS idempotency_key nvarchar(64) NULL
            ADD COLUMN IF NOT EXISTS modified_at timestamp with time zone NOT NULL;
            """));

        batch.BatchCommands.Add(new("""
            CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_idempotency_key
            ON runs (idempotency_key)
            WHERE idempotency_key IS NOT NULL;
            """));

        batch.BatchCommands.Add(new("""
            CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_runs_status_not_final
            ON runs ((run->>'status'), created_at)
            WHERE final = false;
            """));

        batch.BatchCommands.Add(new("""
            CREATE INDEX idx_runs_pending_resources_not_created
            ON runs (resources_created, created_at)
            WHERE resources_created = false AND final = false and (run->>'status') = 'pending';
            """));

        batch.BatchCommands.Add(new("""
            CREATE INDEX idx_runs_pending_resources_not_created
            ON runs (created_at)
            WHERE resources_created = true AND final = false and (run->>'status') = 'pending';
            """));

        await batch.ExecuteNonQueryAsync(cancellationToken);
    }
}
