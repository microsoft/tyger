using k8s.Models;
using Microsoft.OpenApi.Any;
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
            c.SupportNonNullableReferenceTypes();

            c.UseOneOfForPolymorphism();
            c.UseAllOfForInheritance();

            c.MapType<CodespecKind>(() => new OpenApiSchema { Enum = new List<IOpenApiAny> { new OpenApiString("job"), new OpenApiString("worker") } });
            c.SelectDiscriminatorNameUsing(type => type == typeof(JobCodespec) ? "kind" : null);
            c.SelectDiscriminatorValueUsing(type => type switch
            {
                _ when type == typeof(JobCodespec) => "job",
                _ when type == typeof(WorkerCodespec) => "worker",
                _ => null
            });

            c.MapType<ResourceQuantity>(() => new OpenApiSchema { Type = "string" });
            c.MapType<CommittedCodespecRef>(() => new OpenApiSchema { Type = "string" });

            c.SelectSubTypesUsing(type =>
            {
                if (type == typeof(ICodespecRef))
                {
                    return new[] { typeof(CommittedCodespecRef), typeof(Codespec) };
                }

                if (type == typeof(Codespec))
                {
                    return new[] { typeof(JobCodespec), typeof(WorkerCodespec) };
                }

                if (type == typeof(RunCodeTarget))
                {
                    return new[] { typeof(JobRunCodeTarget) };
                }

                return Array.Empty<Type>();

            });
            c.SchemaFilter<ModelBaseSchemaFilter>();
            c.OperationFilter<ParameterStyleWorkaroundFilter>();

            var filePath = Path.Combine(AppContext.BaseDirectory, "tyger.server.xml");
            c.IncludeXmlComments(filePath);
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

// Workaround for https://github.com/microsoft/OpenAPI.NET/issues/1132
internal class ParameterStyleWorkaroundFilter : IOperationFilter
{
    public void Apply(OpenApiOperation operation, OperationFilterContext context)
    {
        if (operation.Parameters != null)
        {
            foreach (var parameter in operation.Parameters)
            {
                parameter.Style = null;
            }
        }
    }
}
