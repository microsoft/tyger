using System.ComponentModel.DataAnnotations;
using System.Text;
using System.Text.Json;
using Microsoft.EntityFrameworkCore;
using Npgsql;
using SimpleBase;
using Tyger.Server.Model;

namespace Tyger.Server.Database;

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
        var codespecEntity = await _context.Codespecs.AsNoTracking()
             .Where(c => c.Name == name && c.Version == version)
             .FirstOrDefaultAsync(cancellationToken);

        return codespecEntity == null ? null : new Codespec(codespecEntity.Spec, name, version);
    }

    public async Task<Codespec?> GetLatestCodespec(string name, CancellationToken cancellationToken)
    {
        var codespecEntity = await _context.Codespecs.AsNoTracking()
             .Where(c => c.Name == name)
             .OrderByDescending(c => c.Version)
             .FirstOrDefaultAsync(cancellationToken);

        return codespecEntity == null ? null : new Codespec(codespecEntity.Spec, name, codespecEntity.Version);
    }

    public async Task<int> UpsertCodespec(string name, NewCodespec codespec, CancellationToken cancellationToken)
    {
        var connection = await GetOpenedConnection(cancellationToken);
        using var command = new NpgsqlCommand
        {
            Connection = connection,
            CommandText = @"
                INSERT INTO codespecs
                SELECT
                    $1,
                    CASE WHEN MAX(version) IS NULL THEN 1 ELSE MAX(version) + 1 END,
                    now() AT TIME ZONE 'utc',
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

    public async Task<Run> CreateRun(NewRun newRun, CancellationToken cancellationToken)
    {
        var connection = await GetOpenedConnection(cancellationToken);
        await using var tx = await connection.BeginTransactionAsync(cancellationToken);
        using var insertCommand = new NpgsqlCommand
        {
            Connection = connection,
            Transaction = tx,
            CommandText = @"
                INSERT INTO runs (created_at, run)
                VALUES (now() AT TIME ZONE 'utc', $1)
                RETURNING id, created_at",
            Parameters =
            {
                new() { Value = JsonSerializer.Serialize(newRun), NpgsqlDbType = NpgsqlTypes.NpgsqlDbType.Jsonb },
            }
        };

        using var reader = await insertCommand.ExecuteReaderAsync(cancellationToken);
        await reader.ReadAsync(cancellationToken);
        var run = new Run(newRun) with { Id = reader.GetInt64(0), CreatedAt = reader.GetDateTime(1) };

        await reader.ReadAsync(cancellationToken);
        await reader.DisposeAsync();

        using var updateCommand = new NpgsqlCommand
        {
            Connection = connection,
            Transaction = tx,
            CommandText = $@"
                UPDATE runs
                SET run = $1
                WHERE id = $2",
            Parameters =
            {
                new() { Value = JsonSerializer.Serialize(run), NpgsqlDbType = NpgsqlTypes.NpgsqlDbType.Jsonb },
                new() { Value = run.Id },
            },
        };

        await updateCommand.ExecuteNonQueryAsync(cancellationToken);
        await tx.CommitAsync(cancellationToken);
        return run;
    }

    public async Task UpdateRun(Run run, bool? resourcesCreated = null, bool? final = null, DateTimeOffset? logsArchivedAt = null, CancellationToken cancellationToken = default)
    {
        var connection = await GetOpenedConnection(cancellationToken);
        using var command = new NpgsqlCommand
        {
            Connection = connection,
            CommandText = $@"
                UPDATE runs
                SET run = $2 {(resourcesCreated.HasValue ? ", resources_created = $3" : null)} {(final.HasValue ? ", final = $4" : null)} {(logsArchivedAt.HasValue ? ", logs_archived_at = $5" : null)}
                WHERE id = $1",
            Parameters =
            {
                new() { Value = run.Id },
                new() { Value = JsonSerializer.Serialize(run), NpgsqlDbType = NpgsqlTypes.NpgsqlDbType.Jsonb },
                new() { Value = resourcesCreated.GetValueOrDefault() },
                new() { Value = final.GetValueOrDefault() },
                new() { Value = logsArchivedAt.GetValueOrDefault() },
            },
        };

        await command.ExecuteNonQueryAsync(cancellationToken);
    }

    public async Task DeleteRun(long id, CancellationToken cancellationToken)
    {
        var connection = await GetOpenedConnection(cancellationToken);
        using var command = new NpgsqlCommand
        {
            Connection = connection,
            CommandText = @"
                DELETE FROM runs
                WHERE id = $1",
            Parameters = { new() { Value = id } },
        };

        await command.ExecuteNonQueryAsync(cancellationToken);
    }

    public async Task<(Run run, bool final, DateTimeOffset? logsArchivedAt)?> GetRun(long id, CancellationToken cancellationToken)
    {
        var entity = await _context.Runs.AsNoTracking().FirstOrDefaultAsync(r => r.Id == id, cancellationToken);
        return entity == null ? null : (entity.Run, entity.Final, entity.LogsArchivedAt);
    }

    public async Task<(IList<(Run run, bool final)>, string? nextContinuationToken)> GetRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        IQueryable<RunEntity> runsQueryable = _context.Runs.Where(r => r.ResourcesCreated);
        if (continuationToken != null)
        {
            bool valid = false;
            try
            {
                var fields = JsonSerializer.Deserialize<long[]>(Encoding.ASCII.GetString(Base32.ZBase32.Decode(continuationToken)));
                if (fields is { Length: 2 })
                {
                    var createdAt = new DateTimeOffset(fields[0], TimeSpan.Zero);
                    var id = fields[1];
                    runsQueryable = runsQueryable.Where(r => r.CreatedAt < createdAt || (r.CreatedAt == createdAt && r.Id < id));
                    valid = true;
                }
            }
            catch (Exception e) when (e is JsonException or FormatException)
            {
            }

            if (!valid)
            {
                throw new ValidationException("Invalid continuation token.");
            }
        }

        if (since.HasValue)
        {
            runsQueryable = runsQueryable.Where(r => r.CreatedAt > since.Value);
        }

        var results = (await runsQueryable
                .OrderByDescending(e => e.CreatedAt).ThenByDescending(e => e.Id)
                .Take(limit + 1)
                .ToListAsync(cancellationToken))
            .Select(e => (e.Run, e.Final))
            .ToList();

        if (results.Count == limit + 1)
        {
            results.RemoveAt(limit);
            var last = results[^1].Run;
            string newToken = Base32.ZBase32.Encode(Encoding.ASCII.GetBytes(JsonSerializer.Serialize(new[] { last.CreatedAt.UtcTicks, last.Id })));
            return (results, newToken);
        }

        return (results, null);
    }

    public async Task<IList<Run>> GetPageOfRunsThatNeverGotResources(CancellationToken cancellationToken)
    {
        var oldestAllowable = DateTimeOffset.UtcNow.AddMinutes(-5);
        return (await _context.Runs.AsNoTracking().Where(r => r.CreatedAt < oldestAllowable && !r.ResourcesCreated).OrderByDescending(r => r.CreatedAt).Take(100).ToListAsync(cancellationToken))
            .Select(r => r.Run).ToList();
    }

    private async ValueTask<NpgsqlConnection> GetOpenedConnection(CancellationToken cancellationToken)
    {
        var connection = (NpgsqlConnection)_context.Database.GetDbConnection();
        if (connection.State != System.Data.ConnectionState.Open)
        {
            await connection.OpenAsync(cancellationToken);
        }

        return connection;
    }
}
