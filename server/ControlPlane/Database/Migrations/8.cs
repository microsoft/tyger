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
            CREATE TABLE IF NOT EXISTS run_buffer_secret_updates (
            run_id bigint NOT NULL PRIMARY KEY,
            updated_at timestamp with time zone NOT NULL,
            expires_at timestamp with time zone NOT NULL
            )
            """));
        await batch.ExecuteNonQueryAsync(cancellationToken);
    }
}
