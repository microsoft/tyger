using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.WebUtilities;
using Microsoft.Extensions.Primitives;
using Tyger.Server.Kubernetes;
using Tyger.Server.Model;

namespace Tyger.Server.Runs;

public static class Runs
{
    public static void MapRuns(this WebApplication app)
    {
        app.MapPost("/v1/runs", async (IKubernetesManager k8sManager, HttpContext context) =>
        {
            var run = await context.Request.ReadAndValidateJson<NewRun>(context.RequestAborted);
            Run createdRun = await k8sManager.CreateRun(run, context.RequestAborted);
            return Results.Created($"/v1/runs/{createdRun.Id}", createdRun);
        })
        .Produces<Run>(StatusCodes.Status201Created)
        .Produces<ErrorBody>(StatusCodes.Status400BadRequest);

        app.MapGet("/v1/runs", async (IKubernetesManager k8sManager, int? limit, DateTimeOffset? since, [FromQuery(Name = "_ct")] string? continuationToken, HttpContext context) =>
        {
            limit = limit is null ? 20 : Math.Min(limit.Value, 200);
            (var items, var nextContinuationToken) = await k8sManager.GetRuns(limit.Value, since, continuationToken, context.RequestAborted);

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

        app.MapGet("/v1/runs/{runId}", async (string runId, IKubernetesManager k8sManager, CancellationToken cancellationToken) =>
        {
            if (!long.TryParse(runId, out var parsedRunId) || await k8sManager.GetRun(parsedRunId, cancellationToken) is not Run run)
            {
                return Responses.NotFound();
            }

            return Results.Ok(run);
        })
        .Produces<Run>(StatusCodes.Status200OK)
        .Produces<ErrorBody>();
    }
}
