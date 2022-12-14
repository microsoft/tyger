using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.WebUtilities;
using Microsoft.Extensions.Primitives;
using Tyger.Server.Kubernetes;
using Tyger.Server.Logging;
using Tyger.Server.Model;

namespace Tyger.Server.Runs;

public static class Runs
{
    public static void MapRuns(this WebApplication app)
    {
        app.MapPost("/v1/runs", async (RunCreator runCreator, HttpContext context) =>
        {
            var run = await context.Request.ReadAndValidateJson<NewRun>(context.RequestAborted);
            Run createdRun = await runCreator.CreateRun(run, context.RequestAborted);
            return Results.Created($"/v1/runs/{createdRun.Id}", createdRun);
        })
        .Produces<Run>(StatusCodes.Status201Created)
        .Produces<ErrorBody>(StatusCodes.Status400BadRequest);

        app.MapGet("/v1/runs", async (RunReader runReader, int? limit, DateTimeOffset? since, [FromQuery(Name = "_ct")] string? continuationToken, HttpContext context) =>
        {
            limit = limit is null ? 20 : Math.Min(limit.Value, 200);
            (var items, var nextContinuationToken) = await runReader.ListRuns(limit.Value, since, continuationToken, context.RequestAborted);

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
        });

        app.MapGet("/v1/runs/{runId}", async (string runId, RunReader runReader, CancellationToken cancellationToken) =>
        {
            if (!long.TryParse(runId, out var parsedRunId) || await runReader.GetRun(parsedRunId, cancellationToken) is not Run run)
            {
                return Responses.NotFound();
            }

            return Results.Ok(run);
        })
        .Produces<Run>(StatusCodes.Status200OK)
        .Produces<ErrorBody>();

        app.MapGet("/v1/runs/{runId}/logs", async (
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
        .Produces<Run>(StatusCodes.Status200OK)
        .Produces<string>();

        // this endpoint is for testing purposes only, to force the background pod sweep
        app.MapPost("/v1/runs/_sweep", async (RunSweeper runSweeper, CancellationToken cancellationToken) =>
        {
            await runSweeper.SweepRuns(cancellationToken);
        }).ExcludeFromDescription();
    }
}
