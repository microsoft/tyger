using System.Threading.Channels;
using Tyger.ControlPlane.Database;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public class LeaseManager : BackgroundService
{
    private readonly Repository _repository;
    private readonly string _leaseHolderId = Environment.MachineName;
    private int _latestLeaseToken;

    private int _started;
    private readonly List<ChannelWriter<(bool, int)>> _onLeaseOwnershipAcquiredChannel = [];

    public string LeaseName { get; }

    public LeaseManager(Repository repository, string leaseName)
    {
        _repository = repository;
        LeaseName = leaseName;
    }

    public void RegisterListener(ChannelWriter<(bool acquired, int token)> listener)
    {
        if (Volatile.Read(ref _started) == 1)
        {
            throw new InvalidOperationException($"Registering listeners after starting the lease is not supported.");
        }

        _onLeaseOwnershipAcquiredChannel.Add(listener);
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        if (Interlocked.Exchange(ref _started, 1) == 1)
        {
            throw new InvalidOperationException("Lease can only be started once.");
        }

        await _repository.AcquireAndHoldLease(LeaseName, _leaseHolderId, async hasLease =>
        {
            var incrementedLeaseToken = Interlocked.Increment(ref _latestLeaseToken);
            foreach (var listener in _onLeaseOwnershipAcquiredChannel)
            {
                await listener.WriteAsync((hasLease, incrementedLeaseToken), stoppingToken);
            }
        }, stoppingToken);
    }

    public int GetCurrentLeaseToken()
    {
        return Volatile.Read(ref _latestLeaseToken);
    }
}
