using System.Runtime.CompilerServices;
using System.Threading.Channels;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public sealed class DedupedChannel<T> : IDisposable
{
    private readonly Channel<T> _channel;
    private readonly HashSet<T> _set;
    private readonly SemaphoreSlim _semaphore;

    public DedupedChannel(Channel<T> channel, IEqualityComparer<T>? comparer)
    {
        _channel = channel;
        _set = new HashSet<T>(comparer);
        _semaphore = new SemaphoreSlim(1);
    }

    public async Task WriteAsync(T item, CancellationToken cancellationToken = default)
    {
        await _semaphore.WaitAsync(cancellationToken);
        try
        {
            if (!_set.Add(item))
            {
                return;
            }

            await _channel.Writer.WriteAsync(item, cancellationToken);
        }
        finally
        {
            _semaphore.Release();
        }
    }

    public async IAsyncEnumerable<T> ReadAllAsync([EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        await foreach (var item in _channel.Reader.ReadAllAsync(cancellationToken))
        {
            await _semaphore.WaitAsync(cancellationToken);
            try
            {
                _set.Remove(item);
            }
            finally
            {
                _semaphore.Release();
            }

            yield return item;
        }
    }

    public void Dispose()
    {
        _semaphore.Dispose();
    }
}
