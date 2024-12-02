// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Model;
using Buffer = Tyger.ControlPlane.Model.Buffer;

namespace Tyger.ControlPlane.Buffers;

public interface IBufferProvider
{
    Task CreateBuffer(Buffer buffer, CancellationToken cancellationToken);
    Task<bool> BufferExists(string id, CancellationToken cancellationToken);

    Uri CreateBufferAccessUrl(string id, bool writeable, bool preferTcp);

    Task<Run> ExportBuffers(ExportBuffersRequest exportBufferRequest, CancellationToken cancellationToken);
    Task<Run> ImportBuffers(CancellationToken cancellationToken);
}

public record StorageAccountInfo(int Id, string Name, string Location, bool Enabled);

public interface IEphemeralBufferProvider
{
    Task<Uri?> CreateBufferAccessUrl(string id, bool writeable, bool preferTcp, bool fromDocker, CancellationToken cancellationToken);
}
