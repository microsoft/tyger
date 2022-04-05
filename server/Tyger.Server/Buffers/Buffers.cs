using System.ComponentModel.DataAnnotations;
using Tyger.Server.Model;
using Buffer = Tyger.Server.Model.Buffer;

namespace Tyger.Server.Buffers;

public static class Buffers
{
    public static void AddBuffers(this IServiceCollection services)
    {
        services.AddOptions<BlobStorageOptions>().BindConfiguration("blobStorage").ValidateDataAnnotations().ValidateOnStart();
        services.AddSingleton<BufferManager>();
        services.AddHealthChecks().AddCheck<BufferManager>("buffers");
    }

    public static void MapBuffers(this WebApplication app)
    {
        app.MapPost("/v1/buffers", async (BufferManager manager, CancellationToken cancellationToken) =>
            {
                var buffer = await manager.CreateBuffer(cancellationToken);
                return Results.CreatedAtRoute("getBufferById", new { buffer.Id }, buffer);
            })
            .WithName("createBuffer")
            .Produces<Buffer>(StatusCodes.Status201Created);

        app.MapGet("/v1/buffers/{id}", async (BufferManager manager, string id, CancellationToken cancellationToken) =>
            {
                var buffer = await manager.GetBufferById(id, cancellationToken);
                if (buffer != null)
                {
                    return Results.Ok(buffer);
                }

                return Results.NotFound();
            })
            .WithName("getBufferById")
            .Produces<Buffer>(StatusCodes.Status200OK)
            .Produces<ErrorBody>(StatusCodes.Status404NotFound);

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

public class BlobStorageOptions
{
    [Required]
    public string ConnectionString { get; init; } = "";
}
