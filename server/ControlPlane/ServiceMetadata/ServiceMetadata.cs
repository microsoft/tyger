// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Auth;

namespace Tyger.ControlPlane.ServiceMetadata;

public static class ServiceMetadata
{
    public static void MapServiceMetadata(this WebApplication app)
    {
        Model.ServiceMetadata? serviceMetadata = null;
        app.MapGet(
            "/v1/metadata",
            (IEnumerable<ICapabilitiesContributor> contributor, IOptions<AuthOptions> auth) =>
            {
                if (serviceMetadata is null)
                {
                    var capabilities = contributor.Aggregate(Capabilities.None, (acc, c) => acc | c.GetCapabilities());
                    var capabilityStrings = Enum.GetValues<Capabilities>().Where(c => c != Capabilities.None && capabilities.HasFlag(c)).Select(c => c.ToString()).ToList();
                    serviceMetadata = auth.Value.Enabled
                        ? new(auth.Value.Authority, auth.Value.Audience, auth.Value.CliAppUri, Capabilities: capabilityStrings)
                        : new(Capabilities: capabilityStrings);
                }

                return serviceMetadata;
            }).AllowAnonymous();
    }
}

[Flags]
public enum Capabilities
{
    None = 0,
    Gpu = 1 << 0,
    NodePools = 1 << 1,
    DistributedRuns = 1 << 2,
    EphemeralBuffers = 1 << 3,
}

public interface ICapabilitiesContributor
{
    Capabilities GetCapabilities();
}
