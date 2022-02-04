using System.Buffers;
using System.Diagnostics;

namespace Tyger.Server.Logging;

/// <summary>
/// This is only to be able to use a Utf8JsonWriter when we need to produce a string
/// while minimizing allocations. Taken from (and slightly simplified):
/// https://github.com/dotnet/runtime/blob/7550df8b58452abf4f45518335914070f4f4f54f/src/libraries/Common/src/System/Text/Json/PooledByteBufferWriter.cs
/// </summary>
internal sealed class PooledByteBufferWriter : IBufferWriter<byte>, IDisposable
{
    private byte[] _rentedBuffer;
    private int _index;

    private const int MinimumBufferSize = 256;

    public PooledByteBufferWriter(int initialCapacity)
    {
        Debug.Assert(initialCapacity > 0);

        _rentedBuffer = ArrayPool<byte>.Shared.Rent(initialCapacity);
        _index = 0;
    }

    public ReadOnlyMemory<byte> WrittenMemory
    {
        get
        {
            Debug.Assert(_rentedBuffer != null);
            Debug.Assert(_index <= _rentedBuffer.Length);
            return _rentedBuffer.AsMemory(0, _index);
        }
    }

    // Returns the rented buffer back to the pool
    public void Dispose()
    {
        if (_rentedBuffer == null)
        {
            return;
        }

        _rentedBuffer.AsSpan(0, _index).Clear();
        byte[] toReturn = _rentedBuffer;
        _rentedBuffer = null!;
        ArrayPool<byte>.Shared.Return(toReturn);
    }

    public void Advance(int count)
    {
        Debug.Assert(_rentedBuffer != null);
        Debug.Assert(count >= 0);
        Debug.Assert(_index <= _rentedBuffer.Length - count);

        _index += count;
    }

    public Memory<byte> GetMemory(int sizeHint = 0)
    {
        CheckAndResizeBuffer(sizeHint);
        return _rentedBuffer.AsMemory(_index);
    }

    public Span<byte> GetSpan(int sizeHint = 0)
    {
        CheckAndResizeBuffer(sizeHint);
        return _rentedBuffer.AsSpan(_index);
    }

    private void CheckAndResizeBuffer(int sizeHint)
    {
        Debug.Assert(_rentedBuffer != null);
        Debug.Assert(sizeHint >= 0);

        if (sizeHint == 0)
        {
            sizeHint = MinimumBufferSize;
        }

        int availableSpace = _rentedBuffer.Length - _index;

        if (sizeHint > availableSpace)
        {
            int currentLength = _rentedBuffer.Length;
            int growBy = Math.Max(sizeHint, currentLength);
            int newSize = currentLength + growBy;

            if ((uint)newSize > int.MaxValue)
            {
                newSize = currentLength + sizeHint;
                if ((uint)newSize > int.MaxValue)
                {
                    throw new InvalidOperationException("Maximum buffer size exceeded.");
                }
            }

            byte[] oldBuffer = _rentedBuffer;

            _rentedBuffer = ArrayPool<byte>.Shared.Rent(newSize);

            Debug.Assert(oldBuffer.Length >= _index);
            Debug.Assert(_rentedBuffer.Length >= _index);

            Span<byte> previousBuffer = oldBuffer.AsSpan(0, _index);
            previousBuffer.CopyTo(_rentedBuffer);
            previousBuffer.Clear();
            ArrayPool<byte>.Shared.Return(oldBuffer);
        }

        Debug.Assert(_rentedBuffer.Length - _index > 0);
        Debug.Assert(_rentedBuffer.Length - _index >= sizeHint);
    }
}
