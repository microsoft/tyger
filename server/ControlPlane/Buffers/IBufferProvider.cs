namespace Tyger.ControlPlane.Buffers;

public interface IBufferProvider
{
    Task CreateBuffer(string id, CancellationToken cancellationToken);
    Task<bool> BufferExists(string id, CancellationToken cancellationToken);

    Uri CreateBufferAccessUrl(string id, bool writeable);
}

public interface IEphemeralBufferProvider
{
    Uri CreateBufferAccessUrl(string id, bool writeable);
}
