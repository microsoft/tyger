// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Reflection;
using System.Text.Json;
using Asp.Versioning.ApiExplorer;
using k8s.Models;
using Microsoft.AspNetCore.Mvc.ApiExplorer;
using Microsoft.AspNetCore.Mvc.ModelBinding;
using Microsoft.Extensions.Options;
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

        builder.Services.AddTransient<IConfigureOptions<SwaggerGenOptions>, ConfigureSwaggerOptions>();

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

            // Set default parameter values, e.g. for `api-version`
            c.OperationFilter<SwaggerDefaultValuesOperationFilter>();

            var filePath = Path.Combine(AppContext.BaseDirectory, "tyger-server.xml");
            c.IncludeXmlComments(filePath);
        });
    }

    public static void UseOpenApi(this WebApplication app)
    {
        app.UseSwagger();
        if (app.Environment.IsDevelopment())
        {
            app.UseSwaggerUI(options =>
            {
                foreach (var description in app.DescribeApiVersions())
                {
                    var url = $"/swagger/{description.GroupName}/swagger.json";
                    var name = description.GroupName.ToUpperInvariant();
                    options.SwaggerEndpoint(url, name);
                }
            });
        }
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
                    Title = $"Tyger API",
                    Version = description.ApiVersion.ToString(),
                });
        }
    }
}

/// <summary>
/// Represents the OpenAPI/Swashbuckle operation filter used to document information provided, but not used.
/// </summary>
/// <remarks>This <see cref="IOperationFilter"/> is only required due to bugs in the <see cref="SwaggerGenerator"/>.
/// Once they are fixed and published, this class can be removed.</remarks>
internal sealed class SwaggerDefaultValuesOperationFilter : IOperationFilter
{
    /// <inheritdoc />
    public void Apply(OpenApiOperation operation, OperationFilterContext context)
    {
        var apiDescription = context.ApiDescription;

        operation.Deprecated |= apiDescription.IsDeprecated();

        // REF: https://github.com/domaindrivendev/Swashbuckle.AspNetCore/issues/1752#issue-663991077
        foreach (var responseType in context.ApiDescription.SupportedResponseTypes)
        {
            // REF: https://github.com/domaindrivendev/Swashbuckle.AspNetCore/blob/b7cf75e7905050305b115dd96640ddd6e74c7ac9/src/Swashbuckle.AspNetCore.SwaggerGen/SwaggerGenerator/SwaggerGenerator.cs#L383-L387
            var responseKey = responseType.IsDefaultResponse ? "default" : responseType.StatusCode.ToString();
            var response = operation.Responses[responseKey];

            foreach (var contentType in response.Content.Keys)
            {
                if (!responseType.ApiResponseFormats.Any(x => x.MediaType == contentType))
                {
                    response.Content.Remove(contentType);
                }
            }
        }

        if (operation.Parameters == null)
        {
            return;
        }

        // REF: https://github.com/domaindrivendev/Swashbuckle.AspNetCore/issues/412
        // REF: https://github.com/domaindrivendev/Swashbuckle.AspNetCore/pull/413
        foreach (var parameter in operation.Parameters)
        {
            try
            {
                var description = apiDescription.ParameterDescriptions.First(p => p.Name == parameter.Name);
                parameter.Description ??= description.ModelMetadata?.Description;

                if (parameter.Schema.Default == null &&
                     description.DefaultValue != null &&
                     description.DefaultValue is not DBNull &&
                     description.ModelMetadata is ModelMetadata modelMetadata)
                {
                    // REF: https://github.com/Microsoft/aspnet-api-versioning/issues/429#issuecomment-605402330
                    var json = JsonSerializer.Serialize(description.DefaultValue, modelMetadata.ModelType);
                    parameter.Schema.Default = OpenApiAnyFactory.CreateFromJson(json);
                }

                parameter.Required |= description.IsRequired;
            }
            catch (Exception e)
            {
                System.Diagnostics.Debug.WriteLine($"Got Exception ${e} for parameter ${parameter.Name}");
                continue;
            }
        }
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
