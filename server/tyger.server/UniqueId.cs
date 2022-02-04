using SimpleBase;

namespace Tyger.Server;

public static class UniqueId
{
    private static readonly Base32 lowercaseRfc4648Base32 = new(new("abcdefghijklmnopqrstuvwxyz234567"));

    /// <summary>
    /// Creates a base32-encoded GUID.
    /// </summary>
    public static string Create()
    {
        Span<byte> bytes = stackalloc byte[16];
        Guid.NewGuid().TryWriteBytes(bytes);

        return lowercaseRfc4648Base32.Encode(bytes, false).ToLowerInvariant();
    }
}
