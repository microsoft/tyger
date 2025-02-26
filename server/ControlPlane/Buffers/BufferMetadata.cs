// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Buffers;

public static class BufferMetadata
{
    public const string EndMetadataBlobName = ".bufferend";
    public static readonly byte[] FailedEndMetadataContent = "{\"status\":\"failed\"}"u8.ToArray();
}
