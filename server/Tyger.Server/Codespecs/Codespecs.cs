using Tyger.Server.Database;
using Tyger.Server.Model;

namespace Tyger.Server.Codespecs;

public static class Codespecs
{
    public static void MapCodespecs(this WebApplication app)
    {
        app.MapPut("/v1/codespecs/{name}", async (string name, IRepository repository, HttpContext context) =>
        {
            var newCodespec = await context.Request.ReadAndValidateJson<NewCodespec>(context.RequestAborted);

            int version = await repository.UpsertCodespec(name, newCodespec!, context.RequestAborted);
            context.Response.Headers.Location = $"/v1/codespecs/{name}/{version}";
            var codespec = new Codespec(newCodespec, name, version);
            return Results.Json(codespec, statusCode: version == 1 ? StatusCodes.Status201Created : StatusCodes.Status200OK);
        })
        .Produces<Codespec>(StatusCodes.Status200OK)
        .Produces<Codespec>(StatusCodes.Status201Created)
        .Produces<ErrorBody>(StatusCodes.Status400BadRequest);

        app.MapGet("/v1/codespecs/{name}", async (string name, IRepository repository, HttpContext context) =>
        {
            Codespec? codespec = await repository.GetLatestCodespec(name, context.RequestAborted);
            if (codespec == null)
            {
                return Responses.NotFound();
            }

            context.Response.Headers.Location = $"/v1/codespecs/{name}/{codespec.Version}";
            return Results.Ok(codespec);
        })
        .Produces<Codespec>();

        app.MapGet("/v1/codespecs/{name}/versions/{version}", async (string name, string version, IRepository repository, CancellationToken cancellationToken) =>
        {
            if (!int.TryParse(version, out var versionInt))
            {
                return Responses.NotFound();
            }

            var codespec = await repository.GetCodespecAtVersion(name, versionInt, cancellationToken);
            if (codespec == null)
            {
                return Responses.NotFound();
            }

            return Results.Ok(codespec);
        })
        .Produces<Codespec>();
    }
}
