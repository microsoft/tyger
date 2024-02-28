namespace Tyger.ControlPlane.Buffers;

public interface IBufferProvider
{
    Task CreateBuffer(string id, CancellationToken cancellationToken);
    Task<bool> BufferExists(string id, CancellationToken cancellationToken);

    Task<Uri> CreateBufferAccessUrl(string id, bool writeable, CancellationToken cancellationToken);
}
