// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Text.Json;
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.WebUtilities;
using Microsoft.Extensions.Primitives;
using Tyger.Common.Api;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Json;
using Tyger.ControlPlane.Logging;
using Tyger.ControlPlane.Model;
using Tyger.ControlPlane.OpenApi;

namespace Tyger.ControlPlane.Runs;

public static class Runs
{
    private static readonly ReadOnlyMemory<byte> s_newline = new("\n"u8.ToArray());

    public static void AddRuns(this IHostApplicationBuilder builder)
    {
        builder.Services.AddHostedService<RunIndexPruner>();
    }

    public static void MapRuns(this RouteGroupBuilder root)
    {
        var runs = root.MapGroup("/runs");

        runs.MapPost("/", async (IRunCreator runCreator, Repository repository, HttpContext context) =>
        {
            var newRun = (await context.Request.ReadAndValidateJson<Run>(context.RequestAborted)).WithoutSystemProperties();
            if (newRun.Kind == RunKind.System)
            {
                throw new ValidationException("System runs cannot be created directly");
            }

            Tags.Validate(newRun.Tags);

            Run createdRun;
            if (context.Request.Headers.TryGetValue("Idempotency-Key", out var idempotencyKey))
            {
                createdRun = await repository.CreateRunWithIdempotencyKeyGuard(newRun, idempotencyKey.ToString(), async (run, ct) => await runCreator.CreateRun(run, idempotencyKey, ct), context.RequestAborted);
            }
            else
            {
                createdRun = await runCreator.CreateRun(newRun, idempotencyKey, context.RequestAborted);
            }

            return Results.Created($"/runs/{createdRun.Id}", createdRun);
        })
        .Accepts<Run>("application/json")
        .Produces<Run>(StatusCodes.Status201Created)
        .Produces<ErrorBody>(StatusCodes.Status400BadRequest);

        runs.MapGet("/", async (IRunReader runReader, int? limit, DateTimeOffset? since, [FromQuery(Name = "status")] string[]? statuses, [FromQuery(Name = "_ct")] string? continuationToken, HttpContext context) =>
        {
            limit = limit is null ? 20 : Math.Min(limit.Value, 2000);

            if (statuses != null)
            {
                for (int i = 0; i < statuses.Length; i++)
                {
                    if (Enum.TryParse<RunStatus>(statuses[i], true, out var statusEnum))
                    {
                        statuses[i] = statusEnum.ToString();
                    }
                    else
                    {
                        throw new ValidationException($"Invalid status: {statuses[i]}");
                    }
                }
            }

            var options = new GetRunsOptions(limit.Value)
            {
                ContinuationToken = continuationToken,
                Since = since,
                Tags = context.GetTagQueryParameters(),
                Statuses = statuses,
            };

            (var items, var nextContinuationToken) = await runReader.ListRuns(options, context.RequestAborted);

            string? nextLink;
            if (nextContinuationToken is null)
            {
                nextLink = null;
            }
            else if (context.Request.QueryString.HasValue)
            {
                var qd = QueryHelpers.ParseQuery(context.Request.QueryString.Value);
                qd["_ct"] = new StringValues(nextContinuationToken);
                nextLink = QueryHelpers.AddQueryString(context.Request.Path, qd);
            }
            else
            {
                nextLink = QueryHelpers.AddQueryString(context.Request.Path, "_ct", nextContinuationToken);
            }

            return new RunPage(items, nextLink == null ? null : new Uri(nextLink));
        })
        .WithTagsQueryParameters();

        runs.MapGet("/counts", async (IRunReader runReader, DateTimeOffset? since, HttpContext context) =>
        {
            var runs = await runReader.GetRunCounts(since, context.GetTagQueryParameters(), context.RequestAborted);
            return Results.Ok(runs);
        })
        .WithTagsQueryParameters()
        .Produces<IDictionary<RunStatus, long>>(StatusCodes.Status200OK);

        runs.MapGet("/{runId}", async (
            string runId,
            bool? watch,
            IRunReader runReader,
            HttpContext context,
            JsonSerializerOptions serializerOptions) =>
        {
            if (!long.TryParse(runId, out var parsedRunId))
            {
                return Responses.NotFound();
            }

            if (!watch.GetValueOrDefault())
            {
                if (await runReader.GetRun(parsedRunId, context.RequestAborted) is not var (run, _, _, _, _))
                {
                    return Responses.NotFound();
                }

                return Results.Ok(run);
            }

            bool any = false;
            await foreach (var runSnapshot in runReader.WatchRun(parsedRunId, context.RequestAborted))
            {
                if (!any)
                {
                    any = true;
                    context.Response.StatusCode = StatusCodes.Status200OK;
                    context.Response.ContentType = "application/json; charset=utf-8";
                }

                await JsonSerializer.SerializeAsync(context.Response.Body, runSnapshot, serializerOptions, context.RequestAborted);
                await context.Response.Body.WriteAsync(s_newline, context.RequestAborted);
                await context.Response.Body.FlushAsync(context.RequestAborted);
            }

            if (!any)
            {
                return Responses.NotFound();
            }

            return Results.Empty;
        })
        .Produces<Run>(StatusCodes.Status200OK)
        .Produces<ErrorBody>(StatusCodes.Status404NotFound);

        runs.MapGet("/{runId}/logs", async (
            string runId,
            ILogSource logSource,
            bool? timestamps,
            int? tailLines,
            DateTimeOffset? since,
            bool? follow,
            HttpContext context) =>
        {
            var options = new GetLogsOptions
            {
                IncludeTimestamps = timestamps.GetValueOrDefault(),
                TailLines = tailLines,
                Since = since,
                Follow = follow.GetValueOrDefault(),
            };

            if (!long.TryParse(runId, out var parsedRunId) ||
                await logSource.GetLogs(parsedRunId, options, context.RequestAborted) is not Pipeline pipeline)
            {
                context.Response.StatusCode = StatusCodes.Status404NotFound;
                return;
            }

            if (options.Follow)
            {
                // When following, there may be a long delay before the first log line is written.
                // Force a body flush here to return the headers to the client as soon as possible.
                await context.Response.BodyWriter.FlushAsync(context.RequestAborted);
            }

            await pipeline.Process(context.Response.BodyWriter, context.RequestAborted);
        })
        .Produces(StatusCodes.Status200OK, null, "text/plain")
        .Produces<ErrorBody>(StatusCodes.Status404NotFound);

        runs.MapPost("/{runId}/cancel", async (
            string runId,
            [FromServices] IRunUpdater runUpdater,
            HttpContext context,
            JsonSerializerOptions serializerOptions) =>
        {
            if (!long.TryParse(runId, out var parsedRunId))
            {
                return Responses.NotFound();
            }

            if (await runUpdater.CancelRun(parsedRunId, context.RequestAborted) is not Run run)
            {
                return Responses.NotFound();
            }

            return Results.Accepted(value: run);
        })
        .Produces<Run>(StatusCodes.Status202Accepted)
        .Produces<ErrorBody>(StatusCodes.Status404NotFound);

        runs.MapPut("/{runId}", async (IRunUpdater updater, string runId, HttpContext context, CancellationToken cancellationToken) =>
        {
            if (!long.TryParse(runId, out var parsedRunId))
            {
                return Results.NotFound();
            }

            var runUpdate = await context.Request.ReadAndValidateJson<RunUpdate>(context.RequestAborted, allowEmpty: true);
            if (runUpdate.Id != null && runUpdate.Id != parsedRunId)
            {
                return Results.BadRequest("The run ID in the URL does not match the run ID in the request body.");
            }

            runUpdate = runUpdate with { Id = parsedRunId };

            string eTagPrecondition = context.Request.Headers.IfMatch.FirstOrDefault() ?? "";
            if (eTagPrecondition == "*") // if-match: * matches everything
            {
                eTagPrecondition = "";
            }

            var result = await updater.UpdateRunTags(runUpdate, eTagPrecondition, cancellationToken);

            return result.Match(
                updated: updated =>
                {
                    context.Response.Headers.ETag = updated.Value.ETag;
                    return Results.Ok(updated.Value);
                },
                notFound: _ => Results.NotFound(),
                preconditionFailed: _ => Results.StatusCode(StatusCodes.Status412PreconditionFailed));
        })
        .WithName("setRunTags")
        .Accepts<RunUpdate>("application/json")
        .Produces<Run>(StatusCodes.Status200OK)
        .Produces<ErrorBody>(StatusCodes.Status404NotFound)
        .Produces<ErrorBody>(StatusCodes.Status412PreconditionFailed);

        // this endpoint is for testing purposes only, to force the background pod sweep
        runs.MapPost("/_sweep", async (IRunSweeper? runSweeper, CancellationToken cancellationToken) =>
        {
            if (runSweeper is not null)
            {
                await runSweeper.SweepRuns(cancellationToken);
            }
        }).ExcludeFromDescription();
    }
}

public interface IRunCreator
{
    Task<Run> CreateRun(Run run, string? idempotencyKey, CancellationToken cancellationToken);
}

public interface IRunReader
{
    Task<IDictionary<RunStatus, long>> GetRunCounts(DateTimeOffset? since, Dictionary<string, string>? tags, CancellationToken cancellationToken);
    Task<(IReadOnlyList<Run>, string? nextContinuationToken)> ListRuns(GetRunsOptions options, CancellationToken cancellationToken);
    Task<(Run run, DateTimeOffset? modifiedAt, DateTimeOffset? logsArchivedAt, bool final, int tagsVersion)?> GetRun(long id, CancellationToken cancellationToken);
    IAsyncEnumerable<Run> WatchRun(long id, CancellationToken cancellationToken);
}

public interface IRunUpdater
{
    Task<Run?> CancelRun(long id, CancellationToken cancellationToken);
    Task<UpdateWithPreconditionResult<Run>> UpdateRunTags(RunUpdate runUpdate, string? eTagPrecondition, CancellationToken cancellationToken);
}

public interface IRunSweeper
{
    Task SweepRuns(CancellationToken cancellationToken);
}
