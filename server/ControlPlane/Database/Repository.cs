// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Data;
using System.Text;
using System.Text.Json;
using Npgsql;
using NpgsqlTypes;
using Polly;
using SimpleBase;
using Tyger.ControlPlane.Model;
using Buffer = Tyger.ControlPlane.Model.Buffer;

namespace Tyger.ControlPlane.Database;

public class Repository
{
    private const int MaxActiveRuns = 2000;
    private const string NewRunChannelName = "new_run";
    private const string RunFinalizedChannelName = "run_finalized";
    private const string RunChangedChannelName = "run_changed";

    private readonly NpgsqlDataSource _dataSource;
    private readonly ResiliencePipeline _resiliencePipeline;
    private readonly JsonSerializerOptions _serializerOptions;
    private readonly ILogger<Repository> _logger;

    public Repository(NpgsqlDataSource dataSource, ResiliencePipeline resiliencePipeline, JsonSerializerOptions serializerOptions, ILogger<Repository> logger)
    {
        _dataSource = dataSource;
        _resiliencePipeline = resiliencePipeline;
        _serializerOptions = serializerOptions;
        _logger = logger;
    }

    public async Task<Codespec?> GetCodespecAtVersion(string name, int version, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var cmd = new NpgsqlCommand("""
                SELECT spec, created_at
                FROM codespecs
                WHERE name = $1 AND version = $2
                """, conn)
            {
                Parameters =
                {
                    new() { NpgsqlDbType = NpgsqlDbType.Text, Value = name },
                    new() {  NpgsqlDbType = NpgsqlDbType.Integer, Value = version },
                }
            };

            await cmd.PrepareAsync(cancellationToken);

            await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
            if (!await reader.ReadAsync(cancellationToken))
            {
                return null;
            }

            var specJson = reader.GetString(0);
            var createdAt = reader.GetDateTime(1);

            return JsonSerializer.Deserialize<Codespec>(specJson, _serializerOptions)
                !.WithSystemProperties(name, version, createdAt);
        }, cancellationToken);
    }

    public async Task<Codespec?> GetLatestCodespec(string name, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            return await GetLatestCodespec(conn, name, cancellationToken);
        }, cancellationToken);
    }

    public async Task<Codespec?> GetLatestCodespec(NpgsqlConnection conn, string name, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var cmd = new NpgsqlCommand("""
                SELECT spec, version, created_at
                FROM codespecs
                WHERE name = $1
                ORDER BY version DESC
                LIMIT 1
                """, conn)
            {
                Parameters =
                {
                    new() { NpgsqlDbType = NpgsqlDbType.Text, Value = name },
                }
            };

            await cmd.PrepareAsync(cancellationToken);

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
        }, cancellationToken);
    }

    public async Task<(IList<Codespec>, string? nextContinuationToken)> GetCodespecs(int limit, string? prefix, string? continuationToken, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync<(IList<Codespec>, string? nextContinuationToken)>(async cancellationToken =>
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

            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var cmd = new NpgsqlCommand($"""
                SELECT DISTINCT ON (name) name, version, created_at, spec
                FROM codespecs
                WHERE name > $3 AND name LIKE $2
                ORDER BY name, version DESC
                LIMIT $1
                """, conn)
            {
                Parameters =
                {
                    new() { NpgsqlDbType = NpgsqlDbType.Integer, Value = limit + 1 },
                    new() { NpgsqlDbType = NpgsqlDbType.Text, Value = prefix + "%" },
                    new() { NpgsqlDbType = NpgsqlDbType.Text, Value = pagingName },
                }
            };

            await cmd.PrepareAsync(cancellationToken);

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
        }, cancellationToken);
    }

    public async Task<Codespec> UpsertCodespec(string name, Codespec newcodespec, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            newcodespec = newcodespec.WithoutSystemProperties();
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);

            Codespec? latestCodespec = await GetLatestCodespec(conn, name, cancellationToken);
            if (latestCodespec != null && newcodespec.Equals(latestCodespec.WithoutSystemProperties()))
            {
                return latestCodespec;
            }

            await using var cmd = new NpgsqlCommand("""
                INSERT INTO codespecs
                SELECT
                    $1,
                    CASE WHEN MAX(version) IS NULL THEN 1 ELSE MAX(version) + 1 END,
                    now() AT TIME ZONE 'utc',
                    $2
                FROM codespecs
                WHERE name = $1
                RETURNING version, created_at
                """, conn)
            {
                Parameters =
                {
                    new() { NpgsqlDbType = NpgsqlDbType.Text, Value = name },
                    new() { NpgsqlDbType = NpgsqlDbType.Jsonb, Value = JsonSerializer.Serialize(newcodespec, _serializerOptions) },
                }
            };

            await cmd.PrepareAsync(cancellationToken);

            for (int i = 0; ; i++)
            {
                try
                {
                    await using var reader = await cmd.ExecuteReaderAsync(cancellationToken);
                    await reader.ReadAsync(cancellationToken);
                    var version = reader.GetInt32(0);
                    var createdAt = reader.GetDateTime(1);
                    return newcodespec.WithSystemProperties(name, version, createdAt);
                }
                catch (PostgresException e) when (e.SqlState == PostgresErrorCodes.UniqueViolation)
                {
                    if (i == 5)
                    {
                        throw;
                    }
                }
            }
        }, cancellationToken);
    }

    public async Task<Run> CreateRun(Run newRun, string? idempotencyKey, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            newRun = newRun.WithoutSystemProperties() with { Status = RunStatus.Pending };

            await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await connection.BeginTransactionAsync(cancellationToken);
            using var getIdCommand = new NpgsqlCommand
            {
                Connection = connection,
                Transaction = tx,
                CommandText = """
                    SELECT nextval('runs_id_seq'), now() AT TIME ZONE 'utc'
                    """
            };

            await getIdCommand.PrepareAsync(cancellationToken);

            Run run;
            await using (var reader = await getIdCommand.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken))
            {
                await reader.ReadAsync(cancellationToken);
                run = newRun with { Id = reader.GetInt64(0), CreatedAt = reader.GetDateTime(1) };
            }

            var batch = new NpgsqlBatch(connection, tx);
            batch.BatchCommands.Add(new NpgsqlBatchCommand
            {
                CommandText = """
                    INSERT into runs (id, run, created_at)
                    VALUES ($1, $2, $3)
                    """,
                Parameters =
                {
                    new() { Value = run.Id, NpgsqlDbType = NpgsqlDbType.Bigint },
                    new() { Value = JsonSerializer.Serialize(run, _serializerOptions), NpgsqlDbType = NpgsqlDbType.Jsonb },
                    new() { Value = run.CreatedAt, NpgsqlDbType = NpgsqlDbType.TimestampTz },
                },
            });

            if (!string.IsNullOrEmpty(idempotencyKey))
            {
                batch.BatchCommands.Add(new NpgsqlBatchCommand
                {
                    CommandText = """
                        INSERT INTO run_idempotency_keys (idempotency_key, run_id)
                        VALUES ($1, $2)
                        """,
                    Parameters =
                    {
                        new() { Value = idempotencyKey, NpgsqlDbType = NpgsqlDbType.Text },
                        new() { Value = run.Id, NpgsqlDbType = NpgsqlDbType.Bigint },
                    },
                });
            }

            batch.BatchCommands.Add(new NpgsqlBatchCommand
            {
                CommandText = $"NOTIFY {NewRunChannelName};",
            });

            await batch.PrepareAsync(cancellationToken);
            await batch.ExecuteNonQueryAsync(cancellationToken);
            await tx.CommitAsync(cancellationToken);
            return run;
        }, cancellationToken);
    }

    public async Task UpdateRunAsResourcesCreated(long id, Run? run, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await conn.BeginTransactionAsync(cancellationToken);

            await using (var readRun = new NpgsqlCommand($"""
                UPDATE runs
                SET resources_created = true {(run != null ? ", run = $2" : "")}
                WHERE id = $1 AND status != 'Canceling'
                """, conn, tx)
            {
                Parameters =
               {
                    new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint },
               }
            })
            {

                if (run != null)
                {
                    readRun.Parameters.Add(new() { Value = JsonSerializer.Serialize(run, _serializerOptions), NpgsqlDbType = NpgsqlDbType.Jsonb });
                }

                await readRun.PrepareAsync(cancellationToken);
                if (await readRun.ExecuteNonQueryAsync(cancellationToken) == 1)
                {
                    await tx.CommitAsync(cancellationToken);
                    return;
                }
            }

            // read run and update state to Canceled
            await using (var readRun = new NpgsqlCommand($"""
                SELECT run
                FROM runs
                WHERE id = $1
                FOR UPDATE
                """, conn, tx)
            {
                Parameters =
               {
                    new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint },
               }
            })
            {
                await readRun.PrepareAsync(cancellationToken);
                await using var reader = await readRun.ExecuteReaderAsync(cancellationToken);
                if (!await reader.ReadAsync(cancellationToken))
                {
                    return;
                }

                var runJson = reader.GetString(0);
                run = JsonSerializer.Deserialize<Run>(runJson, _serializerOptions) ?? throw new InvalidOperationException("Failed to deserialize run.");
                if (run.Status != RunStatus.Canceling)
                {
                    throw new InvalidOperationException($"Expected run {id} to be in Canceling state, but it is in {run.Status} state.");
                }
            }

            var updatedRun = run with
            {
                Status = RunStatus.Canceled,
            };

            DateTime modifiedAt;
            await using (var updateRun = new NpgsqlCommand($"""
                UPDATE runs
                SET run = $2, resources_created = true, modified_at = now() AT TIME ZONE 'utc'
                WHERE id = $1
                RETURNING modified_at
                """, conn, tx)
            {
                Parameters =
                   {
                        new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint },
                        new() { Value = JsonSerializer.Serialize(updatedRun, _serializerOptions), NpgsqlDbType = NpgsqlDbType.Jsonb },
                   }
            })
            {
                await updateRun.PrepareAsync(cancellationToken);
                modifiedAt = (DateTime)(await updateRun.ExecuteScalarAsync(cancellationToken))!;
            }

            await using var notifyCommand = new NpgsqlCommand($"SELECT pg_notify('{RunChangedChannelName}', $1);", conn, tx);
            notifyCommand.Parameters.Add(new() { Value = JsonSerializer.Serialize(new ObservedRunState(updatedRun, modifiedAt), _serializerOptions), NpgsqlDbType = NpgsqlDbType.Text });
            await notifyCommand.PrepareAsync(cancellationToken);
            await notifyCommand.ExecuteNonQueryAsync(cancellationToken);

            await tx.CommitAsync(cancellationToken);
        }, cancellationToken);
    }

    public async Task UpdateRunAsFinal(long id, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var command = new NpgsqlCommand("""
                UPDATE runs
                SET final = true
                WHERE id = $1
                """, conn)
            {
                Parameters = { new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint } }
            };

            await command.PrepareAsync(cancellationToken);
            await command.ExecuteNonQueryAsync(cancellationToken);

            await using var notifyCommand = new NpgsqlCommand($"SELECT pg_notify('{RunFinalizedChannelName}', $1);", conn)
            {
                Parameters = { new() { Value = id.ToString(), NpgsqlDbType = NpgsqlDbType.Text } }
            };

            await notifyCommand.PrepareAsync(cancellationToken);
            await notifyCommand.ExecuteNonQueryAsync(cancellationToken);
        }, cancellationToken);
    }

    public async Task UpdateRunAsLogsArchived(long id, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var command = new NpgsqlCommand("""
                UPDATE runs
                SET logs_archived_at = now() AT TIME ZONE 'utc'
                WHERE id = $1 and logs_archived_at is null
                """, conn)
            {
                Parameters = { new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint } }
            };

            await command.PrepareAsync(cancellationToken);
            await command.ExecuteNonQueryAsync(cancellationToken);
        }, cancellationToken);
    }

    public async Task<Run?> CancelRun(long id, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await conn.BeginTransactionAsync(cancellationToken);
            Run run;
            bool resourcesCreated;
            await using (var readRun = new NpgsqlCommand($"""
                SELECT run, resources_created, final
                FROM runs
                WHERE id = $1
                FOR UPDATE
                """, conn, tx)
            {
                Parameters =
                {
                    new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint },
                }
            })
            {
                await readRun.PrepareAsync(cancellationToken);
                await using var reader = await readRun.ExecuteReaderAsync(cancellationToken);
                if (!await reader.ReadAsync(cancellationToken))
                {
                    return null;
                }

                var runJson = reader.GetString(0);
                run = JsonSerializer.Deserialize<Run>(runJson, _serializerOptions) ?? throw new InvalidOperationException("Failed to deserialize run.");
                resourcesCreated = reader.GetBoolean(1);
                var final = reader.GetBoolean(2);

                if (final || run.Status.IsTerminal())
                {
                    return run;
                }
            }

            var now = DateTimeOffset.UtcNow;
            var updatedRun = run with
            {
                Status = resourcesCreated ? RunStatus.Canceled : RunStatus.Canceling,
                StatusReason = "Canceled by user",
                FinishedAt = new DateTimeOffset(now.Year, now.Month, now.Day, now.Hour, now.Minute, now.Second, now.Offset),
            };

            DateTime modifiedAt;
            await using (var updateRun = new NpgsqlCommand($"""
                UPDATE runs
                SET run = $2, modified_at = now() AT TIME ZONE 'utc'
                WHERE id = $1
                RETURNING modified_at
                """, conn, tx)
            {
                Parameters =
                    {
                        new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint },
                        new() { Value = JsonSerializer.Serialize(updatedRun, _serializerOptions), NpgsqlDbType = NpgsqlDbType.Jsonb },
                    }
            })
            {
                await updateRun.PrepareAsync(cancellationToken);
                modifiedAt = (DateTime)(await updateRun.ExecuteScalarAsync(cancellationToken))!;
            }

            await using var notifyCommand = new NpgsqlCommand($"SELECT pg_notify('{RunChangedChannelName}', $1);", conn, tx);
            notifyCommand.Parameters.Add(new() { Value = JsonSerializer.Serialize(new ObservedRunState(updatedRun, modifiedAt), _serializerOptions), NpgsqlDbType = NpgsqlDbType.Text });
            await notifyCommand.PrepareAsync(cancellationToken);
            await notifyCommand.ExecuteNonQueryAsync(cancellationToken);

            await tx.CommitAsync(cancellationToken);
            return updatedRun;
        }, cancellationToken);
    }

    public async Task UpdateRunFromObservedState(ObservedRunState state, (string leaseName, string holder)? leaseHeldCondition, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await conn.BeginTransactionAsync(cancellationToken);

            Run updatedRun;
            await using (var readRun = new NpgsqlCommand($"""
                SELECT run
                FROM runs
                WHERE id = $1
                    AND status NOT IN ('Failed', 'Succeeded', 'Canceled', 'Canceling')
                FOR UPDATE
                """, conn)
            {
                Connection = conn,
                Transaction = tx,
                Parameters =
                {
                    new() { Value = state.Id, NpgsqlDbType = NpgsqlDbType.Bigint },
                }
            })
            {
                await readRun.PrepareAsync(cancellationToken);
                await using var reader = await readRun.ExecuteReaderAsync(cancellationToken);
                if (!await reader.ReadAsync(cancellationToken))
                {
                    // The run is already in a terminal or canceling state so we don't do anything
                    return;
                }

                var runJson = reader.GetString(0);
                var run = JsonSerializer.Deserialize<Run>(runJson, _serializerOptions) ?? throw new InvalidOperationException("Failed to deserialize run.");
                updatedRun = state.ApplyToRun(run);
                if (updatedRun.Equals(run))
                {
                    return;
                }
            }

            await using (var updateRun = new NpgsqlCommand($"""
                UPDATE runs
                SET run = $2, modified_at = now() AT TIME ZONE 'utc'
                WHERE id = $1
                {(leaseHeldCondition != null ? "AND EXISTS (SELECT 1 FROM leases WHERE id = $3 AND holder = $4)" : "")}
                RETURNING modified_at
                """,
                conn)
            {
                Connection = conn,
                Transaction = tx,
                Parameters =
                    {
                        new() { Value = state.Id, NpgsqlDbType = NpgsqlDbType.Bigint },
                        new() { Value = JsonSerializer.Serialize(updatedRun, _serializerOptions), NpgsqlDbType = NpgsqlDbType.Jsonb },
                    }
            })
            {
                if (leaseHeldCondition != null)
                {
                    updateRun.Parameters.Add(new() { Value = leaseHeldCondition.Value.leaseName, NpgsqlDbType = NpgsqlDbType.Text });
                    updateRun.Parameters.Add(new() { Value = leaseHeldCondition.Value.holder, NpgsqlDbType = NpgsqlDbType.Text });
                }

                await updateRun.PrepareAsync(cancellationToken);
                await using var reader = await updateRun.ExecuteReaderAsync(cancellationToken);
                if (!await reader.ReadAsync(cancellationToken))
                {
                    // Lost the lease
                    return;
                }

                var modifiedAt = reader.GetDateTime(0);
                state = state with { DatabaseUpdatedAt = modifiedAt };
            }

            await using var notifyCommand = new NpgsqlCommand($"SELECT pg_notify('{RunChangedChannelName}', $1);", conn, tx);
            notifyCommand.Parameters.Add(new() { Value = JsonSerializer.Serialize(state, _serializerOptions), NpgsqlDbType = NpgsqlDbType.Text });
            await notifyCommand.PrepareAsync(cancellationToken);
            await notifyCommand.ExecuteNonQueryAsync(cancellationToken);

            await tx.CommitAsync(cancellationToken);

            if (updatedRun.Status is RunStatus.Succeeded or RunStatus.Failed)
            {
                var timeToDetect = DateTimeOffset.UtcNow - updatedRun.FinishedAt!.Value;
                _logger.TerminalStateRecorded(state.Id, timeToDetect);
            }
        }, cancellationToken);
    }

    public async Task DeleteRun(long id, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var command = new NpgsqlCommand("""
                DELETE FROM runs
                WHERE id = $1
                """, conn)
            {
                Parameters =
                {
                    new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint },
                }
            };

            await command.PrepareAsync(cancellationToken);
            await command.ExecuteNonQueryAsync(cancellationToken);
        }, cancellationToken);
    }

    public async Task<(Run run, DateTimeOffset? modifiedAt, DateTimeOffset? logsArchivedAt, bool final)?> GetRun(long id, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync<(Run run, DateTimeOffset? modifiedAt, DateTimeOffset? logsArchivedAt, bool final)?>(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var cmd = new NpgsqlCommand("""
                SELECT run, final, logs_archived_at, resources_created, modified_at
                FROM runs
                WHERE id = $1
                """, conn)
            {
                Parameters =
                {
                    new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint },
                }
            };

            await cmd.PrepareAsync(cancellationToken);
            await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
            if (!await reader.ReadAsync(cancellationToken))
            {
                return null;
            }

            var runJson = reader.GetString(0);
            var final = reader.GetBoolean(1);
            var logsArchivedAt = reader.IsDBNull(2) ? (DateTime?)null : reader.GetDateTime(2);
            var run = JsonSerializer.Deserialize<Run>(runJson, _serializerOptions) ?? throw new InvalidOperationException("Failed to deserialize run.");
            var modifiedAt = reader.IsDBNull(4) ? (DateTime?)null : reader.GetDateTime(4);
            return (run, modifiedAt, logsArchivedAt, final);
        }, cancellationToken);
    }

    public async Task<IDictionary<RunStatus, long>> GetRunCounts(DateTimeOffset? since, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var cmd =
                since == null
                ? new NpgsqlCommand("""
                    SELECT status, count(*)
                    FROM runs
                    GROUP BY status
                    """, conn)
                : new NpgsqlCommand("""
                    SELECT status, count(*)
                    FROM runs
                    WHERE created_at > $1
                    GROUP BY status
                    """, conn)
                {
                    Parameters = { new() { Value = since.Value, NpgsqlDbType = NpgsqlDbType.TimestampTz } }
                };

            await cmd.PrepareAsync(cancellationToken);
            await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
            var res = new Dictionary<RunStatus, long>();
            while (await reader.ReadAsync(cancellationToken))
            {
                var status = reader.GetString(0);
                var count = reader.GetInt64(1);
                res.Add(Enum.Parse<RunStatus>(status), count);
            }

            return res;
        }, cancellationToken);
    }

    public async Task<IDictionary<RunStatus, long>> GetRunCountsWithCallbackForNonFinal(DateTimeOffset? since, Func<Run, CancellationToken, Task<Run>> updateRun, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            var res = new Dictionary<RunStatus, long>();
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await conn.BeginTransactionAsync(cancellationToken);
            await using (var finalsCommand =
                since == null
                ? new NpgsqlCommand("""
                    SELECT status, count(*)
                    FROM runs
                    WHERE final = true
                    GROUP BY status
                    """, conn, tx)
                : new NpgsqlCommand("""
                    SELECT status, count(*)
                    FROM runs
                    WHERE final = true AND created_at > $1
                    GROUP BY status
                    """, conn, tx)
                {
                    Parameters = { new() { Value = since.Value, NpgsqlDbType = NpgsqlDbType.TimestampTz } }
                })
            {
                await finalsCommand.PrepareAsync(cancellationToken);
                await using var finalsReader = await finalsCommand.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
                while (await finalsReader.ReadAsync(cancellationToken))
                {
                    var status = finalsReader.GetString(0);
                    var count = finalsReader.GetInt64(1);
                    res.Add(Enum.Parse<RunStatus>(status), count);
                }
            }

            await using var nonFinalsCommand = new NpgsqlCommand("""
                SELECT run
                FROM runs
                WHERE final = false
                """, conn, tx);

            await nonFinalsCommand.PrepareAsync(cancellationToken);
            await using var nonFinalsReader = await nonFinalsCommand.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
            while (await nonFinalsReader.ReadAsync(cancellationToken))
            {
                var runJson = nonFinalsReader.GetString(0);
                var run = JsonSerializer.Deserialize<Run>(runJson, _serializerOptions) ?? throw new InvalidOperationException("Failed to deserialize run.");
                var updatedRun = await updateRun(run, cancellationToken);
                if (!res.TryGetValue(updatedRun.Status!.Value, out var count))
                {
                    count = 0;
                }

                res[updatedRun.Status!.Value] = count + 1;
            }

            return res;
        }, cancellationToken);
    }

    public async Task<(IList<(Run run, bool final)>, string? nextContinuationToken)> GetRuns(int limit, bool onlyResourcesCreated, DateTimeOffset? since, string? continuationToken, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync<(IList<(Run run, bool final)>, string? nextContinuationToken)>(async cancellationToken =>
        {
            bool hasPredicate = onlyResourcesCreated;
            var sb = new StringBuilder();
            sb.Append($"""
                SELECT run, final
                FROM runs
                {(onlyResourcesCreated ? "WHERE resources_created = true\n" : "")}
                """);

            var parameters = new List<NpgsqlParameter>();
            int paramNumber = 0;

            if (continuationToken != null)
            {
                bool valid = false;
                try
                {
                    var fields = JsonSerializer.Deserialize<long[]>(Encoding.ASCII.GetString(Base32.ZBase32.Decode(continuationToken)), _serializerOptions);
                    if (fields is { Length: 1 or 2 })
                    {
                        var id = fields[^1];
                        sb.AppendLine($"{(hasPredicate ? "AND" : "WHERE")} id < ${++paramNumber}");
                        parameters.Add(new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint });
                        valid = true;
                        hasPredicate = true;
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
                sb.AppendLine($"{(hasPredicate ? "AND" : "WHERE")} created_at > ${++paramNumber}");
                parameters.Add(new() { Value = since.Value, NpgsqlDbType = NpgsqlDbType.TimestampTz });
            }

            sb.AppendLine("ORDER BY created_at DESC, id DESC");
            sb.AppendLine($"LIMIT ${++paramNumber}");
            parameters.Add(new() { Value = limit + 1, NpgsqlDbType = NpgsqlDbType.Integer });

            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var cmd = new NpgsqlCommand(sb.ToString(), conn);
            foreach (var parameter in parameters)
            {
                cmd.Parameters.Add(parameter);
            }

            await cmd.PrepareAsync(cancellationToken);
            await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);

            List<(Run run, bool final)> results = [];
            while (await reader.ReadAsync(cancellationToken))
            {
                var runJson = reader.GetString(0);
                var final = reader.GetBoolean(1);
                var run = JsonSerializer.Deserialize<Run>(runJson, _serializerOptions) ?? throw new InvalidOperationException("Failed to deserialize run.");

                results.Add((run, final));
            }

            if (results.Count == limit + 1)
            {
                results.RemoveAt(limit);
                var (run, final) = results[^1];
                string newToken = Base32.ZBase32.Encode(Encoding.ASCII.GetBytes(JsonSerializer.Serialize(new[] { run.Id!.Value }, _serializerOptions)));
                return (results, newToken);
            }

            return (results, null);
        }, cancellationToken);
    }

    public async Task<IList<Run>> GetPageOfRunsWhereResourcesNotCreated(CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            var oldestAllowable = DateTimeOffset.UtcNow.AddMinutes(-5);

            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var cmd = new NpgsqlCommand("""
                SELECT run
                FROM runs
                WHERE created_at < $1 AND NOT resources_created
                """, conn)
            {
                Parameters =
                {
                    new() { Value = oldestAllowable, NpgsqlDbType = NpgsqlDbType.TimestampTz },
                }
            };

            await cmd.PrepareAsync(cancellationToken);
            await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
            var results = new List<Run>();
            while (await reader.ReadAsync(cancellationToken))
            {
                var runJson = reader.GetString(0);
                results.Add(JsonSerializer.Deserialize<Run>(runJson, _serializerOptions)!);
            }

            return results;
        }, cancellationToken);
    }

    public async Task<bool> CheckBuffersExist(ICollection<string> bufferIds, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var cmd = new NpgsqlCommand
            {
                Connection = conn,
                CommandText = $"""
                    SELECT count(*)
                    FROM buffers
                    WHERE id = ANY($1)
                    """,
                Parameters =
                {
                    new() { Value = bufferIds.ToArray(), NpgsqlDbType = NpgsqlDbType.Array | NpgsqlDbType.Text },
                }
            };

            await cmd.PrepareAsync(cancellationToken);
            return (long)(await cmd.ExecuteScalarAsync(cancellationToken))! == bufferIds.Count;
        }, cancellationToken);
    }

    public async Task<Buffer?> GetBuffer(string id, string eTag, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var command = new NpgsqlCommand("""
                SELECT buffers.created_at, buffers.etag, tag_keys.name, buffer_tags.value
                FROM buffers
                LEFT JOIN buffer_tags
                    on buffers.id = buffer_tags.id
                    and buffer_tags.created_at = buffers.created_at
                LEFT JOIN tag_keys
                    on tag_keys.id = buffer_tags.key
                WHERE buffers.id = $1
                """, conn)
            {
                Parameters =
                {
                    new() { Value = id, NpgsqlDbType = NpgsqlDbType.Text },
                }
            };

            if (eTag != "")
            {
                command.CommandText += " and buffers.etag = $2";
                command.Parameters.Add(new() { Value = eTag, NpgsqlDbType = NpgsqlDbType.Text });
            }

            var tags = new Dictionary<string, string>();
            string currentETag = "";
            DateTimeOffset createdAt = DateTimeOffset.MinValue;

            await command.PrepareAsync(cancellationToken);
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
        }, cancellationToken);
    }

    private static async Task<long?> GetTagId(NpgsqlConnection conn, string name, CancellationToken cancellationToken)
    {
        await using var cmd = new NpgsqlCommand("""
            SELECT id
            FROM tag_keys
            WHERE name = $1
            """, conn)
        {
            Parameters =
            {
                new() { Value = name, NpgsqlDbType = NpgsqlDbType.Text },
            }
        };

        await cmd.PrepareAsync(cancellationToken);
        await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
        if (!await reader.ReadAsync(cancellationToken))
        {
            return null;
        }

        return reader.GetInt64(0);
    }

    public async Task<(IList<Buffer>, string? nextContinuationToken)> GetBuffers(IDictionary<string, string>? tags, int limit, string? continuationToken, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync<(IList<Buffer>, string? nextContinuationToken)>(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var command = new NpgsqlCommand
            {
                Connection = conn,
                Parameters =
                {
                    new() { Value = limit + 1, NpgsqlDbType = NpgsqlDbType.Integer },
                }
            };

            var commandText = new StringBuilder();
            string table = tags?.Count > 0 ? "buffer_tags" : "buffers";
            commandText.AppendLine($"""
                WITH matches AS (
                SELECT t1.id, t1.created_at
                FROM {table} AS t1
                """);

            int param = 2;

            if (tags?.Count > 0)
            {
                for (int x = 0; x < tags.Count - 1; x++)
                {
                    commandText.AppendLine($"INNER JOIN buffer_tags AS t{x + 2} ON t1.created_at = t{x + 2}.created_at and t1.id = t{x + 2}.id");
                }

                commandText.AppendLine("WHERE");

                int index = 1;
                foreach (var tag in tags)
                {
                    if (index != 1)
                    {
                        commandText.Append(" AND ");
                    }

                    var id = await GetTagId(conn, tag.Key, cancellationToken);
                    if (id == null)
                    {
                        return ([], null);
                    }

                    commandText.AppendLine($" t{index}.key = ${param} and t{index}.value = ${param + 1}");
                    command.Parameters.Add(new() { Value = id.Value, NpgsqlDbType = NpgsqlDbType.Bigint });
                    command.Parameters.Add(new() { Value = tag.Value, NpgsqlDbType = NpgsqlDbType.Text });
                    index++;
                    param += 2;
                }
            }

            if (continuationToken != null)
            {
                bool valid = false;
                try
                {
                    var fields = JsonSerializer.Deserialize<JsonElement[]>(Encoding.ASCII.GetString(Base32.ZBase32.Decode(continuationToken)), _serializerOptions);
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
                        command.Parameters.Add(new() { Value = new DateTimeOffset(fields[0].GetInt64(), TimeSpan.Zero), NpgsqlDbType = NpgsqlDbType.TimestampTz });
                        command.Parameters.Add(new() { Value = fields[1].GetString(), NpgsqlDbType = NpgsqlDbType.Text });
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

            commandText.AppendLine("""
                ORDER BY t1.created_at DESC, t1.id DESC
                    LIMIT $1
                )
                SELECT matches.id, matches.created_at, tag_keys.name, buffer_tags.value, buffers.etag
                FROM matches
                LEFT JOIN buffer_tags
                    ON matches.id = buffer_tags.id AND matches.created_at = buffer_tags.created_at
                LEFT JOIN tag_keys ON buffer_tags.key = tag_keys.id
                LEFT JOIN buffers ON matches.id = buffers.id AND matches.created_at = buffers.created_at
                ORDER BY matches.created_at DESC, matches.id DESC
                """);

            command.CommandText = commandText.ToString();
            await command.PrepareAsync(cancellationToken);

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
                    var tagName = reader.GetString(2);
                    var tagValue = reader.GetString(3);

                    currentTags[tagName] = tagValue;
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
                string newToken = Base32.ZBase32.Encode(Encoding.ASCII.GetBytes(JsonSerializer.Serialize(new object[] { last.CreatedAt.UtcTicks, last.Id }, _serializerOptions)));
                return (results, newToken);
            }

            return (results, null);
        }, cancellationToken);
    }

    public async Task<Buffer?> UpdateBufferById(string id, string eTag, IDictionary<string, string>? tags, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await connection.BeginTransactionAsync(IsolationLevel.Serializable, cancellationToken);
            string newETag = DateTime.UtcNow.Ticks.ToString();

            // Update and validate the buffer
            using var bufferCommand = new NpgsqlCommand
            {
                Connection = connection,
                Transaction = tx,
                CommandText = """
                    UPDATE buffers
                    SET etag = $1
                    WHERE id = $2

                    """,
                Parameters =
                    {
                        new() { Value = newETag, NpgsqlDbType = NpgsqlDbType.Text },
                        new() { Value = id, NpgsqlDbType = NpgsqlDbType.Text },
                    }
            };

            if (eTag != "")
            {
                bufferCommand.CommandText += " AND etag = $3";
                bufferCommand.Parameters.Add(new() { Value = eTag, NpgsqlDbType = NpgsqlDbType.Text });
            }

            bufferCommand.CommandText += " RETURNING created_at";

            await bufferCommand.PrepareAsync(cancellationToken);

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
                CommandText = """
                        DELETE FROM buffer_tags WHERE
                        id = $1 AND created_at = $2
                        """,
                Parameters =
                    {
                        new() { Value = id, NpgsqlDbType = NpgsqlDbType.Text },
                        new() { Value = createdAt, NpgsqlDbType = NpgsqlDbType.TimestampTz },
                    }
            };

            await deleteCommand.PrepareAsync(cancellationToken);
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
        }, cancellationToken);
    }

    private static async Task InsertTag(NpgsqlTransaction tx, string id, DateTimeOffset createdAt, KeyValuePair<string, string> tag, CancellationToken cancellationToken)
    {
        using var insertTagCommand = new NpgsqlCommand
        {
            Connection = tx.Connection,
            Transaction = tx,
            CommandText = """
                        WITH INS AS (INSERT INTO tag_keys (name) VALUES ($4) ON CONFLICT DO NOTHING RETURNING id)
                        INSERT INTO buffer_tags (id, created_at, key, value)
                        (SELECT $1, $2, id, $3 FROM INS UNION
                        SELECT $1, $2, tag_keys.id, $3 FROM tag_keys WHERE name = $4)
                        """,

            Parameters =
            {
                new() { Value = id, NpgsqlDbType = NpgsqlDbType.Text },
                new() { Value = createdAt, NpgsqlDbType = NpgsqlDbType.TimestampTz },
                new() { Value = tag.Value, NpgsqlDbType = NpgsqlDbType.Text },
                new() { Value = tag.Key, NpgsqlDbType = NpgsqlDbType.Text },
            }
        };

        await insertTagCommand.PrepareAsync(cancellationToken);
        if (await insertTagCommand.ExecuteNonQueryAsync(cancellationToken) != 1)
        {
            throw new InvalidOperationException("Failed to insert tag: incorrect number of rows inserted");
        }
    }

    public async Task<Run> CreateRunWithIdempotencyKeyGuard(Run newRun, string idempotencyKey, Func<Run, CancellationToken, Task<Run>> createRun, CancellationToken cancellationToken)
    {
        // NOTE: no retrying for this method, because we don't want createRun to be called multiple times
        const int BaseAdvisoryLockId = 864555;
        await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
        await using var tx = await connection.BeginTransactionAsync(cancellationToken);

        using (var lockCommand = new NpgsqlCommand
        {
            Connection = connection,
            Transaction = tx,
            CommandText = $"SELECT pg_advisory_xact_lock({BaseAdvisoryLockId}, hashtext($1))",
            Parameters =
                {
                    new() { Value = idempotencyKey, NpgsqlDbType = NpgsqlDbType.Text },
                }
        })
        {
            await lockCommand.PrepareAsync(cancellationToken);
            await lockCommand.ExecuteNonQueryAsync(cancellationToken);
        }

        using (var selectCommand = new NpgsqlCommand
        {
            Connection = connection,
            Transaction = tx,
            CommandText = """
                SELECT run_id
                FROM run_idempotency_keys
                WHERE idempotency_key = $1
                """,
            Parameters =
                {
                    new() { Value = idempotencyKey, NpgsqlDbType = NpgsqlDbType.Text },
                }
        })
        {
            await selectCommand.PrepareAsync(cancellationToken);
            await using var reader = await selectCommand.ExecuteReaderAsync(cancellationToken);
            if (await reader.ReadAsync(cancellationToken))
            {
                long runId = reader.GetInt64(0);
                (var run, _, _, _) = await GetRun(runId, cancellationToken) ?? throw new InvalidOperationException("Failed to get run with existing idempotency key.");
                return run;
            }
        }

        return await createRun(newRun, cancellationToken);
    }

    public async Task<Buffer> CreateBuffer(Buffer newBuffer, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await connection.BeginTransactionAsync(IsolationLevel.Serializable, cancellationToken);
            string eTag = DateTime.UtcNow.Ticks.ToString();

            // Create the buffer DB entry
            using var insertCommand = new NpgsqlCommand
            {
                Connection = connection,
                Transaction = tx,
                CommandText = """
                        INSERT INTO buffers (id, created_at, etag)
                        VALUES ($1, now() AT TIME ZONE 'utc', $2)
                        RETURNING created_at
                        """,
                Parameters =
                    {
                        new() { Value = newBuffer.Id, NpgsqlDbType = NpgsqlDbType.Text },
                        new() { Value = eTag, NpgsqlDbType = NpgsqlDbType.Text },
                    }
            };

            await insertCommand.PrepareAsync(cancellationToken);

            var buffer = newBuffer with { ETag = eTag };
            await using (var reader = await insertCommand.ExecuteReaderAsync(cancellationToken))
            {
                await reader.ReadAsync(cancellationToken);

                buffer = buffer with { CreatedAt = reader.GetDateTime(0), ETag = eTag };
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
        }, cancellationToken);
    }

    public async Task ListenForNewRuns(Func<IReadOnlyList<Run>, CancellationToken, Task> processRuns, CancellationToken cancellationToken)
    {
        // no need for retries, as this method is invoked in a loop with try/catch

        const long AdvisoryLockId = 2120278927;
        const int MaxPageSize = 100;

        while (true)
        {
            await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using (var listenCommand = new NpgsqlCommand
            {
                Connection = connection,
                CommandText = $"LISTEN {NewRunChannelName}; LISTEN {RunFinalizedChannelName};",
            })
            {
                await listenCommand.PrepareAsync(cancellationToken);
                await listenCommand.ExecuteNonQueryAsync(cancellationToken);
            }

            while (true)
            {
                bool somethingProcessed = false;
                await using (var tx = await connection.BeginTransactionAsync(IsolationLevel.Serializable, cancellationToken))
                {
                    await using (var takeLockCommand = new NpgsqlCommand
                    {
                        Connection = connection,
                        Transaction = tx,
                        CommandText = "SELECT pg_advisory_xact_lock($1)",
                        Parameters = { new() { Value = AdvisoryLockId, NpgsqlDbType = NpgsqlDbType.Bigint } }
                    })
                    {
                        await takeLockCommand.PrepareAsync(cancellationToken);
                        await takeLockCommand.ExecuteNonQueryAsync(cancellationToken);
                    }

                    long activeRuns;
                    using (var countActiveRunsCommand = new NpgsqlCommand
                    {
                        Connection = connection,
                        Transaction = tx,
                        CommandText = "SELECT COUNT(*) FROM runs WHERE resources_created = true AND final = false",
                    })
                    {
                        await countActiveRunsCommand.PrepareAsync(cancellationToken);
                        activeRuns = (long)(await countActiveRunsCommand.ExecuteScalarAsync(cancellationToken))!;
                    }

                    if (activeRuns < MaxActiveRuns)
                    {
                        var limit = Math.Max(MaxPageSize, MaxActiveRuns - activeRuns);
                        var runs = new List<Run>();
                        using (var getPageCommand = new NpgsqlCommand
                        {
                            Connection = connection,
                            Transaction = tx,
                            CommandText = """
                            SELECT run
                            FROM runs
                            WHERE resources_created = false and final = false
                            ORDER BY created_at ASC
                            LIMIT $1
                            """,
                            Parameters = { new() { Value = limit, NpgsqlDbType = NpgsqlDbType.Integer } }
                        })
                        {
                            await getPageCommand.PrepareAsync(cancellationToken);
                            await using var reader = await getPageCommand.ExecuteReaderAsync(cancellationToken);
                            while (await reader.ReadAsync(cancellationToken))
                            {
                                runs.Add(JsonSerializer.Deserialize<Run>(reader.GetString(0), _serializerOptions)!);
                            }
                        }

                        if (runs.Count > 0)
                        {
                            somethingProcessed = true;
                            await processRuns(runs, cancellationToken);
                        }
                    }
                }

                if (somethingProcessed)
                {
                    continue;
                }

                if (!await connection.WaitAsync(TimeSpan.FromMinutes(1), cancellationToken))
                {
                    break;
                }
            }
        }
    }

    public async Task ListenForRunUpdates(DateTimeOffset? since, Func<ObservedRunState, CancellationToken, Task> processRunUpdates, CancellationToken cancellationToken)
    {
        // no need for retries, as this method is invoked in a loop with try/catch

        await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
        List<string> payloads = [];
        connection.Notification += (s, e) => payloads.Add(e.Payload);

        async Task ProcessPayloads()
        {
            foreach (var payload in payloads)
            {
                var runState = JsonSerializer.Deserialize<ObservedRunState>(payload, _serializerOptions);
                await processRunUpdates(runState, cancellationToken);
            }

            payloads.Clear();
        }

        await using (var listenCommand = new NpgsqlCommand
        {
            Connection = connection,
            CommandText = $"LISTEN {RunChangedChannelName};",
        })
        {
            await listenCommand.PrepareAsync(cancellationToken);
            await listenCommand.ExecuteNonQueryAsync(cancellationToken);
        }

        await using (var readExistingCommand = new NpgsqlCommand
        {
            Connection = connection,
            CommandText = $"""
                SELECT run, modified_at from runs
                WHERE {(since == null ? "final = false AND (resources_created = true OR status IN ('Failed', 'Succeeded', 'Canceled'))" : "modified_at > $1")}
                """,
        })
        {
            if (since != null)
            {
                readExistingCommand.Parameters.Add(new() { Value = since.Value, NpgsqlDbType = NpgsqlDbType.TimestampTz });
            }

            await readExistingCommand.PrepareAsync(cancellationToken);

            await using var reader = await readExistingCommand.ExecuteReaderAsync(cancellationToken);
            while (await reader.ReadAsync(cancellationToken))
            {
                var runString = reader.GetString(0);
                var modifiedAt = reader.IsDBNull(1) ? (DateTime?)null : reader.GetDateTime(1);
                var run = JsonSerializer.Deserialize<Run>(runString, _serializerOptions);
                await processRunUpdates(new ObservedRunState(run!, modifiedAt), cancellationToken);
            }
        }

        await ProcessPayloads();

        while (true)
        {
            if (await connection.WaitAsync(TimeSpan.FromMinutes(1), cancellationToken))
            {
                await ProcessPayloads();
            }
        }
    }

    public async Task PruneRunModifedAtIndex(DateTimeOffset cutoff, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var command = new NpgsqlCommand
            {
                Connection = connection,
                CommandText = "UPDATE runs SET modified_at = NULL WHERE final = true and modified_at < $1",
                Parameters = { new() { Value = cutoff, NpgsqlDbType = NpgsqlDbType.TimestampTz } }
            };

            await command.PrepareAsync(cancellationToken);
            await command.ExecuteNonQueryAsync(cancellationToken);
        }, cancellationToken);
    }

    public async Task AcquireAndHoldLease(string leaseName, string holder, Func<bool, ValueTask> onLockStateChange, CancellationToken cancellationToken)
    {
        // no need for retries, as this method is invoked in a loop with try/catch

        var leaseDuration = TimeSpan.FromSeconds(60);
        var renewInterval = TimeSpan.FromSeconds(5);

        var leaseHeld = false;

        async ValueTask<bool> UpdateLeaseHeld(bool newHeld)
        {
            if (newHeld == leaseHeld)
            {
                return leaseHeld;
            }

            leaseHeld = newHeld;
            if (leaseHeld)
            {
                _logger.LeaseAcquired(leaseName);
            }
            else
            {
                _logger.LeaseLost(leaseName);
            }

            await onLockStateChange(leaseHeld);
            return leaseHeld;
        }

        try
        {
            while (true)
            {
                try
                {
                    await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);

                    await using (var insertCommand = new NpgsqlCommand("""
                    WITH upsert AS (
                        INSERT INTO leases (id, holder, expiration)
                        VALUES ($1, $2, now() AT TIME ZONE 'utc' + $3)
                        ON CONFLICT (id)
                        DO UPDATE SET holder = $2, expiration = now() AT TIME ZONE 'utc' + $3
                        WHERE leases.id = $1 AND (leases.expiration < now() AT TIME ZONE 'utc' OR leases.holder = $2)
                        RETURNING true, expiration
                    )
                    SELECT * FROM upsert
                    UNION ALL
                    SELECT false, expiration
                    FROM leases
                    WHERE id = $1
                    """, connection))
                    {
                        insertCommand.Parameters.Add(new() { Value = leaseName, NpgsqlDbType = NpgsqlDbType.Text });
                        insertCommand.Parameters.Add(new() { Value = holder, NpgsqlDbType = NpgsqlDbType.Text });
                        insertCommand.Parameters.Add(new() { Value = leaseDuration, NpgsqlDbType = NpgsqlDbType.Interval });

                        await insertCommand.PrepareAsync(cancellationToken);
                        await using var reader = await insertCommand.ExecuteReaderAsync(cancellationToken);
                        await reader.ReadAsync(cancellationToken);
                        await UpdateLeaseHeld(reader.GetBoolean(0));
                        var expiration = reader.GetDateTime(1);
                        if (!leaseHeld)
                        {
                            var timeToExpiration = expiration - DateTimeOffset.UtcNow;
                            if (timeToExpiration > TimeSpan.Zero)
                            {
                                var toWait = TimeSpan.FromSeconds(Math.Min(30, timeToExpiration.TotalSeconds)) + TimeSpan.FromSeconds(Random.Shared.NextDouble());
                                await Task.Delay(toWait, cancellationToken);
                                continue;
                            }
                        }
                    }

                    while (true)
                    {
                        if (cancellationToken.IsCancellationRequested)
                        {
                            return;
                        }

                        await Task.Delay(renewInterval, cancellationToken);
                        await using var renewCommand = new NpgsqlCommand("""
                            UPDATE leases
                            SET expiration = now() AT TIME ZONE 'utc' + $1
                            WHERE id = $2 AND holder = $3
                            """, connection)
                        {
                            Parameters =
                                {
                                    new() { Value = leaseDuration, NpgsqlDbType = NpgsqlDbType.Interval },
                                    new() { Value = leaseName, NpgsqlDbType = NpgsqlDbType.Text },
                                    new() { Value = holder, NpgsqlDbType = NpgsqlDbType.Text },
                                }
                        };

                        await renewCommand.PrepareAsync(cancellationToken);
                        if (!await UpdateLeaseHeld(await renewCommand.ExecuteNonQueryAsync(cancellationToken) == 1))
                        {
                            break;
                        }
                    }
                }
                catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
                {
                    return;
                }
                catch (Exception e)
                {
                    _logger.LeaseException(leaseName, e);
                    await Task.Delay(TimeSpan.FromSeconds(5), cancellationToken);
                }
            }
        }
        finally
        {
            if (leaseHeld)
            {
                try
                {
                    var releaseCancellationToken = new CancellationTokenSource(TimeSpan.FromSeconds(5)).Token;
                    await using var connection = await _dataSource.OpenConnectionAsync(releaseCancellationToken);
                    await using var releaseCommand = new NpgsqlCommand("""
                        DELETE FROM leases
                        WHERE id = $1 AND holder = $2
                        """, connection)
                    {
                        Parameters =
                                {
                                    new() { Value = leaseName, NpgsqlDbType = NpgsqlDbType.Text },
                                    new() { Value = holder, NpgsqlDbType = NpgsqlDbType.Text },
                                }
                    };

                    await releaseCommand.PrepareAsync(releaseCancellationToken);
                    await releaseCommand.ExecuteNonQueryAsync(releaseCancellationToken);
                    await UpdateLeaseHeld(false);
                }
                catch (OperationCanceledException)
                {
                }
                catch (Exception e)
                {
                    _logger.LeaseReleaseException(leaseName, e);
                }
            }
        }
    }
}
