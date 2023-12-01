namespace Tyger.Server.Database.Migrations;

[Migration(Id, "Initial")]
public class Migration1 : Migration
{
    public const int Id = 1;

    public override async Task Apply(Npgsql.NpgsqlDataSource dataSource, ILogger logger, CancellationToken cancellationToken)
    {
        await using var batch = dataSource.CreateBatch();

        // migrations table

        batch.BatchCommands.Add(new($"""
            CREATE TABLE IF NOT EXISTS migrations (
                timestamp timestamp with time zone NOT NULL DEFAULT (now() AT TIME ZONE 'UTC'),
                version int NOT NULL,
                state varchar(64) NOT NULL CHECK (state IN ('{MigrationRunner.MigrationStateStarted}', '{MigrationRunner.MigrationStateComplete}', '{MigrationRunner.MigrationStateFailed}'))
            )
            """));

        batch.BatchCommands.Add(new(
            WrapCreateIndexWithExistenceCheck(
                "idx_migrations_version_complete",
                $"""
                CREATE INDEX idx_migrations_version_complete ON migrations (version)
                WHERE state = '{MigrationRunner.MigrationStateComplete}'
                """)));

        // codespecs table

        batch.BatchCommands.Add(new("""
            CREATE TABLE IF NOT EXISTS codespecs (
                name text NOT NULL COLLATE "C",
                version integer NOT NULL,
                created_at timestamp with time zone NOT NULL DEFAULT (now() AT TIME ZONE 'UTC'),
                spec jsonb NOT NULL
            )
            """));

        // runs table

        batch.BatchCommands.Add(new("""
            CREATE TABLE IF NOT EXISTS runs (
                id bigint NOT NULL PRIMARY KEY GENERATED BY DEFAULT AS IDENTITY,
                created_at timestamp with time zone NOT NULL,
                run jsonb NOT NULL,
                final boolean NOT NULL DEFAULT false,
                resources_created boolean NOT NULL DEFAULT false,
                logs_archived_at timestamp with time zone
            )
            """));

        batch.BatchCommands.Add(new(
            WrapCreateIndexWithExistenceCheck(
                "idx_runs_created_at_id",
                "CREATE INDEX idx_runs_created_at_id ON runs (created_at, id)")));

        batch.BatchCommands.Add(new(
            WrapCreateIndexWithExistenceCheck(
                "idx_runs_created_at_resources_not_created",
                "CREATE INDEX idx_runs_created_at_resources_not_created ON runs (created_at) WHERE resources_created = false")));

        // buffers table

        batch.BatchCommands.Add(new("""
            CREATE TABLE IF NOT EXISTS buffers (
                id text PRIMARY KEY,
                created_at timestamp with time zone NOT NULL,
                etag text NOT NULL
            )
            """));

        batch.BatchCommands.Add(new(
            WrapCreateIndexWithExistenceCheck(
                "idx_buffers_id_created_at",
                "CREATE INDEX idx_buffers_id_created_at ON buffers (id, created_at)")));

        batch.BatchCommands.Add(new(
            WrapCreateIndexWithExistenceCheck(
                "idx_buffers_created_at_id",
                "CREATE INDEX idx_buffers_created_at_id ON buffers (created_at, id)")));

        // tags table

        batch.BatchCommands.Add(new("""
            CREATE TABLE IF NOT EXISTS tags (
                id text,
                created_at timestamp with time zone NOT NULL,
                key bigint NOT NULL,
                value text NOT NULL
            )
            """));

        batch.BatchCommands.Add(new(
            WrapCreateIndexWithExistenceCheck(
                "idx_tags_created_at_id_key_value",
                "CREATE INDEX idx_tags_created_at_id_key_value ON tags (created_at, id, key, value)")));

        batch.BatchCommands.Add(new(
            WrapCreateIndexWithExistenceCheck(
                "idx_tags_key_value_created_at_id",
                "CREATE INDEX idx_tags_key_value_created_at_id ON tags (key, value, created_at, id)")));

        // tag_keys table
        batch.BatchCommands.Add(new("""
        CREATE TABLE IF NOT EXISTS tag_keys (
            id bigint NOT NULL PRIMARY KEY GENERATED BY DEFAULT AS IDENTITY,
            name text NOT NULL
        )
        """));

        batch.BatchCommands.Add(new(
            WrapCreateIndexWithExistenceCheck(
                "idx_tag_keys_name",
                "CREATE UNIQUE INDEX idx_tag_keys_name ON tag_keys (name)")));

        await batch.ExecuteNonQueryAsync(cancellationToken);
    }
}
