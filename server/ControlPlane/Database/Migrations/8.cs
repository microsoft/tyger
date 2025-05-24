// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Database.Migrations;

public class Migrator8 : Migrator
{
    public override async Task Apply(Npgsql.NpgsqlDataSource dataSource, ILogger logger, CancellationToken cancellationToken)
    {
        await using var batch = dataSource.CreateBatch();
        batch.BatchCommands.Add(new(
            $"""
            ALTER TABLE runs
            ADD COLUMN IF NOT EXISTS secret_refresh_at timestamp with time zone DEFAULT null
            """));
        await batch.ExecuteNonQueryAsync(cancellationToken);
    }
}
