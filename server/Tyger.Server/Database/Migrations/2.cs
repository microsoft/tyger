namespace Tyger.Server.Database.Migrations;

public class Migrator2 : Migrator
{
    public override async Task Apply(Npgsql.NpgsqlDataSource dataSource, ILogger logger, CancellationToken cancellationToken)
    {
        await using var batch = dataSource.CreateBatch();

        batch.BatchCommands.Add(new(
            WrapCreateIndexWithExistenceCheck(
                "idx_codespecs_name_version",
                "CREATE INDEX idx_codespecs_name_version ON codespecs (name, version DESC)")));

        await batch.ExecuteNonQueryAsync(cancellationToken);
    }
}
