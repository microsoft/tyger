// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Docker.DotNet;
using Docker.DotNet.Models;
using Microsoft.Extensions.Options;
using Tyger.Common.Buffers;
using Tyger.ControlPlane.Buffers;

namespace Tyger.ControlPlane.Compute.Docker;

public class DockerEphemeralBufferProvider : IEphemeralBufferProvider
{
    private readonly DockerClient _client;
    private readonly SignDataFunc _signData;

    public DockerEphemeralBufferProvider(DockerClient client, IOptions<BufferOptions> bufferOptions)
    {
        _client = client;
        _signData = DigitalSignature.CreateSingingFunc(
            DigitalSignature.CreateAsymmetricAlgorithmFromPem(bufferOptions.Value.PrimarySigningPrivateKeyPath));
    }

    public async Task<Uri?> CreateBufferAccessUrl(string id, bool writeable, bool preferTcp, bool fromDocker, TimeSpan? accessTtl, CancellationToken cancellationToken)
    {
        var containers = await _client.Containers
            .ListContainersAsync(
                new ContainersListParameters()
                {
                    All = true,
                    Filters = new Dictionary<string, IDictionary<string, bool>>
                    {
                        {"label", new Dictionary<string, bool>{{ $"{DockerRunCreator.EphemeralBufferIdLabelKey}={id}" , true } } }
                    }
                }, cancellationToken);

        if (containers.Count == 0)
        {
            return null;
        }

        if (containers.Count > 1)
        {
            throw new InvalidOperationException($"Multiple containers found for ephemeral buffer {id}");
        }

        QueryString queryString = GetSasQueryString(id, writeable, accessTtl);

        if (!preferTcp)
        {
            if (!containers[0].Labels.TryGetValue(DockerRunCreator.EphemeralBufferSocketPathLabelKey, out var socketPath))
            {
                throw new InvalidOperationException($"Container {containers[0].ID} does not have the required label {DockerRunCreator.EphemeralBufferSocketPathLabelKey}");
            }

            return new Uri($"http+unix://{socketPath}:{queryString}");
        }

        for (int retryCount = 0; ; retryCount++)
        {
            var container = await _client.Containers.InspectContainerAsync(containers[0].ID, cancellationToken);

            (var innerSpec, var hostSpecs) = container.NetworkSettings.Ports.Single();

            if (hostSpecs is null or { Count: 0 })
            {
                if (retryCount == 20)
                {
                    throw new InvalidOperationException($"Container {container.ID} does not have any exposed ports");
                }

                await Task.Delay(500, cancellationToken);
                continue;
            }

            if (fromDocker)
            {
                var innerPort = innerSpec.Split("/")[0];
                var ip = container.NetworkSettings.IPAddress;
                if (string.IsNullOrEmpty(ip))
                {
                    ip = container.NetworkSettings.Networks.SingleOrDefault().Value?.IPAddress;

                    if (string.IsNullOrEmpty(ip))
                    {
                        throw new InvalidOperationException($"Unable to determine container IP address for container {container.Name}");
                    }
                }

                return new Uri($"http://{ip}:{innerPort}{queryString}");
            }

            var hostPort = hostSpecs[0].HostPort;

            return new Uri($"http://localhost:{hostPort}{queryString}");
        }
    }

    public QueryString GetSasQueryString(string bufferId, bool writeable, TimeSpan? accessTtl)
    {
        var action = writeable ? SasAction.Create | SasAction.Read : SasAction.Read;
        var queryString = LocalSasHandler.GetSasQueryString(bufferId, SasResourceType.Blob, action, accessTtl, _signData, "1.0");
        queryString = queryString.Add("relay", "true");
        return queryString;
    }
}
