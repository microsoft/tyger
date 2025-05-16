// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Tyger.ControlPlane.Buffers;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public class KubernetesEphemeralBufferProvider : IEphemeralBufferProvider
{
    public Task<Uri?> CreateBufferAccessUrl(string id, bool writeable, bool preferTcp, bool fromDocker, TimeSpan? accessTtl, CancellationToken cancellationToken)
    {
        throw new ValidationException("Ephemeral buffers are not supported.");
    }
}
