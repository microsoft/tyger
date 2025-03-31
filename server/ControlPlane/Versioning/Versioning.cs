// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Asp.Versioning;
using Asp.Versioning.Builder;
using Microsoft.AspNetCore.Http.Extensions;
using Microsoft.AspNetCore.Rewrite;

namespace Tyger.ControlPlane.Versioning;

public static class ApiVersions
{

    public static readonly ApiVersion V0p9 = new(0, 9);
    public static readonly ApiVersion V1p0 = new(1, 0);
    public static readonly ApiVersion V1p1 = new(1, 1);

    public static void AddApiVersioning(this IHostApplicationBuilder builder)
    {
        builder.Services.AddProblemDetails().AddErrorObjects();
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
                    // The old Tyger CLI client does not request an API version, so this is an error state
                    feature.RequestedApiVersion = null;
                    feature.RawRequestedApiVersion = "";
                    feature.RawRequestedApiVersions = [];
                }
            }
        }
    }
}
