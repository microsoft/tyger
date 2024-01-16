// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Microsoft.Extensions.Options;
using Tyger.Server.Auth;
using Tyger.Server.Model;

namespace Tyger.Server.ServiceMetadata;

public static class ServiceMetadata
{
    public static void MapServiceMetadata(this WebApplication app)
    {
        app.MapGet(
            "/v1/metadata",
            (IOptions<AuthOptions> auth) =>
                auth.Value.Enabled
                ? new Metadata(auth.Value.Authority, auth.Value.Audience, auth.Value.CliAppUri)
                : new Metadata())
            .AllowAnonymous();
    }
}
