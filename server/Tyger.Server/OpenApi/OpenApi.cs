using k8s.Models;
using Microsoft.OpenApi.Models;
using Swashbuckle.AspNetCore.SwaggerGen;
using Tyger.Server.Model;

namespace Tyger.Server.OpenApi;

public static class OpenApi
{
    public static void AddOpenApi(this IServiceCollection services)
    {
        services.AddEndpointsApiExplorer();
        services.AddSwaggerGen(c =>
        {
            c.MapType<ResourceQuantity>(() => new OpenApiSchema { Type = "string" });
            c.SchemaFilter<ModelBaseSchemaFilter>();
        });
    }

    public static void UseOpenApi(this WebApplication app)
    {
        app.UseSwagger();
        app.UseSwaggerUI();
    }
}

internal class ModelBaseSchemaFilter : ISchemaFilter
{
    public void Apply(OpenApiSchema schema, SchemaFilterContext context)
    {
        if (context.Type.IsSubclassOf(typeof(ModelBase)))
        {
            // The OpenApi schema for this type will by by default allow additional
            // properties because of the [JsonExtensionData] member. But that is only
            // there to fail when unrecognized fields are encountered during deserialization.
            schema.AdditionalPropertiesAllowed = false;
        }
    }
}
