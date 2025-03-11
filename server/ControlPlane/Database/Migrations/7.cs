// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Database.Migrations;

public class Migrator7 : Migrator
{
    public override async Task Apply(Npgsql.NpgsqlDataSource dataSource, ILogger logger, CancellationToken cancellationToken)
    {
        await using var batch = dataSource.CreateBatch();
        batch.BatchCommands.Add(new(
            $"""
            ALTER TABLE buffers
            ADD COLUMN IF NOT EXISTS expires_at timestamp with time zone DEFAULT null,
            ADD COLUMN IF NOT EXISTS is_soft_deleted boolean NOT NULL DEFAULT false
            """));

        batch.BatchCommands.Add(new(
            $"""
            CREATE INDEX IF NOT EXISTS idx_buffers_expires_at
            ON buffers (expires_at)
            WHERE expires_at IS NOT NULL
            """));

        await batch.ExecuteNonQueryAsync(cancellationToken);
    }
}
