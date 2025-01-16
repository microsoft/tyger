// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Database.Migrations;

public class Migrator6 : Migrator
{
    public override async Task Apply(Npgsql.NpgsqlDataSource dataSource, ILogger logger, CancellationToken cancellationToken)
    {
        await using var batch = dataSource.CreateBatch();
        batch.BatchCommands.Add(new(
            $"""
            CREATE TABLE IF NOT EXISTS run_tags (
            id bigint NOT NULL,
            created_at timestamp with time zone NOT NULL,
            key bigint NOT NULL,
            value text NOT NULL
            )
            """));

        batch.BatchCommands.Add(new(
            $"""
            CREATE INDEX IF NOT EXISTS idx_run_tags_created_at_id_key_value
            ON run_tags (created_at, id, key, value)
            """));

        batch.BatchCommands.Add(new(
            $"""
            CREATE INDEX IF NOT EXISTS idx_run_tags_key_value_created_at_id
            ON run_tags (key, value, created_at, id)
            """));

        batch.BatchCommands.Add(new(
            $"""
            ALTER TABLE runs
            ADD COLUMN IF NOT EXISTS tags_version int NOT NULL DEFAULT 1
            """));

        batch.BatchCommands.Add(new(
            $"""
            ALTER TABLE buffers DROP COLUMN IF EXISTS etag
            """));

        await batch.ExecuteNonQueryAsync(cancellationToken);
    }
}
