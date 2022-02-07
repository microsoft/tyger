using Tyger.Server.Kubernetes;
using Tyger.Server.Model;

namespace Tyger.Server.Runs;

public static class Runs
{
    public static void MapRuns(this WebApplication app)
    {
        app.MapPost("/v1/runs", async (IKubernetesManager k8sManager, HttpContext context) =>
        {
            Run run = await context.Request.ReadAndValidateJson<Run>(context.RequestAborted);
            Run createdRun = await k8sManager.CreateRun(run, context.RequestAborted);
            return Results.Created($"/v1/runs/{createdRun.Id}", createdRun);
        })
        .Produces<Run>(StatusCodes.Status201Created)
        .Produces<ErrorBody>(StatusCodes.Status400BadRequest);

        app.MapGet("/v1/runs/{runId}", async (string runId, IKubernetesManager k8sManager, CancellationToken cancellationToken) =>
        {
            Run? run = await k8sManager.GetRun(runId, cancellationToken);
            if (run == null)
            {
                return Responses.NotFound();
            }

            return Results.Ok(run);
        })
        .Produces<Run>(StatusCodes.Status200OK)
        .Produces<ErrorBody>();
    }
}
