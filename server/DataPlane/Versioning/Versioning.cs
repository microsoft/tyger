// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Asp.Versioning;
using Tyger.Common.Versioning;

namespace Tyger.DataPlane.Versioning;

public static class ApiVersioning
{
    public static readonly ApiVersion V1p0 = new(1, 0);

    public static IEnumerable<ApiVersion> SupportedVersions()
    {
        return [V1p0];
    }

    public static void AddApiVersioning(this IHostApplicationBuilder builder)
    {
        builder.AddApiVersioning(SupportedVersions());
    }

    public static RouteGroupBuilder ConfigureVersionedRouteGroup(this WebApplication app, string prefix)
    {
        var api = app.NewVersionedApi();
        var root = api.MapGroup(prefix)
            .HasApiVersion(V1p0);

        return root;
    }
}
