// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Data;
using System.IO.Hashing;
using System.Text;
using System.Text.Json;
using Dunet;
using Microsoft.Extensions.ObjectPool;
using Npgsql;
using NpgsqlTypes;
using Polly;
using SimpleBase;
using Tyger.ControlPlane.Model;
using Buffer = Tyger.ControlPlane.Model.Buffer;

namespace Tyger.ControlPlane.Database;

public class Repository
{
    private const int MaxActiveRuns = 5000;
    private const string NewRunChannelName = "new_run";
    private const string RunFinalizedChannelName = "run_finalized";
    private const string RunChangedChannelName = "run_changed";

    private readonly NpgsqlDataSource _dataSource;
    private readonly ResiliencePipeline _resiliencePipeline;
    private readonly ObjectPool<XxHash3> _xxHash3Pool;
    private readonly Lazy<IRunAugmenter>? _runAugmenter;
    private readonly JsonSerializerOptions _serializerOptions;
    private readonly ILogger<Repository> _logger;

    public Repository(
        NpgsqlDataSource dataSource,
        ResiliencePipeline resiliencePipeline,
        IEnumerable<Lazy<IRunAugmenter>> runAugmenter,
        ObjectPool<XxHash3> xxHash3Pool,
        JsonSerializerOptions serializerOptions,
        ILogger<Repository> logger)
    {
        _dataSource = dataSource;
        _resiliencePipeline = resiliencePipeline;
        _xxHash3Pool = xxHash3Pool;
        _runAugmenter = runAugmenter.FirstOrDefault();
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
                    CreateJsonbParameter(newcodespec),
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
                    CreateJsonbParameter(run),
                    new() { Value = run.CreatedAt, NpgsqlDbType = NpgsqlDbType.TimestampTz },
                },
            });

            await UpsertTagsCore(tx, new RunUpdate { Id = run.Id, CreatedAt = run.CreatedAt, Tags = run.Tags }, false, null, cancellationToken);

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

    public async Task<UpdateWithPreconditionResult<Run>> UpdateRunTags(RunUpdate runUpdate, string? eTagPrecondition, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync<UpdateWithPreconditionResult<Run>>(async cancellationToken =>
        {
            await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await connection.BeginTransactionAsync(cancellationToken);

            int tagsVersion = -1;
            // Update and validate the buffer
            using var bufferCommand = new NpgsqlCommand
            {
                Connection = connection,
                Transaction = tx,
                CommandText = """
                    SELECT run, created_at, final, tags_version FROM runs
                    WHERE id = $1
                    FOR UPDATE
                    """,
                Parameters =
                    {
                        new() { Value = runUpdate.Id, NpgsqlDbType = NpgsqlDbType.Bigint },
                    }
            };

            await bufferCommand.PrepareAsync(cancellationToken);

            Run? run = null;
            bool final = false;
            await using (var reader = await bufferCommand.ExecuteReaderAsync(cancellationToken))
            {
                bool any = false;
                while (await reader.ReadAsync(cancellationToken))
                {
                    any = true;
                    var runJson = reader.GetString(0);
                    run = JsonSerializer.Deserialize<Run>(runJson, _serializerOptions) ?? throw new InvalidOperationException("Failed to deserialize run.");
                    run = run with { Tags = runUpdate.Tags, CreatedAt = reader.GetDateTime(1) };
                    runUpdate = runUpdate with { CreatedAt = run.CreatedAt };
                    final = reader.GetBoolean(2);
                    tagsVersion = reader.GetInt32(3);
                }

                if (!any)
                {
                    return new UpdateWithPreconditionResult<Run>.NotFound();
                }
            }

            if (!final && _runAugmenter != null)
            {
                run = await _runAugmenter.Value.AugmentRun(run!, cancellationToken);
            }

            var hash = _xxHash3Pool.Get();
            try
            {
                var existingTags = await UpsertTagsCore(tx, runUpdate, true, eTagPrecondition, cancellationToken);
                if (!string.IsNullOrEmpty(eTagPrecondition))
                {
                    var existingRun = run! with { Tags = existingTags };
                    if (!string.Equals(existingRun.ComputeEtag(hash), eTagPrecondition, StringComparison.Ordinal))
                    {
                        await tx.RollbackAsync(cancellationToken);
                        return new UpdateWithPreconditionResult<Run>.PreconditionFailed();
                    }
                }

                run!.ComputeEtag(hash);
            }
            finally
            {
                hash.Reset();
                _xxHash3Pool.Return(hash);
            }

            await using var updateTagsVersionCommand = new NpgsqlCommand("""
                UPDATE runs
                SET tags_version = tags_version + 1, modified_at = now() AT TIME ZONE 'utc'
                WHERE id = $1
                RETURNING modified_at
                """, connection, tx)
            {
                Parameters =
                {
                    new() { Value = runUpdate.Id, NpgsqlDbType = NpgsqlDbType.Integer },
                }
            };

            await updateTagsVersionCommand.PrepareAsync(cancellationToken);
            var modifiedAt = (DateTime)(await updateTagsVersionCommand.ExecuteScalarAsync(cancellationToken))!;

            await using var notifyCommand = new NpgsqlCommand($"SELECT pg_notify('{RunChangedChannelName}', $1);", connection, tx);
            notifyCommand.Parameters.Add(new() { Value = JsonSerializer.Serialize(new ObservedRunState(run, modifiedAt) { TagsVersion = tagsVersion + 1 }, _serializerOptions), NpgsqlDbType = NpgsqlDbType.Text });
            await notifyCommand.PrepareAsync(cancellationToken);
            await notifyCommand.ExecuteNonQueryAsync(cancellationToken);

            await tx.CommitAsync(cancellationToken);
            return new UpdateWithPreconditionResult<Run>.Updated(run);
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
                    readRun.Parameters.Add(CreateJsonbParameter(run));
                }

                await readRun.PrepareAsync(cancellationToken);
                if (await readRun.ExecuteNonQueryAsync(cancellationToken) == 1)
                {
                    await tx.CommitAsync(cancellationToken);
                    return;
                }
            }

            int tagsVersion;
            // read run and update state to Canceled
            await using (var readRun = new NpgsqlCommand($"""
                SELECT run, tags_version
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

                tagsVersion = reader.GetInt32(1);
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
                        CreateJsonbParameter(updatedRun),
                   }
            })
            {
                await updateRun.PrepareAsync(cancellationToken);
                modifiedAt = (DateTime)(await updateRun.ExecuteScalarAsync(cancellationToken))!;
            }

            await using var notifyCommand = new NpgsqlCommand($"SELECT pg_notify('{RunChangedChannelName}', $1);", conn, tx);
            notifyCommand.Parameters.Add(new() { Value = JsonSerializer.Serialize(new ObservedRunState(updatedRun, modifiedAt) { TagsVersion = tagsVersion }, _serializerOptions), NpgsqlDbType = NpgsqlDbType.Text });
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
            int tagsVersion;
            await using (var readRun = new NpgsqlCommand($"""
                SELECT run, resources_created, final, tags_version
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

                tagsVersion = reader.GetInt32(3);
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
                        CreateJsonbParameter(updatedRun),
                    }
            })
            {
                await updateRun.PrepareAsync(cancellationToken);
                modifiedAt = (DateTime)(await updateRun.ExecuteScalarAsync(cancellationToken))!;
            }

            await using var notifyCommand = new NpgsqlCommand($"SELECT pg_notify('{RunChangedChannelName}', $1);", conn, tx);
            notifyCommand.Parameters.Add(new() { Value = JsonSerializer.Serialize(new ObservedRunState(updatedRun, modifiedAt) { TagsVersion = tagsVersion }, _serializerOptions), NpgsqlDbType = NpgsqlDbType.Text });
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
                        CreateJsonbParameter(updatedRun),
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

    public async Task ForceUpdateRun(Run run, CancellationToken cancellationToken)
    {
        await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await conn.BeginTransactionAsync(cancellationToken);
            await using var updateRunCommand = new NpgsqlCommand("""
                UPDATE runs
                SET run = $2, modified_at = now() AT TIME ZONE 'utc'
                WHERE id = $1
                RETURNING modified_at, tags_version
                """, conn, tx)
            {
                Parameters =
                {
                    new() { Value = run.Id, NpgsqlDbType = NpgsqlDbType.Bigint },
                    CreateJsonbParameter(run),
                }
            };

            await updateRunCommand.PrepareAsync(cancellationToken);
            DateTime modifiedAt;
            int tagsVersion;
            await using (var reader = await updateRunCommand.ExecuteReaderAsync(cancellationToken))
            {
                await reader.ReadAsync(cancellationToken);
                modifiedAt = reader.GetDateTime(0);
                tagsVersion = reader.GetInt32(1);
            }

            await using var notifyCommand = new NpgsqlCommand($"SELECT pg_notify('{RunChangedChannelName}', $1);", conn, tx);
            notifyCommand.Parameters.Add(new() { Value = JsonSerializer.Serialize(new ObservedRunState(run, modifiedAt) { TagsVersion = tagsVersion }, _serializerOptions), NpgsqlDbType = NpgsqlDbType.Text });
            await notifyCommand.PrepareAsync(cancellationToken);
            await notifyCommand.ExecuteNonQueryAsync(cancellationToken);

            await tx.CommitAsync(cancellationToken);
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

    public async Task<(Run run, DateTimeOffset? modifiedAt, DateTimeOffset? logsArchivedAt, bool final, int tagsVersion)?> GetRun(long id, CancellationToken cancellationToken, GetRunOptions options = GetRunOptions.None)
    {
        return await _resiliencePipeline.ExecuteAsync<(Run run, DateTimeOffset? modifiedAt, DateTimeOffset? logsArchivedAt, bool final, int tagsVersion)?>(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var cmd = new NpgsqlCommand($"""
                SELECT run, final, logs_archived_at, resources_created, modified_at, tags_version {((options & GetRunOptions.SkipTags) == GetRunOptions.SkipTags ? "" : ", tag_keys.name, run_tags.value")}
                FROM runs
                {((options & GetRunOptions.SkipTags) == GetRunOptions.SkipTags ? "" : "LEFT JOIN run_tags ON runs.id = run_tags.id LEFT JOIN tag_keys ON run_tags.key = tag_keys.id")}
                WHERE runs.id = $1
                {((options & GetRunOptions.SkipTags) == GetRunOptions.SkipTags ? "" : "ORDER BY tag_keys.name")}
                """, conn)
            {
                Parameters =
                {
                    new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint },
                }
            };

            await cmd.PrepareAsync(cancellationToken);
            await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);

            bool final = false;
            DateTimeOffset? logsArchivedAt = null;
            DateTimeOffset? modifiedAt = null;
            Run? run = null;
            OrderedDictionary<string, string>? tags = null;
            int tagsVersion = 0;

            while (await reader.ReadAsync(cancellationToken))
            {
                if (run == null)
                {
                    var runJson = reader.GetString(0);
                    final = reader.GetBoolean(1);
                    logsArchivedAt = reader.IsDBNull(2) ? (DateTime?)null : reader.GetDateTime(2);
                    run = JsonSerializer.Deserialize<Run>(runJson, _serializerOptions) ?? throw new InvalidOperationException("Failed to deserialize run.");
                    modifiedAt = reader.IsDBNull(4) ? (DateTime?)null : reader.GetDateTime(4);
                    if (!final && (options & GetRunOptions.SkipAugmentation) == 0 && _runAugmenter != null)
                    {
                        run = await _runAugmenter.Value.AugmentRun(run, cancellationToken);
                    }

                    tagsVersion = reader.GetInt32(5);
                }

                if ((options & GetRunOptions.SkipTags) == 0 && !reader.IsDBNull(6))
                {
                    var name = reader.GetString(6);
                    var value = reader.GetString(7);
                    (tags ??= []).Add(name, value);
                }
            }

            if (run == null)
            {
                return null;
            }

            run = run with { Tags = tags };
            var hash = _xxHash3Pool.Get();
            try
            {
                run.ComputeEtag(hash);
            }
            finally
            {
                hash.Reset();
                _xxHash3Pool.Return(hash);
            }

            return (run, modifiedAt, logsArchivedAt, final, tagsVersion);
        }, cancellationToken);
    }

    public async Task<IDictionary<RunStatus, long>> GetRunCounts(DateTimeOffset? since, Dictionary<string, string>? tags, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await conn.BeginTransactionAsync(cancellationToken);
            var res = new Dictionary<RunStatus, long>();

            var commonClauses = new StringBuilder();
            var parameters = new List<NpgsqlParameter>();
            var paramNumber = 0;
            NpgsqlParameter? finalParameter = null;

            if (tags?.Count > 0)
            {
                for (int x = 0; x < tags.Count; x++)
                {
                    commonClauses.AppendLine($"INNER JOIN run_tags AS t{x} ON runs.created_at = t{x}.created_at and runs.id = t{x}.id");
                }

                commonClauses.AppendLine("WHERE");

                int index = 0;
                foreach (var tag in tags)
                {
                    if (index > 0)
                    {
                        commonClauses.Append(" AND ");
                    }

                    var id = await GetTagId(conn, tag.Key, cancellationToken);
                    if (id == null)
                    {
                        return res;
                    }

                    commonClauses.AppendLine($"      t{index}.key = ${++paramNumber} and t{index}.value = ${++paramNumber}");
                    parameters.Add(new() { Value = id.Value, NpgsqlDbType = NpgsqlDbType.Bigint });
                    parameters.Add(new() { Value = tag.Value, NpgsqlDbType = NpgsqlDbType.Text });
                    index++;
                }
            }

            if (since != null)
            {
                commonClauses.AppendLine($"{(paramNumber > 0 ? "AND" : "WHERE")} runs.created_at > ${++paramNumber}");
                parameters.Add(new() { Value = since.Value, NpgsqlDbType = NpgsqlDbType.TimestampTz });
            }

            if (_runAugmenter != null)
            {
                commonClauses.AppendLine($"{(paramNumber > 0 ? "AND" : "WHERE")} runs.final = ${++paramNumber}");
                parameters.Add(finalParameter = new() { Value = true, NpgsqlDbType = NpgsqlDbType.Boolean });
            }

            await using var cmd = new NpgsqlCommand($"""
                    SELECT runs.status, count(*)
                    FROM runs
                    {commonClauses}
                    GROUP BY runs.status
                    """, conn, tx);

            foreach (var parameter in parameters)
            {
                cmd.Parameters.Add(parameter);
            }

            await cmd.PrepareAsync(cancellationToken);
            await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
            while (await reader.ReadAsync(cancellationToken))
            {
                var status = reader.GetString(0);
                var count = reader.GetInt64(1);
                res.Add(Enum.Parse<RunStatus>(status), count);
            }

            if (_runAugmenter != null)
            {
                await using var nonFinalsCommand = new NpgsqlCommand($"""
                    SELECT runs.run
                    {commonClauses}
                    FROM runs
                    """, conn, tx);

                foreach (var parameter in parameters)
                {
                    nonFinalsCommand.Parameters.Add(parameter);
                }

                finalParameter!.Value = false;

                await nonFinalsCommand.PrepareAsync(cancellationToken);
                await using var nonFinalsReader = await nonFinalsCommand.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
                while (await nonFinalsReader.ReadAsync(cancellationToken))
                {
                    var runJson = nonFinalsReader.GetString(0);
                    var run = JsonSerializer.Deserialize<Run>(runJson, _serializerOptions) ?? throw new InvalidOperationException("Failed to deserialize run.");
                    var updatedRun = await _runAugmenter.Value.AugmentRun(run, cancellationToken);
                    if (!res.TryGetValue(updatedRun.Status!.Value, out var count))
                    {
                        count = 0;
                    }

                    res[updatedRun.Status!.Value] = count + 1;
                }
            }

            return res;

        }, cancellationToken);
    }

    public async Task<(IList<(Run run, bool final)>, string? nextContinuationToken)> GetRuns(GetRunsOptions options, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync<(IList<(Run run, bool final)>, string? nextContinuationToken)>(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            var sb = new StringBuilder();
            sb.AppendLine($"""
                WITH matches AS (
                    SELECT runs.created_at, runs.id, runs.run, runs.final
                    FROM runs
                """);

            var parameters = new List<NpgsqlParameter>();
            int paramNumber = 0;

            if (options.Tags?.Count > 0)
            {
                for (int x = 0; x < options.Tags.Count; x++)
                {
                    sb.AppendLine($"    INNER JOIN run_tags AS t{x} ON runs.created_at = t{x}.created_at and runs.id = t{x}.id");
                }

                sb.AppendLine("    WHERE");

                int index = 0;
                foreach (var tag in options.Tags)
                {
                    if (index > 0)
                    {
                        sb.Append(" AND ");
                    }

                    var id = await GetTagId(conn, tag.Key, cancellationToken);
                    if (id == null)
                    {
                        return ([], null);
                    }

                    sb.AppendLine($"      t{index}.key = ${++paramNumber} and t{index}.value = ${++paramNumber}");
                    parameters.Add(new() { Value = id.Value, NpgsqlDbType = NpgsqlDbType.Bigint });
                    parameters.Add(new() { Value = tag.Value, NpgsqlDbType = NpgsqlDbType.Text });
                    index++;
                }
            }

            if (!string.IsNullOrEmpty(options.ContinuationToken))
            {
                bool valid = false;
                try
                {
                    var fields = JsonSerializer.Deserialize<long[]>(Encoding.ASCII.GetString(Base32.ZBase32.Decode(options.ContinuationToken)), _serializerOptions);
                    if (fields is { Length: 2 })
                    {
                        var createdAt = new DateTimeOffset(fields[0], TimeSpan.Zero);
                        var id = fields[1];
                        sb.AppendLine($"    {(paramNumber > 0 ? "AND" : "WHERE")} (runs.created_at, runs.id) < (${++paramNumber}, ${++paramNumber})");
                        parameters.Add(new() { Value = createdAt, NpgsqlDbType = NpgsqlDbType.TimestampTz });
                        parameters.Add(new() { Value = id, NpgsqlDbType = NpgsqlDbType.Bigint });
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

            if (options.Since.HasValue)
            {
                sb.AppendLine($"    {(paramNumber > 0 ? "AND" : "WHERE")} runs.created_at > ${++paramNumber}");
                parameters.Add(new() { Value = options.Since.Value, NpgsqlDbType = NpgsqlDbType.TimestampTz });
            }

            if (options.Statuses?.Length > 0)
            {
                sb.AppendLine($"    {(paramNumber > 0 ? "AND" : "WHERE")} runs.status = ANY(${++paramNumber})");
                parameters.Add(new() { Value = options.Statuses, NpgsqlDbType = NpgsqlDbType.Array | NpgsqlDbType.Text });
            }

            if (options.OnlyResourcesCreated)
            {
                sb.AppendLine($"    {(paramNumber > 0 ? "AND" : "WHERE")} runs.resources_created = true");
            }

            sb.AppendLine("    ORDER BY created_at DESC, id DESC");
            sb.AppendLine($"    LIMIT ${++paramNumber}");
            parameters.Add(new() { Value = options.Limit + 1, NpgsqlDbType = NpgsqlDbType.Integer });
            sb.AppendLine(")");

            sb.AppendLine($"""
                SELECT
                    matches.run,
                    matches.final,
                    jsonb_object_agg(tag_keys.name, run_tags.value) FILTER (WHERE tag_keys.name IS NOT NULL) AS tags
                FROM matches
                LEFT JOIN run_tags
                    ON matches.id = run_tags.id
                    AND run_tags.created_at = matches.created_at
                LEFT JOIN tag_keys
                    ON tag_keys.id = run_tags.key
                GROUP BY matches.created_at, matches.id, matches.run, matches.final
                ORDER BY matches.created_at DESC, matches.id DESC
                """);

            await using var cmd = new NpgsqlCommand(sb.ToString(), conn);
            foreach (var parameter in parameters)
            {
                cmd.Parameters.Add(parameter);
            }

            await cmd.PrepareAsync(cancellationToken);
            await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);

            List<(Run run, bool final)> results = [];
            var hash = _xxHash3Pool.Get();
            try
            {
                while (await reader.ReadAsync(cancellationToken))
                {
                    var runJson = reader.GetString(0);
                    var final = reader.GetBoolean(1);
                    var run = JsonSerializer.Deserialize<Run>(runJson, _serializerOptions) ?? throw new InvalidOperationException("Failed to deserialize run.");
                    if (!reader.IsDBNull(2))
                    {
                        var tagsJson = reader.GetString(2);
                        run = run with { Tags = JsonSerializer.Deserialize<Dictionary<string, string>>(tagsJson, _serializerOptions) };
                    }

                    if (!final && _runAugmenter != null && results.Count < options.Limit)
                    {
                        run = await _runAugmenter.Value.AugmentRun(run, cancellationToken);
                    }

                    run.ComputeEtag(hash);

                    results.Add((run, final));
                }
            }
            finally
            {
                hash.Reset();
                _xxHash3Pool.Return(hash);
            }

            if (results.Count == options.Limit + 1)
            {
                results.RemoveAt(options.Limit);
                var (run, final) = results[^1];
                string newToken = Base32.ZBase32.Encode(Encoding.ASCII.GetBytes(JsonSerializer.Serialize(new[] { run.CreatedAt!.Value.UtcTicks, run.Id!.Value }, _serializerOptions)));
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

    public async Task<IList<(string bufferId, int? accountId)>> GetBufferStorageAccountIds(IList<string> ids, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var cmd = new NpgsqlCommand("""
                SELECT b.id, sa.id
                FROM unnest($1::text[]) AS b(id)
                LEFT JOIN buffers AS buf ON buf.id = b.id
                LEFT JOIN storage_accounts AS sa ON sa.id = buf.storage_account_id
                """, conn)
            {
                Parameters =
                {
                    new() { Value = ids.ToArray(), NpgsqlDbType = NpgsqlDbType.Array | NpgsqlDbType.Text }
                }
            };

            await cmd.PrepareAsync(cancellationToken);
            var results = new List<(string bufferId, int? accountId)>(ids.Count);
            await using var reader = await cmd.ExecuteReaderAsync(CommandBehavior.SequentialAccess, cancellationToken);
            while (await reader.ReadAsync(cancellationToken))
            {
                results.Add((reader.GetString(0), reader.IsDBNull(1) ? null : reader.GetInt32(1)));
            }

            return results;
        }, cancellationToken);
    }

    public async Task<Buffer?> GetBuffer(string id, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var conn = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var command = new NpgsqlCommand("""
                SELECT buffers.created_at, storage_accounts.location, tag_keys.name, buffer_tags.value
                FROM buffers
                INNER JOIN storage_accounts
                    on storage_accounts.id = buffers.storage_account_id
                LEFT JOIN buffer_tags
                    on buffers.id = buffer_tags.id
                    and buffer_tags.created_at = buffers.created_at
                LEFT JOIN tag_keys
                    on tag_keys.id = buffer_tags.key
                WHERE buffers.id = $1
                ORDER BY tag_keys.name
                """, conn)
            {
                Parameters =
                {
                    new() { Value = id, NpgsqlDbType = NpgsqlDbType.Text },
                }
            };

            OrderedDictionary<string, string>? tags = null;
            string location = "";
            DateTimeOffset createdAt = default;

            await command.PrepareAsync(cancellationToken);
            await using var reader = (await command.ExecuteReaderAsync(cancellationToken))!;
            bool any = false;

            while (await reader.ReadAsync(cancellationToken))
            {
                if (!any)
                {
                    any = true;
                    createdAt = reader.GetDateTime(0);
                    location = reader.GetString(1);
                }

                if (!reader.IsDBNull(2) && !reader.IsDBNull(3))
                {
                    var name = reader.GetString(2);
                    var value = reader.GetString(3);
                    (tags ??= []).Add(name, value);
                }
            }

            if (!any)
            {
                return null;
            }

            return new Buffer { Id = id, CreatedAt = createdAt, Location = location, Tags = tags, };
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
            commandText.AppendLine($"""
                WITH matches AS (
                    SELECT buffers.id, buffers.created_at, buffers.storage_account_id
                    FROM buffers
                """);

            int param = 2;

            if (tags?.Count > 0)
            {
                for (int x = 0; x < tags.Count; x++)
                {
                    commandText.AppendLine($"INNER JOIN buffer_tags AS t{x} ON buffers.created_at = t{x}.created_at and buffers.id = t{x}.id");
                }

                commandText.AppendLine("WHERE");

                int index = 0;
                foreach (var tag in tags)
                {
                    if (index > 0)
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

                        commandText.Append($"(buffers.created_at, buffers.id) < (${param}, ${param + 1})\n");
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
                    ORDER BY buffers.created_at DESC, buffers.id DESC
                    LIMIT $1
                )
                SELECT matches.id, matches.created_at, tag_keys.name, buffer_tags.value, storage_accounts.location
                FROM matches
                INNER JOIN storage_accounts ON matches.storage_account_id = storage_accounts.id
                LEFT JOIN buffer_tags
                    ON matches.id = buffer_tags.id AND matches.created_at = buffer_tags.created_at
                LEFT JOIN tag_keys ON buffer_tags.key = tag_keys.id
                ORDER BY matches.created_at DESC, matches.id DESC, tag_keys.name ASC
                """);

            command.CommandText = commandText.ToString();
            await command.PrepareAsync(cancellationToken);

            var results = new List<Buffer>();
            var currentTags = new OrderedDictionary<string, string>();
            var currentBuffer = new Buffer();
            var hash = _xxHash3Pool.Get();
            try
            {
                using var reader = (await command.ExecuteReaderAsync(cancellationToken))!;
                while (await reader.ReadAsync(cancellationToken))
                {
                    var id = reader.GetString(0);
                    var createdAt = reader.GetDateTime(1);
                    var location = reader.GetString(4);

                    if (currentBuffer.Id != id)
                    {
                        if (currentBuffer.Id != "")
                        {
                            currentBuffer = currentBuffer with { Tags = currentTags };
                            currentBuffer.ComputeEtag(hash);
                            results.Add(currentBuffer);
                        }

                        currentBuffer = currentBuffer with { Id = id, CreatedAt = createdAt, Location = location };
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
                    currentBuffer.ComputeEtag(hash);
                    results.Add(currentBuffer with { Tags = currentTags });
                }

                if (results.Count == limit + 1)
                {
                    results.RemoveAt(limit);
                    var last = results[^1];
                    string newToken = Base32.ZBase32.Encode(Encoding.ASCII.GetBytes(JsonSerializer.Serialize(new object[] { last.CreatedAt!.Value.UtcTicks, last.Id }, _serializerOptions)));
                    return (results, newToken);
                }

                return (results, null);
            }
            finally
            {
                hash.Reset();
                _xxHash3Pool.Return(hash);
            }
        }, cancellationToken);
    }

    public async Task<UpdateWithPreconditionResult<Buffer>> UpdateBufferTags(BufferUpdate bufferUpdate, string? eTagPrecondition, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync<UpdateWithPreconditionResult<Buffer>>(async cancellationToken =>
        {
            await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await connection.BeginTransactionAsync(cancellationToken);

            Buffer buffer = new() { Id = bufferUpdate.Id!, Tags = bufferUpdate.Tags };

            // Update and validate the buffer
            using var bufferCommand = new NpgsqlCommand
            {
                Connection = connection,
                Transaction = tx,
                CommandText = """
                    SELECT created_at, storage_accounts.location FROM buffers
                    INNER JOIN storage_accounts ON storage_accounts.id = buffers.storage_account_id
                    WHERE buffers.id = $1
                    FOR UPDATE
                    """,
                Parameters =
                    {
                        new() { Value = bufferUpdate.Id, NpgsqlDbType = NpgsqlDbType.Text },
                    }
            };

            await bufferCommand.PrepareAsync(cancellationToken);

            await using (var reader = await bufferCommand.ExecuteReaderAsync(cancellationToken))
            {
                bool any = false;
                while (await reader.ReadAsync(cancellationToken))
                {
                    any = true;
                    buffer = buffer with { CreatedAt = reader.GetDateTime(0), Location = reader.GetString(1) };
                    bufferUpdate = bufferUpdate with { CreatedAt = buffer.CreatedAt };
                }

                if (!any)
                {
                    return new UpdateWithPreconditionResult<Buffer>.NotFound();
                }
            }

            var hash = _xxHash3Pool.Get();
            try
            {
                var existingTags = await UpsertTagsCore(tx, bufferUpdate, true, eTagPrecondition, cancellationToken);
                if (!string.IsNullOrEmpty(eTagPrecondition))
                {
                    var existingBuffer = buffer with { Tags = existingTags };
                    if (!string.Equals(existingBuffer.ComputeEtag(hash), eTagPrecondition, StringComparison.Ordinal))
                    {
                        await tx.RollbackAsync(cancellationToken);
                        return new UpdateWithPreconditionResult<Buffer>.PreconditionFailed();
                    }
                }

                buffer.ComputeEtag(hash);
            }
            finally
            {
                hash.Reset();
                _xxHash3Pool.Return(hash);
            }

            await tx.CommitAsync(cancellationToken);
            return new UpdateWithPreconditionResult<Buffer>.Updated(buffer);
        }, cancellationToken);
    }

    private static async Task<IReadOnlyDictionary<string, string>?> UpsertTagsCore<TId>(NpgsqlTransaction tx, IResourceWithTags<TId> resourceToUpdate, bool update, string? eTagPrecondition, CancellationToken cancellationToken)
    {
        OrderedDictionary<string, string>? existingTags = null;
        var idNpgsqlDbType = typeof(TId) == typeof(string) ? NpgsqlDbType.Text : NpgsqlDbType.Bigint;
        var tagTableName = resourceToUpdate switch
        {
            BufferUpdate => "buffer_tags",
            RunUpdate => "run_tags",
            _ => throw new InvalidOperationException("Unsupported resource type.")
        };

        if (update)
        {
            if (string.IsNullOrEmpty(eTagPrecondition))
            {
                await using var deleteExistingCommand = new NpgsqlCommand(
                    $"""
                    DELETE FROM {tagTableName}
                    WHERE id = $1 and created_at = $2
                    """, tx.Connection, tx)
                {
                    Parameters =
                        {
                            new() { Value = resourceToUpdate.Id, NpgsqlDbType = idNpgsqlDbType },
                            new() { Value = resourceToUpdate.CreatedAt!.Value, NpgsqlDbType = NpgsqlDbType.TimestampTz }
                        },
                };

                await deleteExistingCommand.PrepareAsync(cancellationToken);
                await deleteExistingCommand.ExecuteNonQueryAsync(cancellationToken);
            }
            else
            {
                await using var readAndDeleteExistingCommand = new NpgsqlCommand(
                    $"""
                    WITH deleted AS (
                        DELETE FROM {tagTableName}
                        WHERE id = $1 and created_at = $2
                        RETURNING key, value
                    )
                    SELECT tag_keys.name, deleted.value FROM deleted
                    INNER JOIN tag_keys ON tag_keys.id = deleted.key
                    ORDER BY tag_keys.name;
                    """, tx.Connection, tx)
                {
                    Parameters =
                        {
                            new() { Value = resourceToUpdate.Id, NpgsqlDbType = idNpgsqlDbType },
                            new() { Value = resourceToUpdate.CreatedAt!.Value, NpgsqlDbType = NpgsqlDbType.TimestampTz }
                        },
                };

                await readAndDeleteExistingCommand.PrepareAsync(cancellationToken);

                await using var reader = await readAndDeleteExistingCommand.ExecuteReaderAsync(cancellationToken);
                while (await reader.ReadAsync(cancellationToken))
                {
                    var tagName = reader.GetFieldValue<string>(0);
                    var tagValue = reader.GetFieldValue<string>(1);
                    (existingTags ??= [])[tagName] = tagValue;
                }
            }
        }

        if (resourceToUpdate.Tags is { Count: > 0 })
        {
            var insertCommand = new NpgsqlCommand(
                $"""
                WITH temp_tags AS (
                    SELECT * FROM UNNEST($1::text[], $2::text[]) AS t(key, value)
                ),
                inserted_keys AS (
                    INSERT INTO tag_keys (name)
                    SELECT DISTINCT key FROM temp_tags
                    ON CONFLICT (name) DO NOTHING
                    RETURNING id, name
                ),
                all_keys AS (
                    SELECT id, name FROM inserted_keys
                    UNION
                    SELECT id, name FROM tag_keys WHERE name IN (SELECT key FROM temp_tags)
                )
                INSERT INTO {tagTableName} (id, created_at, key, value)
                SELECT $3, $4, all_keys.id, temp_tags.value
                FROM temp_tags
                JOIN all_keys ON all_keys.name = temp_tags.key;
                """, tx.Connection, tx)
            {
                Parameters =
                {
                    new() { Value = resourceToUpdate.Tags.Keys.ToArray(), NpgsqlDbType = NpgsqlDbType.Array | NpgsqlDbType.Text},
                    new() { Value = resourceToUpdate.Tags.Values.ToArray(), NpgsqlDbType = NpgsqlDbType.Array | NpgsqlDbType.Text },
                    new() { Value = resourceToUpdate.Id, NpgsqlDbType = idNpgsqlDbType },
                    new() { Value = resourceToUpdate.CreatedAt!.Value, NpgsqlDbType = NpgsqlDbType.TimestampTz },
                }
            };

            await insertCommand.PrepareAsync(cancellationToken);
            await insertCommand.ExecuteNonQueryAsync(cancellationToken);
        }

        return existingTags;
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
                (var run, _, _, _, _) = await GetRun(runId, cancellationToken) ?? throw new InvalidOperationException("Failed to get run with existing idempotency key.");
                return run;
            }
        }

        return await createRun(newRun, cancellationToken);
    }

    public async Task<Buffer> CreateBuffer(Buffer newBuffer, int storageAccountId, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var tx = await connection.BeginTransactionAsync(IsolationLevel.Serializable, cancellationToken);

            // Create the buffer DB entry
            using var insertCommand = new NpgsqlCommand
            {
                Connection = connection,
                Transaction = tx,
                CommandText = """
                        INSERT INTO buffers (id, created_at, storage_account_id)
                        VALUES ($1, now() AT TIME ZONE 'utc', $2)
                        RETURNING created_at
                        """,
                Parameters =
                    {
                        new() { Value = newBuffer.Id, NpgsqlDbType = NpgsqlDbType.Text },
                        new() { Value = storageAccountId, NpgsqlDbType = NpgsqlDbType.Integer },
                    }
            };

            await insertCommand.PrepareAsync(cancellationToken);

            await using (var reader = await insertCommand.ExecuteReaderAsync(cancellationToken))
            {
                await reader.ReadAsync(cancellationToken);
                newBuffer = newBuffer with { CreatedAt = reader.GetDateTime(0) };
            }

            await UpsertTagsCore(tx, new BufferUpdate { Id = newBuffer.Id, CreatedAt = newBuffer.CreatedAt, Tags = newBuffer.Tags }, false, null, cancellationToken);

            await tx.CommitAsync(cancellationToken);
            return newBuffer;
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

                if (!await connection.WaitAsync(TimeSpan.FromSeconds(10), cancellationToken))
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
                SELECT run, modified_at, tags_version from runs
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
                var tagsVersion = reader.GetInt32(2);
                var run = JsonSerializer.Deserialize<Run>(runString, _serializerOptions);
                await processRunUpdates(new ObservedRunState(run!, modifiedAt) { TagsVersion = tagsVersion }, cancellationToken);
            }
        }

        await ProcessPayloads();

        while (true)
        {
            if (await connection.WaitAsync(TimeSpan.FromSeconds(10), cancellationToken))
            {
                await ProcessPayloads();
            }
            else
            {
                // Ensure the connection is still alive
                using var cmd = new NpgsqlCommand("SELECT 1", connection);
                await cmd.PrepareAsync(cancellationToken);
                await cmd.ExecuteScalarAsync(cancellationToken);

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

    public async Task<Dictionary<int, string>> UpsertStorageAccounts(IList<StorageAccount> storageAccounts, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);

            var results = new Dictionary<int, string>();
            var names = storageAccounts.Select(sa => sa.Name.ToLowerInvariant()).ToArray();
            var endpoints = storageAccounts.Select(sa => sa.Endpoint).ToArray();
            var locations = storageAccounts.Select(sa => sa.Location).ToArray();

            var commandText = """
                WITH inserted AS (
                    INSERT INTO storage_accounts (name, endpoint, location)
                    SELECT unnest($1::text[]), unnest($2::text[]), unnest($3::text[])
                    ON CONFLICT (name) DO NOTHING
                    RETURNING id, name
                ),
                existing AS (
                    SELECT id, name FROM storage_accounts WHERE name = ANY($1)
                )
                SELECT id, name FROM inserted
                UNION ALL
                SELECT id, name FROM existing
                """;

            using var upsertCommand = new NpgsqlCommand(commandText, connection);
            upsertCommand.Parameters.Add(new() { Value = names, NpgsqlDbType = NpgsqlDbType.Array | NpgsqlDbType.Text });
            upsertCommand.Parameters.Add(new() { Value = endpoints, NpgsqlDbType = NpgsqlDbType.Array | NpgsqlDbType.Text });
            upsertCommand.Parameters.Add(new() { Value = locations, NpgsqlDbType = NpgsqlDbType.Array | NpgsqlDbType.Text });

            await upsertCommand.PrepareAsync(cancellationToken);
            await using var reader = await upsertCommand.ExecuteReaderAsync(cancellationToken);
            while (await reader.ReadAsync(cancellationToken))
            {
                results.Add(reader.GetInt32(0), reader.GetString(1));
            }

            return results;
        }, cancellationToken);
    }

    public async Task<string> GetStorageAccountEndpoint(int id, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var command = new NpgsqlCommand("""
                SELECT endpoint
                FROM storage_accounts
                WHERE id = $1
                """, connection)
            {
                Parameters = { new() { Value = id, NpgsqlDbType = NpgsqlDbType.Integer } }
            };

            await command.PrepareAsync(cancellationToken);
            return (string?)await command.ExecuteScalarAsync(cancellationToken) ?? throw new ValidationException($"Storage account with id '{id}' not found.");
        }, cancellationToken);
    }

    public async Task<IList<(int id, string endpoint)>> GetStorageAccountsByLocation(string location, CancellationToken cancellationToken)
    {
        return await _resiliencePipeline.ExecuteAsync(async cancellationToken =>
        {
            await using var connection = await _dataSource.OpenConnectionAsync(cancellationToken);
            await using var command = new NpgsqlCommand("""
                SELECT id, endpoint
                FROM storage_accounts
                WHERE location = $1
                """, connection)
            {
                Parameters = { new() { Value = location.ToLowerInvariant(), NpgsqlDbType = NpgsqlDbType.Text } }
            };

            await command.PrepareAsync(cancellationToken);
            await using var reader = await command.ExecuteReaderAsync(cancellationToken);
            var results = new List<(int id, string endpoint)>();
            while (await reader.ReadAsync(cancellationToken))
            {
                results.Add((reader.GetInt32(0), reader.GetString(1)));
            }

            return results;
        }, cancellationToken);
    }

    private NpgsqlParameter CreateJsonbParameter(object value)
    {
        var updatedValue = value switch
        {
            Run run => run with { Tags = null, ETag = null },
            _ => value,
        };

        return new() { Value = JsonSerializer.Serialize(updatedValue, _serializerOptions), NpgsqlDbType = NpgsqlDbType.Jsonb };
    }
}

[Union]
public partial record UpdateWithPreconditionResult<T>
{
    public partial record Updated(T Value);
    public partial record NotFound();
    public partial record PreconditionFailed();
}

[Flags]
public enum GetRunOptions
{
    None = 0,
    SkipAugmentation = 1,
    SkipTags = 2,
}

public record GetRunsOptions(int Limit)
{
    public bool OnlyResourcesCreated { get; init; }
    public DateTimeOffset? Since { get; init; }
    public string[]? Statuses { get; init; }
    public IDictionary<string, string>? Tags { get; init; }
    public string? ContinuationToken { get; init; }
}
