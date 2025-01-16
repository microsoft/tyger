using System.Buffers.Binary;
using System.IO.Hashing;
using System.Text;

namespace Tyger.ControlPlane.Model;

public static class XxHashExtensions
{
    private static readonly byte[] s_keyValueHashDelimiter = [0x0];

    public static void Append(this XxHash3 hash, string value)
    {
        if (value.Length <= 1024)
        {
            Span<byte> buf = stackalloc byte[value.Length];
            if (Encoding.UTF8.TryGetBytes(value, buf, out int written))
            {
                hash.Append(buf[..written]);
            }

            return;
        }

        hash.Append(Encoding.UTF8.GetBytes(value));
    }

    public static void Append(this XxHash3 hash, int value)
    {
        Span<byte> buf = stackalloc byte[sizeof(int)];
        BinaryPrimitives.WriteInt32LittleEndian(buf, value);
        hash.Append(buf);
    }

    public static void Append(this XxHash3 hash, long value)
    {
        Span<byte> buf = stackalloc byte[sizeof(long)];
        BinaryPrimitives.WriteInt64LittleEndian(buf, value);
        hash.Append(buf);
    }

    public static XxHash3 Append(this XxHash3 hash, IReadOnlyDictionary<string, string>? tags)
    {
        if (tags is null or { Count: 0 })
        {
            hash.Append([byte.MaxValue]);
            return hash;
        }

        // Assuming correcly sorted if SortedList/SortedDictionary and assuming that entries were added to OrderedDictionary
        // in sorted order
        if (tags is OrderedDictionary<string, string> or SortedDictionary<string, string> or SortedList<string, string>)
        {
            foreach (var tag in tags)
            {
                hash.Append(tag.Key);
                hash.Append(s_keyValueHashDelimiter);
                hash.Append(tag.Value);
            }
        }
        else
        {
            foreach (var tag in tags.OrderBy(kvp => kvp.Key, StringComparer.InvariantCultureIgnoreCase))
            {
                hash.Append(tag.Key);
                hash.Append(s_keyValueHashDelimiter);
                hash.Append(tag.Value);
            }
        }

        return hash;
    }
}
