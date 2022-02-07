using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Npgsql;
using Tyger.Server.Model;

namespace Tyger.Server.Database;

public interface IRepository
{
    Task<int> UpsertCodespec(string name, Codespec codespec, CancellationToken cancellationToken);
    Task<(Codespec codespec, int version)?> GetLatestCodespec(string name, CancellationToken cancellationToken);
    Task<Codespec?> GetCodespecAtVersion(string name, int version, CancellationToken cancellationToken);
}

public class Repository : IRepository
{
    private readonly TygerDbContext _context;
    private readonly ILogger<Repository> _logger;

    public Repository(TygerDbContext context, ILogger<Repository> logger)
    {
        _context = context;
        _logger = logger;
    }

    public async Task<Codespec?> GetCodespecAtVersion(string name, int version, CancellationToken cancellationToken)
    {
        var codespecEntity = await _context.Codespecs
             .Where(c => c.Name == name && c.Version == version)
             .FirstOrDefaultAsync(cancellationToken);

        return codespecEntity?.Spec;
    }

    public async Task<(Codespec codespec, int version)?> GetLatestCodespec(string name, CancellationToken cancellationToken)
    {
        var codespecEntity = await _context.Codespecs
             .Where(c => c.Name == name)
             .OrderByDescending(c => c.Version)
             .FirstOrDefaultAsync(cancellationToken);

        return codespecEntity == null ? null : (codespecEntity.Spec, codespecEntity.Version);
    }

    public async Task<int> UpsertCodespec(string name, Codespec codespec, CancellationToken cancellationToken)
    {
        var connection = (NpgsqlConnection)_context.Database.GetDbConnection();
        await connection.OpenAsync(cancellationToken);
        await using var command = new NpgsqlCommand
        {
            Connection = connection,
            CommandText = @"
                INSERT INTO codespecs
                SELECT
                    $1,
                    CASE WHEN MAX(version) IS NULL THEN 1 ELSE MAX(version) + 1 END,
                    now(),
                    $2
                FROM codespecs where name = $1
                RETURNING version",
            Parameters =
            {
                new() { Value = name },
                new() { Value = JsonSerializer.Serialize(codespec), NpgsqlDbType = NpgsqlTypes.NpgsqlDbType.Jsonb },
            },
        };

        for (int i = 0; ; i++)
        {
            try
            {
                _logger.UpsertingCodespec(name);
                return (int)(await command.ExecuteScalarAsync(cancellationToken))!;
            }
            catch (PostgresException e) when (e.SqlState == PostgresErrorCodes.UniqueViolation)
            {
                _logger.UpsertingCodespecConflict(name);
                if (i == 5)
                {
                    throw;
                }
            }
        }
    }
}
