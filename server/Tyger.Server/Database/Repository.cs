using System.ComponentModel.DataAnnotations;
using System.Data;
using System.Text;
using System.Text.Json;
using Npgsql;
using SimpleBase;
using Tyger.Server.Model;
using Buffer = Tyger.Server.Model.Buffer;

namespace Tyger.Server.Database;

public class Repository : IRepository
{
    private readonly NpgsqlDataSource _dataSource;
    private readonly JsonSerializerOptions _serializerOptions;
    private readonly ILogger<Repository> _logger;

    public Repository(NpgsqlDataSource dataSource, JsonSerializerOptions serializerOptions, ILogger<Repository> logger)
    {
        _dataSource = dataSource;
        _serializerOptions = serializerOptions;
        _logger = logger;
    }

    public async Task<Codespec?> GetCodespecAtVersion(string name, int version, CancellationToken cancellationToken)
    {
        await using var cmd = _dataSource.CreateCommand($"""
            SELECT spec, created_at
            FROM codespecs
            WHERE name = $1 AND version = $2
            """);

        cmd.Parameters.AddWithValue(name);
        cmd.Parameters.AddWithValue(version);

        await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
        if (!await reader.ReadAsync(cancellationToken))
        {
            return null;
        }

        var specJson = reader.GetString(0);
        var createdAt = reader.GetDateTime(1);

        return JsonSerializer.Deserialize<Codespec>(specJson, _serializerOptions)
            !.WithSystemProperties(name, version, createdAt);
    }

    public async Task<Codespec?> GetLatestCodespec(string name, CancellationToken cancellationToken)
    {
        await using var cmd = _dataSource.CreateCommand($"""
            SELECT spec, version, created_at
            FROM codespecs
            WHERE name = $1
            ORDER BY version DESC
            LIMIT 1
            """);

        cmd.Parameters.AddWithValue(name);

        await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
        if (!await reader.ReadAsync(cancellationToken))
        {
            return null;
        }

        var specJson = reader.GetString(0);
        var version = reader.GetInt32(1);
        var createdAt = reader.GetDateTime(2);

        return JsonSerializer.Deserialize<Codespec>(specJson, _serializerOptions)
            !.WithSystemProperties(name, version, createdAt);
    }

    public async Task<(IList<Codespec>, string? nextContinuationToken)> GetCodespecs(int limit, string? prefix, string? continuationToken, CancellationToken cancellationToken)
    {
        var pagingName = "";
        if (continuationToken != null)
        {
            bool valid = false;
            try
            {
                var fields = JsonSerializer.Deserialize<string[]>(Encoding.ASCII.GetString(Base32.ZBase32.Decode(continuationToken)), _serializerOptions);
                if (fields is { Length: 1 })
                {
                    pagingName = fields[0];
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

        await using var cmd = _dataSource.CreateCommand($"""
            SELECT DISTINCT ON (name) name, version, created_at, spec
            FROM codespecs
            WHERE name > $3 AND name LIKE $2
            ORDER BY name, version DESC
            LIMIT $1
            """);

        cmd.Parameters.AddWithValue(limit + 1);
        cmd.Parameters.AddWithValue(prefix + "%");
        cmd.Parameters.AddWithValue(pagingName);

        var results = new List<Codespec>();
        await using var reader = (await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken))!;
        while (await reader.ReadAsync(cancellationToken))
        {
            var name = reader.GetString(0);
            var version = reader.GetInt32(1);
            var createdAt = reader.GetDateTime(2);
            Codespec spec = JsonSerializer.Deserialize<Codespec>(reader.GetString(3), _serializerOptions)!;
            results.Add(spec.WithSystemProperties(name, version, createdAt));
        }

        if (results.Count == limit + 1)
        {
            results.RemoveAt(limit);
            var last = results[^1];
            string newToken = Base32.ZBase32.Encode(Encoding.ASCII.GetBytes(JsonSerializer.Serialize(new[] { last.Name }, _serializerOptions)));
            return (results, newToken);
        }

        return (results, null);
    }

    public async Task<Codespec> UpsertCodespec(string name, Codespec newcodespec, CancellationToken cancellationToken)
    {
        newcodespec = newcodespec.WithoutSystemProperties();

        Codespec? latestCodespec = await GetLatestCodespec(name, cancellationToken);
        if (latestCodespec != null && newcodespec.Equals(latestCodespec.WithoutSystemProperties()))
        {
            return latestCodespec;
        }

        await using var cmd = _dataSource.CreateCommand("""
            INSERT INTO codespecs
            SELECT
                $1,
                CASE WHEN MAX(version) IS NULL THEN 1 ELSE MAX(version) + 1 END,
                now() AT TIME ZONE 'utc',
                $2
            FROM codespecs
            WHERE name = $1
            RETURNING version, created_at
            """);

        cmd.Parameters.AddWithValue(name);
        cmd.Parameters.Add(new() { Value = JsonSerializer.Serialize(newcodespec, _serializerOptions), NpgsqlDbType = NpgsqlTypes.NpgsqlDbType.Jsonb });

        for (int i = 0; ; i++)
        {
            try
            {
                _logger.UpsertingCodespec(name);
                using var reader = (await cmd.ExecuteReaderAsync(cancellationToken))!;
                await reader.ReadAsync(cancellationToken);
                var version = reader.GetInt32(0);
                var createdAt = reader.GetDateTime(1);
                await reader.ReadAsync(cancellationToken);
                await reader.DisposeAsync();
                return newcodespec.WithSystemProperties(name, version, createdAt);
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

    public async Task<Run> CreateRun(Run newRun, CancellationToken cancellationToken)
    {
        newRun = newRun.WithoutSystemProperties();

        await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
        await using var tx = await connection.BeginTransactionAsync(cancellationToken);
        using var insertCommand = new NpgsqlCommand
        {
            Connection = connection,
            Transaction = tx,
            CommandText = """
                INSERT INTO runs (created_at, run)
                VALUES (now() AT TIME ZONE 'utc', $1)
                RETURNING id, created_at
                """,
            Parameters =
            {
                new() { Value = JsonSerializer.Serialize(newRun, _serializerOptions), NpgsqlDbType = NpgsqlTypes.NpgsqlDbType.Jsonb },
            }
        };

        Run run;
        await using (var reader = await insertCommand.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken))
        {
            await reader.ReadAsync(cancellationToken);
            run = newRun with { Id = reader.GetInt64(0), CreatedAt = reader.GetDateTime(1), Status = RunStatus.Pending };
        }

        using var updateCommand = new NpgsqlCommand
        {
            Connection = connection,
            Transaction = tx,
            CommandText = """
                UPDATE runs
                SET run = $1
                WHERE id = $2
                """,
            Parameters =
            {
                new() { Value = JsonSerializer.Serialize(run, _serializerOptions), NpgsqlDbType = NpgsqlTypes.NpgsqlDbType.Jsonb },
                new() { Value = run.Id },
            },
        };

        await updateCommand.ExecuteNonQueryAsync(cancellationToken);
        await tx.CommitAsync(cancellationToken);
        return run;
    }

    public async Task UpdateRun(Run run, bool? resourcesCreated = null, bool? final = null, DateTimeOffset? logsArchivedAt = null, CancellationToken cancellationToken = default)
    {
        await using var command = _dataSource.CreateCommand($@"
            UPDATE runs
            SET run = $2 {(resourcesCreated.HasValue ? ", resources_created = $3" : null)} {(final.HasValue ? ", final = $4" : null)} {(logsArchivedAt.HasValue ? ", logs_archived_at = $5" : null)}
            WHERE id = $1");

        command.Parameters.AddWithValue(run.Id!);
        command.Parameters.Add(new() { Value = JsonSerializer.Serialize(run, _serializerOptions), NpgsqlDbType = NpgsqlTypes.NpgsqlDbType.Jsonb });
        command.Parameters.AddWithValue(resourcesCreated.GetValueOrDefault());
        command.Parameters.AddWithValue(final.GetValueOrDefault());
        command.Parameters.AddWithValue(logsArchivedAt.GetValueOrDefault());

        await command.ExecuteNonQueryAsync(cancellationToken);
    }

    public async Task DeleteRun(long id, CancellationToken cancellationToken)
    {
        await using var command = _dataSource.CreateCommand($@"
            DELETE FROM runs
            WHERE id = $1");

        command.Parameters.AddWithValue(id);

        await command.ExecuteNonQueryAsync(cancellationToken);
    }

    public async Task<(Run run, bool final, DateTimeOffset? logsArchivedAt)?> GetRun(long id, CancellationToken cancellationToken)
    {
        await using var cmd = _dataSource.CreateCommand($@"
            SELECT created_at, run, final, logs_archived_at
            FROM runs
            WHERE id = $1");

        cmd.Parameters.AddWithValue(id);

        await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
        if (!await reader.ReadAsync(cancellationToken))
        {
            return null;
        }

        var createdAt = reader.GetDateTime(0);
        var runJson = reader.GetString(1);
        var final = reader.GetBoolean(2);
        var logsArchivedAt = reader.IsDBNull(3) ? (DateTime?)null : reader.GetDateTime(3);

        return (JsonSerializer.Deserialize<Run>(runJson, _serializerOptions)!, final, logsArchivedAt);
    }

    public async Task<(IList<(Run run, bool final)>, string? nextContinuationToken)> GetRuns(int limit, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        var sb = new StringBuilder();
        sb.Append("""
            SELECT run, final
            FROM runs
            WHERE resources_created = true

            """);

        var parameters = new List<NpgsqlParameter>();
        int paramNumber = 0;

        if (continuationToken != null)
        {
            bool valid = false;
            try
            {
                var fields = JsonSerializer.Deserialize<long[]>(Encoding.ASCII.GetString(Base32.ZBase32.Decode(continuationToken)), _serializerOptions);
                if (fields is { Length: 2 })
                {
                    var createdAt = new DateTimeOffset(fields[0], TimeSpan.Zero);
                    var id = fields[1];
                    sb.AppendLine($"AND (created_at, id) < (${++paramNumber}, ${++paramNumber})");
                    parameters.Add(new() { Value = createdAt });
                    parameters.Add(new() { Value = id });
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
            sb.AppendLine($"AND created_at > ${++paramNumber}");
            parameters.Add(new() { Value = since.Value });
        }

        sb.AppendLine("ORDER BY created_at DESC, id DESC");
        sb.AppendLine($"LIMIT ${++paramNumber}");
        parameters.Add(new() { Value = limit + 1 });

        await using var cmd = _dataSource.CreateCommand(sb.ToString());
        foreach (var parameter in parameters)
        {
            cmd.Parameters.Add(parameter);
        }

        List<(Run Run, bool Final)> results = [];
        await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
        while (await reader.ReadAsync(cancellationToken))
        {
            var runJson = reader.GetString(0);
            var final = reader.GetBoolean(1);

            results.Add((JsonSerializer.Deserialize<Run>(runJson, _serializerOptions)!, final));
        }

        if (results.Count == limit + 1)
        {
            results.RemoveAt(limit);
            var last = results[^1].Run;
            string newToken = Base32.ZBase32.Encode(Encoding.ASCII.GetBytes(JsonSerializer.Serialize(new[] { last.CreatedAt!.Value.UtcTicks, last.Id }, _serializerOptions)));
            return (results, newToken);
        }

        return (results, null);
    }

    public async Task<IList<Run>> GetPageOfRunsThatNeverGotResources(CancellationToken cancellationToken)
    {
        var oldestAllowable = DateTimeOffset.UtcNow.AddMinutes(-5);

        await using var cmd = _dataSource.CreateCommand("""
            SELECT run
            FROM runs
            WHERE created_at < $1 AND NOT resources_created
            """);

        cmd.Parameters.AddWithValue(oldestAllowable);

        await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
        var results = new List<Run>();
        while (await reader.ReadAsync(cancellationToken))
        {
            var runJson = reader.GetString(0);
            results.Add(JsonSerializer.Deserialize<Run>(runJson, _serializerOptions)!);
        }

        return results;
    }

    public async Task<Buffer?> GetBuffer(string id, string eTag, CancellationToken cancellationToken)
    {
        await using var command = _dataSource.CreateCommand("""
            SELECT buffers.created_at, buffers.etag, tag_keys.name, tags.value
            FROM buffers
            LEFT JOIN tags
                on buffers.id = tags.id
                and tags.created_at = buffers.created_at
            LEFT JOIN tag_keys
                on tag_keys.id = tags.key
            WHERE buffers.id = $1
            """);

        command.Parameters.AddWithValue(id);

        if (eTag != "")
        {
            command.CommandText += " and buffers.etag = $2";
            command.Parameters.Add(new() { Value = eTag });
        }

        var tags = new Dictionary<string, string>();
        string currentETag = "";
        DateTimeOffset createdAt = DateTimeOffset.MinValue;

        await using var reader = (await command.ExecuteReaderAsync(cancellationToken))!;
        while (await reader.ReadAsync(cancellationToken))
        {
            if (createdAt == DateTimeOffset.MinValue)
            {
                createdAt = reader.GetDateTime(0);
            }

            if (string.IsNullOrEmpty(currentETag))
            {
                currentETag = reader.GetString(1);
            }

            if (!reader.IsDBNull(2) && !reader.IsDBNull(3))
            {
                var name = reader.GetString(2);
                var value = reader.GetString(3);
                tags.Add(name, value);
            }
        }

        if (currentETag == "" && createdAt == DateTimeOffset.MinValue)
        {
            return null;
        }

        return new Buffer { Id = id, ETag = currentETag, CreatedAt = createdAt, Tags = tags };
    }

    private async Task<long?> GetTagId(string name, CancellationToken cancellationToken)
    {
        await using var cmd = _dataSource.CreateCommand("""
            SELECT id
            FROM tag_keys
            WHERE name = $1
            """);

        cmd.Parameters.AddWithValue(name);

        await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
        if (!await reader.ReadAsync(cancellationToken))
        {
            return null;
        }

        return reader.GetInt64(0);
    }

    public async Task<(IList<Buffer>, string? nextContinuationToken)> GetBuffers(IDictionary<string, string>? tags, int limit, string? continuationToken, CancellationToken cancellationToken)
    {
        await using var command = _dataSource.CreateCommand();
        command.Parameters.AddWithValue(limit + 1);

        var commandText = new StringBuilder();
        string table = tags?.Count > 0 ? "tags" : "buffers";
        commandText.Append(@$"WITH matches AS (
            SELECT t1.id, t1.created_at
            FROM {table} AS t1
            ");

        int param = 2;

        if (tags?.Count > 0)
        {
            for (int x = 0; x < tags.Count - 1; x++)
            {
                commandText.Append($"INNER JOIN tags AS t{x + 2} ON t1.created_at = t{x + 2}.created_at and t1.id = t{x + 2}.id\n");
            }

            commandText.Append("WHERE\n");

            int index = 1;
            foreach (var tag in tags)
            {
                if (index != 1)
                {
                    commandText.Append(" AND ");
                }

                var id = await GetTagId(tag.Key, cancellationToken);
                if (id == null)
                {
                    return (new List<Buffer>(), null);
                }

                commandText.Append($" t{index}.key = ${param} and t{index}.value = ${param + 1}\n");
                command.Parameters.Add(new() { Value = id.Value });
                command.Parameters.Add(new() { Value = tag.Value });
                index++;
                param += 2;
            }
        }

        if (continuationToken != null)
        {
            bool valid = false;
            try
            {
                var fields = JsonSerializer.Deserialize<string[]>(Encoding.ASCII.GetString(Base32.ZBase32.Decode(continuationToken)), _serializerOptions);
                if (fields is { Length: 2 })
                {
                    if (tags == null)
                    {
                        commandText.Append(" WHERE ");
                    }
                    else
                    {
                        commandText.Append(" AND ");
                    }

                    commandText.Append($"(t1.created_at, t1.id) < (${param}, ${param + 1})\n");
                    command.Parameters.Add(new() { Value = DateTimeOffset.Parse(fields[0]) });
                    command.Parameters.Add(new() { Value = fields[1] });
                    param += 2;
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

        commandText.Append(@" ORDER BY t1.created_at DESC, t1.id DESC
                LIMIT $1
            )
            SELECT matches.id, matches.created_at, tag_keys.name, tags.value, buffers.etag
            FROM matches
            LEFT JOIN tags
                ON matches.id = tags.id AND matches.created_at = tags.created_at
            LEFT JOIN tag_keys ON tags.key = tag_keys.id
            LEFT JOIN buffers ON matches.id = buffers.id AND matches.created_at = buffers.created_at
            ORDER BY matches.created_at DESC, matches.id DESC");

        command.CommandText = commandText.ToString();

        var results = new List<Buffer>();
        var currentTags = new Dictionary<string, string>();
        var currentBuffer = new Buffer();

        using var reader = (await command.ExecuteReaderAsync(cancellationToken))!;
        while (await reader.ReadAsync(cancellationToken))
        {
            var id = reader.GetString(0);
            var createdAt = reader.GetDateTime(1);
            var etag = reader.GetString(4);

            if (currentBuffer.Id != id)
            {
                if (currentBuffer.Id != "")
                {
                    results.Add(currentBuffer with { Tags = currentTags });
                }

                currentBuffer = currentBuffer with { Id = id, CreatedAt = createdAt, ETag = etag };
                currentTags = [];
            }

            if (!reader.IsDBNull(2) && !reader.IsDBNull(3))
            {
                var tagname = reader.GetString(2);
                var tagvalue = reader.GetString(3);

                currentTags[tagname] = tagvalue;
            }
        }

        if (currentBuffer.Id != "")
        {
            results.Add(currentBuffer with { Tags = currentTags });
        }

        if (results.Count == limit + 1)
        {
            results.RemoveAt(limit);
            var last = results[^1];
            string newToken = Base32.ZBase32.Encode(Encoding.ASCII.GetBytes(JsonSerializer.Serialize(new[] { last.CreatedAt.ToString(), last.Id }, _serializerOptions)));
            return (results, newToken);
        }

        return (results, null);
    }

    public async Task<Buffer?> UpdateBufferById(string id, string eTag, IDictionary<string, string>? tags, CancellationToken cancellationToken)
    {
        await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
        await using var tx = await connection.BeginTransactionAsync(IsolationLevel.Serializable, cancellationToken);
        string newETag = DateTime.UtcNow.Ticks.ToString();

        // Update and validate the buffer
        using var bufferCommand = new NpgsqlCommand
        {
            Connection = connection,
            Transaction = tx,
            CommandText = @"
                UPDATE buffers
                SET etag = $1
                WHERE id = $2",
            Parameters =
                {
                    new() { Value = newETag },
                    new() { Value = id },
                }
        };

        if (eTag != "")
        {
            bufferCommand.CommandText += " AND etag = $3";
            bufferCommand.Parameters.Add(new() { Value = eTag });
        }

        bufferCommand.CommandText += " RETURNING created_at";

        DateTimeOffset createdAt = DateTimeOffset.MinValue;

        await using (var reader = await bufferCommand.ExecuteReaderAsync(cancellationToken))
        {
            // If the query didn't do anything, return null
            if (!reader.HasRows)
            {
                return null;
            }

            await reader.ReadAsync(cancellationToken);

            createdAt = reader.GetDateTime(0);

            await reader.ReadAsync(cancellationToken);
        }

        // Delete old tags
        using var deleteCommand = new NpgsqlCommand
        {
            Connection = connection,
            Transaction = tx,
            CommandText = @"
                    DELETE FROM tags WHERE
                    id = $1 AND created_at = $2",
            Parameters =
                {
                    new() { Value = id },
                    new() { Value = createdAt },
                }
        };

        await deleteCommand.ExecuteNonQueryAsync(cancellationToken);

        if (tags != null)
        {
            // Add the new tags
            foreach (var tag in tags)
            {
                await InsertTag(tx, id, createdAt, tag, cancellationToken);
            }
        }

        await tx.CommitAsync(cancellationToken);
        return new Buffer() { Id = id, ETag = newETag, CreatedAt = createdAt, Tags = tags };
    }

    private static async Task InsertTag(NpgsqlTransaction tx, string id, DateTimeOffset createdAt, KeyValuePair<string, string> tag, CancellationToken cancellationToken)
    {
        using var insertTagCommand = new NpgsqlCommand
        {
            Connection = tx.Connection,
            Transaction = tx,
            CommandText = @"
                        WITH INS AS (INSERT INTO tag_keys (name) VALUES ($4) ON CONFLICT DO NOTHING RETURNING id)
                        INSERT INTO tags (id, created_at, key, value)
                        (SELECT $1, $2, id, $3 FROM INS UNION
                        SELECT $1, $2, tag_keys.id, $3 FROM tag_keys WHERE name = $4)",

            Parameters =
            {
                new() { Value = id },
                new() { Value = createdAt },
                new() { Value = tag.Value },
                new() { Value = tag.Key },
            }
        };

        if (await insertTagCommand.ExecuteNonQueryAsync(cancellationToken) != 1)
        {
            throw new InvalidOperationException("Failed to insert tag: incorrect number of rows inserted");
        }
    }

    public async Task<Buffer> CreateBuffer(Buffer newBuffer, CancellationToken cancellationToken)
    {
        await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
        await using var tx = await connection.BeginTransactionAsync(IsolationLevel.Serializable, cancellationToken);
        string eTag = DateTime.UtcNow.Ticks.ToString();

        // Create the buffer DB entry
        using var insertCommand = new NpgsqlCommand
        {
            Connection = connection,
            Transaction = tx,
            CommandText = @"
                    INSERT INTO buffers (id, created_at, etag)
                    VALUES ($1, now() AT TIME ZONE 'utc', $2)
                    RETURNING created_at",
            Parameters =
                {
                    new() { Value = newBuffer.Id },
                    new() { Value = eTag },
                }
        };

        var buffer = newBuffer with { ETag = eTag };

        await using (var reader = await insertCommand.ExecuteReaderAsync(cancellationToken))
        {
            await reader.ReadAsync(cancellationToken);

            buffer = buffer with { CreatedAt = reader.GetDateTime(0), ETag = eTag };

            await reader.ReadAsync(cancellationToken);
        }

        if (buffer.Tags != null)
        {
            foreach (var tag in buffer.Tags)
            {
                await InsertTag(tx, buffer.Id, buffer.CreatedAt, tag, cancellationToken);
            }
        }

        await tx.CommitAsync(cancellationToken);
        return buffer;
    }
}
