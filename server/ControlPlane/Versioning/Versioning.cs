// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Asp.Versioning;
using Microsoft.AspNetCore.Rewrite;
using Tyger.Common.Api;

namespace Tyger.ControlPlane.Versioning;

public static class ApiVersions
{

    public static readonly ApiVersion V0p8 = new(0, 8);
    public static readonly ApiVersion V0p9 = new(0, 9);
    public static readonly ApiVersion V1p0 = new(1, 0);

    public static void AddApiVersioning(this IHostApplicationBuilder builder)
    {
        builder.Services.AddProblemDetails();
        builder.Services.AddSingleton<IProblemDetailsWriter, ErrorBodyWriter>();
        builder.Services.AddApiVersioning(options =>
            {
                options.ApiVersionReader = new QueryStringApiVersionReader("api-version");
                options.DefaultApiVersion = V1p0;
            })
            .AddApiExplorer(options =>
            {
                options.GroupNameFormat = "'v'VVVV";
            });
    }

    public static void UseApiVersioning(this WebApplication app)
    {
        // For backward-compatibility with old clients, rewrite all requests starting with `/v1/`
        var options = new RewriteOptions()
            .Add(RewriteRules.RewriteV1ApiRequests);
        app.UseRewriter(options);
    }
}

internal sealed class RewriteRules
{
    public static void RewriteV1ApiRequests(RewriteContext context)
    {
        var request = context.HttpContext.Request;

        // Is this is an old Tyger CLI client?
        if (request.Path.StartsWithSegments("/v1"))
        {
            var newPath = request.Path.Value?.Replace("/v1/", "/");
            context.HttpContext.Request.Path = newPath;

            var feature = context.HttpContext.Features.Get<IApiVersioningFeature>();
            if (feature != null)
            {
                if (feature.RequestedApiVersion == null)
                {
                    // Default to API version 1.0
                    feature.RequestedApiVersion = ApiVersions.V1p0;
                }
                else if (feature.RequestedApiVersion != ApiVersions.V1p0)
                {
                    // The old Tyger CLI client does not request an API version, so we force an UnsupportedApiVersion error
                    feature.RequestedApiVersion = new ApiVersion(0, 0);
                }
            }
        }
    }
}

/// <summary>
/// Implements a problem details writer for emitting our own ErrorBody JSON response.
/// Loosely based on https://github.com/dotnet/aspnet-api-versioning/blob/3fc071913dcded23eeb5ebe55bca44f3828488bf/src/AspNetCore/WebApi/src/Asp.Versioning.Http/ErrorObjectWriter.cs
/// </summary>
public partial class ErrorBodyWriter : IProblemDetailsWriter
{
    private const string ProblemDetailsCodeKey = "code";

    public virtual bool CanWrite(ProblemDetailsContext context)
    {
        ArgumentNullException.ThrowIfNull(context);

        var type = context.ProblemDetails.Type;

        string? code = null;

        if (type == ProblemDetailsDefaults.Unsupported.Type)
        {
            code = ProblemDetailsDefaults.Unsupported.Code;
        }
        else if (type == ProblemDetailsDefaults.Unspecified.Type)
        {
            code = ProblemDetailsDefaults.Unspecified.Code;
        }
        else if (type == ProblemDetailsDefaults.Invalid.Type)
        {
            code = ProblemDetailsDefaults.Invalid.Code;
        }
        else if (type == ProblemDetailsDefaults.Ambiguous.Type)
        {
            code = ProblemDetailsDefaults.Ambiguous.Code;
        }

        if (code != null)
        {
            // This is a workaround for Asp.Versioning BUG https://github.com/dotnet/aspnet-api-versioning/issues/1091
            context.ProblemDetails.Extensions[ProblemDetailsCodeKey] = code;
            return true;
        }

        return false;
    }

    public ValueTask WriteAsync(ProblemDetailsContext context)
    {
        ArgumentNullException.ThrowIfNull(context);

        var response = context.HttpContext.Response;

        var errorCode = context.ProblemDetails.Extensions.TryGetValue("code", out var value) &&
                   value is string code ? code : "";
        var errorMessage = context.ProblemDetails.Title ?? "";

        var obj = new ErrorBody(errorCode, errorMessage);

        return new ValueTask(Results.Json(obj, statusCode: response.StatusCode).ExecuteAsync(context.HttpContext));
    }
}
