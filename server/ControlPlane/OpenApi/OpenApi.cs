// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Reflection;
using System.Text.Json.Nodes;
using Asp.Versioning.ApiExplorer;
using k8s.Models;
using Microsoft.Extensions.Options;
using Microsoft.OpenApi;
using Swashbuckle.AspNetCore.SwaggerGen;
using Tyger.Common.Versioning;
using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.OpenApi;

public static class OpenApi
{
    public static void AddOpenApi(this IHostApplicationBuilder builder)
    {
        builder.Services.AddEndpointsApiExplorer();

        builder.Services.AddTransient<IConfigureOptions<SwaggerGenOptions>, ConfigureSwaggerOptions>();

        builder.Services.AddSwaggerGen(c =>
        {
            c.SupportNonNullableReferenceTypes();

            c.UseOneOfForPolymorphism();
            c.UseAllOfForInheritance();

            c.MapType<CodespecKind>(() => new OpenApiSchema { Enum = [JsonValue.Create("job"), JsonValue.Create("worker")] });
            c.SelectDiscriminatorNameUsing(type => type == typeof(JobCodespec) ? "kind" : null);
            c.SelectDiscriminatorValueUsing(type => type switch
            {
                _ when type == typeof(JobCodespec) => "job",
                _ when type == typeof(WorkerCodespec) => "worker",
                _ => null
            });

            c.MapType<RunKind>(() => new OpenApiSchema { Enum = [JsonValue.Create("user"), JsonValue.Create("system")] });

            c.MapType<ResourceQuantity>(() => new OpenApiSchema { Type = JsonSchemaType.String });
            c.MapType<CommittedCodespecRef>(() => new OpenApiSchema { Type = JsonSchemaType.String });
            c.MapType<RunStatus>(() => new OpenApiSchema
            {
                Type = JsonSchemaType.String,
                Enum = [.. typeof(RunStatus).GetEnumNames().Select(value => (JsonNode)JsonValue.Create(value))]
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

            c.OperationFilter<ApiVersionParameterFilter>();
            c.OperationFilter<TagsQueryParameterOperationFilter>();

            var filePath = Path.Combine(AppContext.BaseDirectory, "tyger-server.xml");
            c.IncludeXmlComments(filePath);
        });
    }

    public static void UseOpenApi(this WebApplication app)
    {
        app.UseSwagger();
    }
}

internal sealed class ConfigureSwaggerOptions : IConfigureOptions<SwaggerGenOptions>
{
    private readonly IApiVersionDescriptionProvider _provider;

    public ConfigureSwaggerOptions(IApiVersionDescriptionProvider provider) =>
      _provider = provider;

    public void Configure(SwaggerGenOptions options)
    {
        foreach (var description in _provider.ApiVersionDescriptions)
        {
            options.SwaggerDoc(
              description.GroupName,
                new OpenApiInfo()
                {
                    Title = $"Tyger Server",
                    Version = description.ApiVersion.ToString(),
                });
        }
    }
}

/// <summary>
/// Removes the redundant `api-version` parameter from each operation's spec
/// </summary>
internal sealed class ApiVersionParameterFilter : IOperationFilter
{
    public void Apply(OpenApiOperation operation, OperationFilterContext context)
    {
        if (operation.Parameters == null)
        {
            return;
        }

        var versionParam = operation.Parameters.FirstOrDefault(p => p.Name?.Equals(ApiVersioning.QueryParameterKey, StringComparison.OrdinalIgnoreCase) == true);
        if (versionParam != null)
        {
            operation.Parameters.Remove(versionParam);
        }
    }
}

internal sealed class ModelBaseSchemaFilter : ISchemaFilter
{
    public void Apply(IOpenApiSchema schema, SchemaFilterContext context)
    {
        if (context.Type.IsSubclassOf(typeof(ModelBase)))
        {
            // The OpenApi schema for this type will by by default allow additional
            // properties because of the [JsonExtensionData] member. But that is only
            // there to fail when unrecognized fields are encountered during deserialization.
            if (schema is OpenApiSchema concreteSchema)
            {
                concreteSchema.AdditionalPropertiesAllowed = false;
                concreteSchema.AdditionalProperties = null;
            }
        }
    }
}

internal sealed class OpenApiExcludeSchemaFilter : ISchemaFilter
{
    public void Apply(IOpenApiSchema schema, SchemaFilterContext context)
    {
        if (schema.Properties == null)
        {
            return;
        }

        var excludedProperties = context.Type.GetProperties().Where(p => p.GetCustomAttribute<OpenApiExcludeAttribute>() != null);

        foreach (var excludedProperty in excludedProperties)
        {
            // casing does not match
            var keyToRemove = schema.Properties.Keys.SingleOrDefault(k => k.Equals(excludedProperty.Name, StringComparison.OrdinalIgnoreCase));
            if (keyToRemove != null)
            {
                schema.Properties.Remove(keyToRemove);
            }
        }
    }
}

public static class EndpointConventionBuilderExtensions
{
    public static TBuilder WithTagsQueryParameters<TBuilder>(this TBuilder builder) where TBuilder : IEndpointConventionBuilder
    {
        return builder.WithMetadata(new TagsQueryParameterMetadata());
    }
}

internal sealed class TagsQueryParameterMetadata;

/// <summary>
/// Adds the "tag" deep-object query parameter to operations marked with <see cref="TagsQueryParameterMetadata"/>.
/// </summary>
internal sealed class TagsQueryParameterOperationFilter : IOperationFilter
{
    public void Apply(OpenApiOperation operation, OperationFilterContext context)
    {
        var metadata = context.ApiDescription.ActionDescriptor.EndpointMetadata
            .OfType<TagsQueryParameterMetadata>()
            .FirstOrDefault();

        if (metadata == null)
        {
            return;
        }

        // Specify that the query parameter "tag" is a "deep object"
        // and can be used like this `tag[key1]=value1&tag[key2]=value2`.
        operation.Parameters ??= [];
        operation.Parameters.Add(new OpenApiParameter
        {
            Name = "tag",
            In = ParameterLocation.Query,
            Required = false,
            Schema = new OpenApiSchema
            {
                Type = JsonSchemaType.Object,
                AdditionalProperties = new OpenApiSchema
                {
                    Type = JsonSchemaType.String,
                },
            },
            Style = ParameterStyle.DeepObject,
            Explode = true,
        });

        // For some reason the text is changed to "OK" when we implement this,
        // so we need to set it back to "Success".
        // if (operation.Responses?.TryGetValue("200", out var okResponse) == true && okResponse != null)
        // {
        //     okResponse.Description = "Success";
        // }
    }
}
