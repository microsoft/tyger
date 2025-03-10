// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Reflection;
using k8s.Models;
using Microsoft.OpenApi.Any;
using Microsoft.OpenApi.Models;
using Swashbuckle.AspNetCore.SwaggerGen;
using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.OpenApi;

public static class OpenApi
{
    public static void AddOpenApi(this IHostApplicationBuilder builder)
    {
        builder.Services.AddEndpointsApiExplorer();
        builder.Services.AddSwaggerGen(c =>
        {
            c.SupportNonNullableReferenceTypes();

            c.UseOneOfForPolymorphism();
            c.UseAllOfForInheritance();

            c.MapType<CodespecKind>(() => new OpenApiSchema { Enum = [new OpenApiString("job"), new OpenApiString("worker")] });
            c.SelectDiscriminatorNameUsing(type => type == typeof(JobCodespec) ? "kind" : null);
            c.SelectDiscriminatorValueUsing(type => type switch
            {
                _ when type == typeof(JobCodespec) => "job",
                _ when type == typeof(WorkerCodespec) => "worker",
                _ => null
            });

            c.MapType<RunKind>(() => new OpenApiSchema { Enum = [new OpenApiString("user"), new OpenApiString("system")] });

            c.MapType<ResourceQuantity>(() => new OpenApiSchema { Type = "string" });
            c.MapType<CommittedCodespecRef>(() => new OpenApiSchema { Type = "string" });
            c.MapType<RunStatus>(() => new OpenApiSchema
            {
                Type = "string",
                Enum = [.. typeof(RunStatus).GetEnumNames().Select(value => new OpenApiString(value))]
            });

            c.SelectSubTypesUsing(type =>
            {
                if (type == typeof(ICodespecRef))
                {
                    return [typeof(CommittedCodespecRef), typeof(Codespec)];
                }

                if (type == typeof(Codespec))
                {
                    return [typeof(JobCodespec), typeof(WorkerCodespec)];
                }

                if (type == typeof(RunCodeTarget))
                {
                    return [typeof(JobRunCodeTarget)];
                }

                return [];

            });
            c.SchemaFilter<ModelBaseSchemaFilter>();
            c.SchemaFilter<OpenApiExcludeSchemaFilter>();

            var filePath = Path.Combine(AppContext.BaseDirectory, "tyger-server.xml");
            c.IncludeXmlComments(filePath);
        });
    }

    public static void UseOpenApi(this WebApplication app)
    {
        app.UseSwagger();
    }
}

internal sealed class ModelBaseSchemaFilter : ISchemaFilter
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

internal sealed class OpenApiExcludeSchemaFilter : ISchemaFilter
{
    public void Apply(OpenApiSchema schema, SchemaFilterContext context)
    {
        var excludedProperties = context.Type.GetProperties().Where(p => p.GetCustomAttribute<OpenApiExcludeAttribute>() != null);

        foreach (var excludedProperty in excludedProperties)
        {
            // casing does not match
            var keyToRemove = schema.Properties.Keys.Single(k => k.Equals(excludedProperty.Name, StringComparison.OrdinalIgnoreCase));
            schema.Properties.Remove(keyToRemove);
        }
    }
}

public static class EndpointConventionBuilderExtensions
{
    public static TBuilder WithTagsQueryParameters<TBuilder>(this TBuilder builder) where TBuilder : IEndpointConventionBuilder
    {
        return builder.WithOpenApi(c =>
        {
            // Specify that the query parameter "tag" is a "deep object"
            // and can be used like this `tag[key1]=value1&tag[key2]=value2`.
            c.Parameters.Add(new OpenApiParameter
            {
                Name = "tag",
                In = ParameterLocation.Query,
                Required = false,
                Schema = new OpenApiSchema
                {
                    Type = "object",
                    AdditionalProperties = new OpenApiSchema
                    {
                        Type = "string",
                    },
                },
                Style = ParameterStyle.DeepObject,
                Explode = true,
            });

            // For some reason the text is changed to "OK" when we implement this,
            // so we need to set it back to "Success".
            c.Responses["200"].Description = "Success";
            return c;
        });
    }
}
