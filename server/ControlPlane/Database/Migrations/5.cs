// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Microsoft.Extensions.Options;
using NpgsqlTypes;
using Tyger.ControlPlane.Buffers;

namespace Tyger.ControlPlane.Database.Migrations;

public class Migrator5 : Migrator
{
    private readonly CloudBufferStorageOptions _cloudBufferStorageOptions;
    private readonly LocalBufferStorageOptions _localBufferStorageOptions;

    public Migrator5(IOptions<CloudBufferStorageOptions> cloudBufferStorageOptions, IOptions<LocalBufferStorageOptions> localBufferStorageOptions)
    {
        _cloudBufferStorageOptions = cloudBufferStorageOptions.Value;
        _localBufferStorageOptions = localBufferStorageOptions.Value;
    }

    public override async Task Apply(Npgsql.NpgsqlDataSource dataSource, ILogger logger, CancellationToken cancellationToken)
    {
        await using (var batch = dataSource.CreateBatch())
        {
            batch.BatchCommands.Add(new(
                $"""
                CREATE TABLE IF NOT EXISTS storage_accounts (
                    id int NOT NULL PRIMARY KEY GENERATED ALWAYS AS IDENTITY (MINVALUE 0 START WITH 0 INCREMENT BY 1),
                    name text NOT NULL,
                    location text NOT NULL,
                    endpoint text NOT NULL
                )
                """));

            batch.BatchCommands.Add(new(
                $"""
                CREATE UNIQUE INDEX IF NOT EXISTS idx_storage_accounts_name
                ON storage_accounts (name)
                """));

            await batch.ExecuteNonQueryAsync(cancellationToken);

        }

        bool hasStorageAccounts;
        using (var anyStorageAccountsCmd = dataSource.CreateCommand(
            """
            SELECT 1 FROM storage_accounts LIMIT 1
            """))
        {
            hasStorageAccounts = await anyStorageAccountsCmd.ExecuteScalarAsync(cancellationToken) != null;
        }

        var defaultStorageAccountId = 0;
        if (!hasStorageAccounts)
        {
            bool hasBuffers;
            using (var anyBuffersCmd = dataSource.CreateCommand(
                """
                SELECT 1 FROM buffers LIMIT 1
                """))
            {
                hasBuffers = await anyBuffersCmd.ExecuteScalarAsync(cancellationToken) != null;
            }

            if (hasBuffers)
            {
                string name;
                string location;
                string endpoint;

                // Note that both options will be non-null.
                // We tell if we are using cloud storage by checking if the cloud storage options has any storage accounts.
                // If using cloud, the count has been validated to be greater than 0.

                if (_cloudBufferStorageOptions.StorageAccounts.Count > 0)
                {
                    name = _cloudBufferStorageOptions.StorageAccounts[0].Name;
                    location = _cloudBufferStorageOptions.StorageAccounts[0].Location;
                    endpoint = _cloudBufferStorageOptions.StorageAccounts[0].Endpoint;
                }
                else
                {
                    name = LocalStorageBufferProvider.AccountName;
                    location = LocalStorageBufferProvider.AccountLocation;
                    endpoint = _localBufferStorageOptions.DataPlaneEndpoint.ToString();
                }

                await using var insertStorageAccountCmd = dataSource.CreateCommand(
                    """
                    INSERT INTO storage_accounts
                    (name, location, endpoint)
                    VALUES ($1, $2, $3)
                    RETURNING id
                    """);
                insertStorageAccountCmd.Parameters.Add(new() { Value = name.ToLowerInvariant(), NpgsqlDbType = NpgsqlDbType.Text });
                insertStorageAccountCmd.Parameters.Add(new() { Value = location.ToLowerInvariant(), NpgsqlDbType = NpgsqlDbType.Text });
                insertStorageAccountCmd.Parameters.Add(new() { Value = endpoint, NpgsqlDbType = NpgsqlDbType.Text });

                defaultStorageAccountId = (int)(await insertStorageAccountCmd.ExecuteScalarAsync(cancellationToken))!;
            }
        }

        await using var addStorageAccountIdColumnCmd = dataSource.CreateCommand(
            $"""
            ALTER TABLE buffers
            ADD COLUMN IF NOT EXISTS storage_account_id int REFERENCES storage_accounts(id) DEFAULT {defaultStorageAccountId}
            """);

        await addStorageAccountIdColumnCmd.ExecuteNonQueryAsync(cancellationToken);
    }
}
