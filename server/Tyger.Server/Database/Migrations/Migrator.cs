// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Npgsql;
using static Tyger.Server.Database.Constants;

namespace Tyger.Server.Database.Migrations;

/// <summary>
/// Base class for a migration from version N to N+1.
/// </summary>
public abstract class Migrator
{
    public abstract Task Apply(NpgsqlDataSource dataSource, ILogger logger, CancellationToken cancellationToken);

    protected static string WrapCreateIndexWithExistenceCheck(string indexName, string createIndexStatement)
    {
        return $"""
            DO $$
            BEGIN
                IF NOT EXISTS (
                    SELECT
                    FROM pg_class c
                    JOIN pg_namespace n ON n.oid = c.relnamespace
                    WHERE c.relname = '{indexName}'
                        AND n.nspname = '{DatabaseNamespace}'
                ) THEN
                    {Indent("        ", createIndexStatement)};
                END IF;
            END
            $$;
            """;
    }

    private static string Indent(string indent, string s)
    {
        return string.Join(Environment.NewLine, s.Split(Environment.NewLine).Select(l => indent + l));
    }
}
