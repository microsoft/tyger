using System.ComponentModel.DataAnnotations;
using Tyger.Server.Json;
using Tyger.Server.Model;
using Buffer = Tyger.Server.Model.Buffer;

namespace Tyger.Server.Buffers;

public static class Buffers
{
    public static void AddBuffers(this IServiceCollection services)
    {
        services.AddOptions<BufferOptions>().BindConfiguration("buffers").ValidateDataAnnotations().ValidateOnStart();
        services.AddScoped<BufferManager>();

        services.AddHealthChecks().AddCheck<BufferManager>("buffers");
    }

    public static void MapBuffers(this WebApplication app)
    {
        app.MapPost("/v1/buffers", async (BufferManager manager, HttpContext context, CancellationToken cancellationToken) =>
            {
                var newBuffer = await context.Request.ReadAndValidateJson<Buffer>(context.RequestAborted);
                var buffer = await manager.CreateBuffer(newBuffer, cancellationToken);
                context.Response.Headers.ETag = buffer.ETag;
                return Results.CreatedAtRoute("getBufferById", new { buffer.Id }, buffer);
            })
            .Accepts<Buffer>("application/json")
            .WithName("createBuffer")
            .Produces<Buffer>(StatusCodes.Status201Created);

        app.MapGet("/v1/buffers/{id}", async (BufferManager manager, HttpContext context, string id, CancellationToken cancellationToken) =>
            {
                var buffer = await manager.GetBufferById(id, cancellationToken);
                if (buffer != null)
                {
                    context.Response.Headers.ETag = buffer.ETag;
                    return Results.Ok(buffer);
                }

                return Results.NotFound();
            })
            .WithName("getBufferById")
            .Produces<Buffer>(StatusCodes.Status200OK)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound);

        app.MapPut("/v1/buffers/{id}/tags", async (BufferManager manager, HttpContext context, string id, CancellationToken cancellationToken) =>
            {
                string eTag = context.Request.Headers.IfMatch.FirstOrDefault() ?? "";
                if (eTag == "*") // if-match: * matches everything
                {
                    eTag = "";
                }

                var newTags = await context.Request.ReadAndValidateJson<IDictionary<string, string>>(context.RequestAborted, allowEmpty: true);
                newTags = Normalizer.NormalizeEmptyToNull(newTags);

                var buffer = await manager.UpdateBufferById(id, eTag, newTags, cancellationToken);

                if (buffer != null)
                {
                    context.Response.Headers.ETag = buffer.ETag;
                    return Results.Ok(buffer);
                }
                else if (eTag != "")
                {
                    buffer = await manager.GetBufferById(id, cancellationToken);
                    if (buffer != null)
                    {
                        return Results.StatusCode(StatusCodes.Status412PreconditionFailed);
                    }
                }

                return Results.NotFound();
            })
            .WithName("setBufferTags")
            .Accepts<IDictionary<string, string>>("application/json")
            .Produces<Buffer>(StatusCodes.Status200OK)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound)
            .Produces<ErrorBody>(StatusCodes.Status412PreconditionFailed);

        app.MapPost("/v1/buffers/{id}/access", async (BufferManager manager, string id, bool? writeable, CancellationToken cancellationToken) =>
            {
                var bufferAccess = await manager.CreateBufferAccessString(id, writeable == true, cancellationToken);
                if (bufferAccess is null)
                {
                    return Responses.NotFound();
                }

                return Results.Json(bufferAccess, statusCode: StatusCodes.Status201Created);
            })
            .WithName("getBufferAccessString")
            .Produces<BufferAccess>(StatusCodes.Status201Created)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound);
    }
}

public class BufferOptions
{
    [Required]
    public required string ConnectionString { get; init; }

    [Required]
    public required string BufferSidecarImage { get; init; }
}
