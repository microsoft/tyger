// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Microsoft.Extensions.Options;
using Tyger.Common.Buffers;
using Tyger.ControlPlane.Buffers;

namespace Tyger.ControlPlane.Compute.Docker;

public class DockerEphemeralBufferProvider : IEphemeralBufferProvider
{
    private readonly string _ephemeralBuffersDir;
    private readonly SignDataFunc _signData;

    public DockerEphemeralBufferProvider(IOptions<DockerOptions> dockerOptions, IOptions<BufferOptions> bufferOptions)
    {
        _ephemeralBuffersDir = dockerOptions.Value.EphemeralBuffersPath;
        _signData = DigitalSignature.CreateSingingFunc(
            DigitalSignature.CreateAsymmetricAlgorithmFromPem(bufferOptions.Value.PrimarySigningPrivateKeyPath));
    }

    public Uri CreateBufferAccessUrl(string id, int? port, bool writeable, bool preferTcp)
    {
        var action = writeable ? SasAction.Create | SasAction.Read : SasAction.Read;
        var queryString = LocalSasHandler.GetSasQueryString(id, SasResourceType.Blob, action, _signData);
        queryString = queryString.Add("relay", "true");
        if (preferTcp && port.HasValue)
        {
            return new Uri($"http://localhost:{port}{queryString}");
        }

        return new Uri($"http+unix://{Path.Combine(_ephemeralBuffersDir, id)}.sock:{queryString}");
    }
}
