namespace Tyger.Server.Logging;

/// <summary>
/// A stream that wraps another stream and counts the number of lines as the
/// stream is read. Assuming UTF-8 and \n line endings.
/// A trailing newline is not counted as its own line.
/// </summary>
public class LineCountingReadStream : Stream
{
    private readonly Stream _inner;

    private int _newlineCount;
    private bool _atNewline = true;

    public LineCountingReadStream(Stream inner) => _inner = inner;

    public int LineCount => _atNewline ? _newlineCount : _newlineCount + 1;

    public override bool CanRead => true;

    public override bool CanSeek => false;

    public override bool CanWrite => false;

    public override long Length => _inner.Length;

    public override long Position { get => _inner.Position; set => throw new NotSupportedException(); }

    public override void Flush() => _inner.Flush();

    public override int Read(byte[] buffer, int offset, int count)
    {
        int c = _inner.Read(buffer, offset, count);
        ScanForNewlines(new Span<byte>(buffer, offset, c));
        return c;
    }

    public override Task<int> ReadAsync(byte[] buffer, int offset, int count, CancellationToken cancellationToken)
    {
        return ReadAsync(new Memory<byte>(buffer, offset, count), cancellationToken).AsTask();
    }

    public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default)
    {
        int c = await _inner.ReadAsync(buffer, cancellationToken);
        ScanForNewlines(buffer.Span[..c]);
        return c;
    }

    public override int Read(Span<byte> buffer)
    {
        int c = _inner.Read(buffer);
        ScanForNewlines(buffer[..c]);
        return c;
    }

    private void ScanForNewlines(Span<byte> buffer)
    {
        for (int i = 0; i < buffer.Length; i++)
        {
            if (buffer[i] == (byte)'\n')
            {
                _newlineCount++;
                _atNewline = true;
            }
            else
            {
                _atNewline = false;
            }
        }
    }

    public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();

    public override void SetLength(long value) => throw new NotSupportedException();

    public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();
}
