// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Net.Sockets;
using System.Threading.Channels;
using System.Threading.RateLimiting;
using k8s;
using k8s.Autorest;
using k8s.Models;

namespace Tyger.ControlPlane.Compute.Kubernetes;

// Inspired by https://github.com/microsoft/reverse-proxy/blob/c9042d21927716f32e072fae4b634943de9e18cc/src/Kubernetes.Controller/Client/ResourceInformer.cs

public abstract class ResourceInformer<TResource, TListResource>
    where TResource : class, IKubernetesObject<V1ObjectMeta>, new()
    where TListResource : class, IKubernetesObject<V1ListMeta>, IItems<TResource>, new()
{
    private Dictionary<string, TResource> _cache = [];
    private string? _lastResourceVersion;
    private readonly string? _labelSelector;
    private readonly ChannelWriter<TResource> _initialResourcesChannel;
    private readonly ChannelWriter<(WatchEventType eventType, TResource resource)> _updatesChannel;
    private readonly string _namespace;
    private readonly ILogger _logger;

    protected ResourceInformer(
        IKubernetes client,
        string @namespace,
        string labelSelector,
        ChannelWriter<TResource> initialResourcesChannel,
        ChannelWriter<(WatchEventType eventType, TResource resource)> updatesChannel,
        ILogger logger)
    {
        Client = client;
        _namespace = @namespace;
        _labelSelector = labelSelector;
        _initialResourcesChannel = initialResourcesChannel;
        _updatesChannel = updatesChannel;
        _logger = logger;
    }

    protected IKubernetes Client { get; init; }

    public async Task ExecuteAsync(CancellationToken cancellationToken)
    {
        var limiter = new TokenBucketRateLimiter(new() { ReplenishmentPeriod = TimeSpan.FromSeconds(5), TokensPerPeriod = 1, QueueLimit = 1000, TokenLimit = 3 });
        var shouldSync = true;
        var firstSync = true;
        while (true)
        {
            try
            {
                cancellationToken.ThrowIfCancellationRequested();

                try
                {
                    if (shouldSync)
                    {
                        await ListAsync(firstSync, cancellationToken);
                        shouldSync = false;
                    }

                    if (firstSync)
                    {
                        _initialResourcesChannel.Complete();
                        firstSync = false;
                    }

                    await WatchAsync(cancellationToken);
                }
                catch (IOException ex) when (ex.InnerException is SocketException)
                {
                    _logger.ErrorWatchingResources(ex);
                }
                catch (KubernetesException ex)
                {
                    _logger.ErrorWatchingResources(ex);

                    // deal with this non-recoverable condition "too old resource version"
                    // with a re-sync to listing everything again ensuring no subscribers miss updates
                    if (ex is KubernetesException kubernetesError)
                    {
                        if (string.Equals(kubernetesError.Status.Reason, "Expired", StringComparison.Ordinal))
                        {
                            _lastResourceVersion = null;
                            shouldSync = true;
                        }
                    }
                }

                // rate limiting the reconnect loop
                await limiter.AcquireAsync(cancellationToken: cancellationToken);
            }
            catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception error)
            {
                _logger.ErrorWatchingResources(error);
            }
        }
    }

    protected abstract Task<HttpOperationResponse<TListResource>> RetrieveResourceListAsync(
        string namespaceParameter,
        string? labelSelector,
        bool watch,
        string? resourceVersion,
        string? continuationToken,
        CancellationToken cancellationToken);

    private async Task ListAsync(bool firstSync, CancellationToken cancellationToken)
    {
        var previousCache = _cache;
        _cache = [];

        string? continueParameter = null;
        do
        {
            cancellationToken.ThrowIfCancellationRequested();

            // request next page of items
            using var listWithHttpMessage = await RetrieveResourceListAsync(_namespace, _labelSelector, false, _lastResourceVersion, continueParameter, cancellationToken);

            var list = listWithHttpMessage.Body;
            foreach (var item in list.Items)
            {
                var key = item.Name();
                _cache[item.Name()] = item;

                var watchEventType = WatchEventType.Added;
                if (previousCache.Remove(key))
                {
                    // an already-known key is provided as a modification for re-sync purposes
                    watchEventType = WatchEventType.Modified;
                }

                if (firstSync)
                {
                    await _initialResourcesChannel.WriteAsync(item, cancellationToken);
                }
                else
                {
                    await _updatesChannel.WriteAsync((watchEventType, item), cancellationToken);
                }
            }

            if (!firstSync)
            {
                foreach (var (key, value) in previousCache)
                {
                    // for anything which was previously known but not part of list
                    // send a deleted notification to clear any observer caches
                    await _updatesChannel.WriteAsync((WatchEventType.Deleted, value), cancellationToken);
                }
            }

            // keep track of values needed for next page and to start watching
            _lastResourceVersion = list.ResourceVersion();
            continueParameter = list.Continue();
        }
        while (!string.IsNullOrEmpty(continueParameter));
    }

    private async Task WatchAsync(CancellationToken cancellationToken)
    {
        // completion source helps turn OnClose callback into something awaitable
        var watcherCompletionSource = new TaskCompletionSource<int>();

        // begin watching where list left off
        var watchWithHttpMessage = RetrieveResourceListAsync(_namespace, watch: true, resourceVersion: _lastResourceVersion, labelSelector: _labelSelector, continuationToken: null, cancellationToken: cancellationToken);

        var lastEventUtc = DateTime.UtcNow;
        using var watcher = watchWithHttpMessage.Watch<TResource, TListResource>(
            async (watchEventType, item) =>
            {
                if (!watcherCompletionSource.Task.IsCompleted)
                {
                    lastEventUtc = DateTime.UtcNow;
                    await OnEvent(watchEventType, item);
                }
            },
            error =>
            {
                if (error is KubernetesException kubernetesError)
                {
                    // deal with this non-recoverable condition "too old resource version"
                    if (string.Equals(kubernetesError.Status.Reason, "Expired", StringComparison.Ordinal))
                    {
                        // cause this error to surface
                        watcherCompletionSource.TrySetException(error);
                        throw error;
                    }
                }

                _logger.ErrorWatchingResources(error);
            },
            () =>
            {
                watcherCompletionSource.TrySetResult(0);
            });

        // reconnect if no events have arrived after a certain time
        using var checkLastEventUtcTimer = new Timer(
            _ =>
            {
                var lastEvent = DateTime.UtcNow - lastEventUtc;
                if (lastEvent > TimeSpan.FromMinutes(9.5))
                {
                    lastEventUtc = DateTime.MaxValue;
                    watcherCompletionSource.TrySetCanceled();
                    watcher.Dispose();
                }
            },
            state: null,
            dueTime: TimeSpan.FromSeconds(45),
            period: TimeSpan.FromSeconds(45));

        using var registration = cancellationToken.Register(watcher.Dispose);
        try
        {
            await watcherCompletionSource.Task;
        }
        catch (TaskCanceledException)
        {
        }
    }

    private async ValueTask OnEvent(WatchEventType watchEventType, TResource item)
    {
        if (watchEventType is WatchEventType.Added or WatchEventType.Modified)
        {
            // BUGBUG: log warning if cache was not in expected state
            _cache[item.Name()] = item;
        }

        if (watchEventType == WatchEventType.Deleted)
        {
            _cache.Remove(item.Name());
        }

        if (watchEventType is WatchEventType.Bookmark)
        {
            _lastResourceVersion = item.ResourceVersion();
        }

        if (watchEventType is WatchEventType.Added or WatchEventType.Modified or WatchEventType.Deleted)
        {
            await _updatesChannel.WriteAsync((watchEventType, item));
        }
    }
}

public class PodInformer : ResourceInformer<V1Pod, V1PodList>
{
    public PodInformer(
        IKubernetes client,
        string @namespace,
        string labelSelector,
        ChannelWriter<V1Pod> initialResourcesChannel,
        ChannelWriter<(WatchEventType eventType, V1Pod resource)> updatesChannel,
        ILogger<PodInformer> logger)
        : base(client, @namespace, labelSelector, initialResourcesChannel, updatesChannel, logger)
    {
    }

    protected override Task<HttpOperationResponse<V1PodList>> RetrieveResourceListAsync(string namespaceParameter, string? labelSelector, bool watch, string? resourceVersion, string? continuationToken, CancellationToken cancellationToken)
    {
        return Client.CoreV1.ListNamespacedPodWithHttpMessagesAsync(
            namespaceParameter,
            labelSelector: labelSelector,
            watch: watch,
            resourceVersion: resourceVersion,
            continueParameter: continuationToken,
            allowWatchBookmarks: watch,
            cancellationToken: cancellationToken);
    }
}
