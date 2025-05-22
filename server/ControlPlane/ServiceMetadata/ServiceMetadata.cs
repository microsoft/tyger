// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Microsoft.Extensions.Options;
using Tyger.ControlPlane.AccessControl;
using Tyger.ControlPlane.Versioning;

namespace Tyger.ControlPlane.ServiceMetadata;

public static class ServiceMetadata
{
    public static void MapServiceMetadata(this WebApplication app)
    {
        Model.ServiceMetadata? serviceMetadata = null;
        app.MapGet(
            "/metadata",
            (IEnumerable<ICapabilitiesContributor> contributor, IOptions<AccessControlOptions> accessControl) =>
            {
                if (serviceMetadata is null)
                {
                    var capabilities = contributor.Aggregate(Capabilities.None, (acc, c) => acc | c.GetCapabilities());
                    var capabilityStrings = Enum.GetValues<Capabilities>().Where(c => c != Capabilities.None && capabilities.HasFlag(c)).Select(c => c.ToString()).ToList();
                    var apiVersionsSupported = ApiVersioning.SupportedVersions().Select(v => v.ToString()).ToList();

                    serviceMetadata = new Model.ServiceMetadata
                    {
                        Capabilities = capabilityStrings,
                        ApiVersions = apiVersionsSupported
                    };

                    if (accessControl.Value.Enabled)
                    {
                        serviceMetadata = serviceMetadata with
                        {
                            RbacEnabled = true,
                            Authority = accessControl.Value.Authority,
                            Audience = accessControl.Value.Audience,
                            ApiAppUri = accessControl.Value.ApiAppUri,
                            ApiAppId = accessControl.Value.ApiAppId,
                            CliAppUri = accessControl.Value.CliAppUri,
                            CliAppId = accessControl.Value.CliAppId,
                        };
                    }
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
    Docker = 1 << 4,
    Kubernetes = 1 << 5,
}

public interface ICapabilitiesContributor
{
    Capabilities GetCapabilities();
}
