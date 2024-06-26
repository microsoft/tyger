// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using SimpleBase;

namespace Tyger.ControlPlane.Buffers;

public static class UniqueId
{
    private static readonly Base32 s_lowercaseRfc4648Base32 = new(new("abcdefghijklmnopqrstuvwxyz234567"));

    /// <summary>
    /// Creates a base32-encoded GUID.
    /// </summary>
    public static string Create()
    {
        Span<byte> bytes = stackalloc byte[16];
        Guid.NewGuid().TryWriteBytes(bytes);

        return s_lowercaseRfc4648Base32.Encode(bytes, false).ToLowerInvariant();
    }
}
