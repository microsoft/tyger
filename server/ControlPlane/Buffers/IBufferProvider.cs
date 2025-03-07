// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Model;
using Buffer = Tyger.ControlPlane.Model.Buffer;

namespace Tyger.ControlPlane.Buffers;

public interface IBufferProvider
{
    Task<Buffer> CreateBuffer(Buffer buffer, CancellationToken cancellationToken);

    Task<int> DeleteBuffers(IList<string> ids, CancellationToken cancellationToken);

    Task<IList<(string id, bool writeable, BufferAccess? bufferAccess)>> CreateBufferAccessUrls(IList<(string id, bool writeable)> requests, bool preferTcp, bool checkExists, CancellationToken cancellationToken);
    IList<StorageAccount> GetStorageAccounts();

    Task<Run> ExportBuffers(ExportBuffersRequest exportBufferRequest, CancellationToken cancellationToken);
    Task<Run> ImportBuffers(ImportBuffersRequest importBuffersRequest, CancellationToken cancellationToken);

    Task TryMarkBufferAsFailed(string id, CancellationToken cancellationToken);
}

public interface IEphemeralBufferProvider
{
    Task<Uri?> CreateBufferAccessUrl(string id, bool writeable, bool preferTcp, bool fromDocker, CancellationToken cancellationToken);
}
