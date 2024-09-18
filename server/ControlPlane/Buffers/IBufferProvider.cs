// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Buffers;

public interface IBufferProvider
{
    Task CreateBuffer(string id, CancellationToken cancellationToken);
    Task<bool> BufferExists(string id, CancellationToken cancellationToken);

    Uri CreateBufferAccessUrl(string id, bool writeable, bool preferTcp);

    Task<Run> ExportBuffers(ExportBuffersRequest exportBufferRequest, CancellationToken cancellationToken);
}

public interface IEphemeralBufferProvider
{
    Task<Uri?> CreateBufferAccessUrl(string id, bool writeable, bool preferTcp, bool fromDocker, CancellationToken cancellationToken);
}
