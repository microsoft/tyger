// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Auth;
using Tyger.ControlPlane.Versioning;

namespace Tyger.ControlPlane.ServiceMetadata;

public static class ServiceMetadata
{
    public static void MapServiceMetadata(this RouteGroupBuilder root)
    {
        Model.ServiceMetadata? serviceMetadata = null;
        root.MapGet(
            "/metadata",
            (IEnumerable<ICapabilitiesContributor> contributor, IOptions<AuthOptions> auth) =>
            {
                if (serviceMetadata is null)
                {
                    var capabilities = contributor.Aggregate(Capabilities.None, (acc, c) => acc | c.GetCapabilities());
                    var capabilityStrings = Enum.GetValues<Capabilities>().Where(c => c != Capabilities.None && capabilities.HasFlag(c)).Select(c => c.ToString()).ToList();
                    var apiVersionsSupported = ApiVersions.SupportedVersions().Select(v => v.ToString()).ToList();

                    serviceMetadata = new Model.ServiceMetadata
                    {
                        Capabilities = capabilityStrings,
                        ApiVersions = apiVersionsSupported
                    };

                    if (auth.Value.Enabled)
                    {
                        serviceMetadata = serviceMetadata with
                        {
                            Authority = auth.Value.Authority,
                            Audience = auth.Value.Audience,
                            CliAppUri = auth.Value.CliAppUri
                        };
                    }
                }

                return serviceMetadata;
            }).AllowAnonymous().IsApiVersionNeutral();
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
    Docker = 1 << 4,
    Kubernetes = 1 << 5,
}

public interface ICapabilitiesContributor
{
    Capabilities GetCapabilities();
}
