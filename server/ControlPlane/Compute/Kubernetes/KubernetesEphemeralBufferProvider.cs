// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Tyger.ControlPlane.Buffers;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public class KubernetesEphemeralBufferProvider : IEphemeralBufferProvider
{
    public Uri CreateBufferAccessUrl(string id, int? port, bool writeable, bool preferTcp)
    {
        throw new ValidationException("Ephemeral buffers are not supported.");
    }
}
