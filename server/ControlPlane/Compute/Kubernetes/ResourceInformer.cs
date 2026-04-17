// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Threading.Channels;
using System.Threading.RateLimiting;
using k8s;
using k8s.Models;

namespace Tyger.ControlPlane.Compute.Kubernetes;

// Inspired by https://github.com/microsoft/reverse-proxy/blob/c9042d21927716f32e072fae4b634943de9e18cc/src/Kubernetes.Controller/Client/ResourceInformer.cs

public abstract class ResourceInformer<TResource, TListResource>
    where TResource : class, IKubernetesObject<V1ObjectMeta>, new()
    where TListResource : class, IKubernetesObject<V1ListMeta>, IItems<TResource>, new()
{
    // Bound each watch request so the apiserver periodically closes even a
    // completely idle but healthy stream and we get a chance to reconnect.
    protected static readonly TimeSpan WatchRequestTimeout = TimeSpan.FromMinutes(5);

    // If the server does not close within a short grace period after the
    // requested timeout, assume the stream is stuck and reconnect locally.
    private static readonly TimeSpan WatchRequestTimeoutGracePeriod = TimeSpan.FromSeconds(30);

    // An empty watch that lived for at least this long is treated as a normal
    // reconnect (for example server timeout / infra idle close), not as a
    // tight-loop failure.
    private static readonly TimeSpan HealthyEmptyWatchDuration = TimeSpan.FromSeconds(30);

    // If WatchAsync returns this many times in a row without yielding any
    // events, force a full re-list. This defends against silent failure modes
    // where the server immediately closes the stream (e.g. an in-band Status
    // event the enumerator absorbs) and the reconnect loop would otherwise
    // spin forever observing nothing.
    private const int EmptyWatchReListThreshold = 5;

    // A single bad watch frame is harmless and just gets logged. If we see
    // this many in a row within one watch, drop and reconnect rather than
    // sit logging the same parse failure forever.
    private const int MaxWatchDeserializeErrors = 10;

    private Dictionary<string, TResource> _cache = [];
    private string? _lastResourceVersion;
    private readonly string? _labelSelector;
    private readonly ChannelWriter<TResource> _initialResourcesChannel;
    private readonly ChannelWriter<(WatchEventType eventType, TResource resource)> _updatesChannel;
    private readonly string _namespace;
    private readonly ILogger _logger;
    private readonly TimeProvider _timeProvider;
    private readonly RateLimiter _reconnectLimiter;

    private enum WatchCompletion
    {
        ObservedEvents,
        HealthyNoEventClosure,
        SuspiciousEmptyClosure,
    }

    protected ResourceInformer(
        IKubernetes client,
        string @namespace,
        string labelSelector,
        ChannelWriter<TResource> initialResourcesChannel,
        ChannelWriter<(WatchEventType eventType, TResource resource)> updatesChannel,
        ILogger logger,
        TimeProvider? timeProvider = null,
        RateLimiter? reconnectLimiter = null)
    {
        Client = client;
        _namespace = @namespace;
        _labelSelector = labelSelector;
        _initialResourcesChannel = initialResourcesChannel;
        _updatesChannel = updatesChannel;
        _logger = logger;
        _timeProvider = timeProvider ?? TimeProvider.System;
        _reconnectLimiter = reconnectLimiter ?? new TokenBucketRateLimiter(new()
        {
            ReplenishmentPeriod = TimeSpan.FromSeconds(5),
            TokensPerPeriod = 1,
            QueueLimit = 1000,
            TokenLimit = 3,
        });
    }

    protected IKubernetes Client { get; init; }

    public async Task ExecuteAsync(CancellationToken cancellationToken)
    {
        var shouldSync = true;
        var firstSync = true;
        var consecutiveEmptyWatches = 0;

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
                        consecutiveEmptyWatches = 0;
                    }

                    if (firstSync)
                    {
                        _initialResourcesChannel.Complete();
                        firstSync = false;
                    }

                    switch (await WatchAsync(cancellationToken))
                    {
                        case WatchCompletion.ObservedEvents:
                        case WatchCompletion.HealthyNoEventClosure:
                            consecutiveEmptyWatches = 0;
                            break;
                        case WatchCompletion.SuspiciousEmptyClosure:
                            if (++consecutiveEmptyWatches >= EmptyWatchReListThreshold)
                            {
                                _logger.ResourceInformerEmptyWatches(consecutiveEmptyWatches);
                                _lastResourceVersion = null;
                                shouldSync = true;
                                consecutiveEmptyWatches = 0;
                            }

                            break;
                    }
                }
                catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
                {
                    throw;
                }
                catch (KubernetesException ex)
                {
                    _logger.ErrorWatchingResources(ex);

                    // Any in-band Status from the apiserver means the watch
                    // is over and we cannot trust _lastResourceVersion to
                    // still be valid (Reason == "Expired" is the canonical
                    // case, but Gone, Forbidden, etc. are equally
                    // unrecoverable from a watch standpoint). Re-list so no
                    // subscribers miss updates.
                    _lastResourceVersion = null;
                    shouldSync = true;
                }
                catch (Exception ex)
                {
                    // Any other failure (IOException, HttpRequestException,
                    // HttpIOException, HttpOperationException for non-2xx
                    // start-of-watch responses, ObjectDisposedException on a
                    // torn-down HttpClient, etc.) is treated as a transient
                    // transport problem: log, fall through to the rate
                    // limiter, and reconnect using the same
                    // _lastResourceVersion. If the version is actually stale,
                    // the next watch attempt will return a KubernetesException
                    // (handled above) and trigger a re-list.
                    //
                    // Catching Exception here is deliberate: it prevents
                    // unexpected failure modes from bypassing the rate
                    // limiter and burning CPU in a tight reconnect loop, and
                    // ensures the BackgroundService never terminates.
                    _logger.ErrorWatchingResources(ex);
                }

                // rate limiting the reconnect loop
                await _reconnectLimiter.AcquireAsync(cancellationToken: cancellationToken);
            }
            catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception error)
            {
                // Last-resort safety net: nothing in the inner block should
                // reach here (everything above is either handled or
                // rethrown for cancellation). If something does, log and
                // back off so we cannot spin even if the rate limiter
                // itself is the thing failing.
                _logger.ErrorWatchingResources(error);
                try
                {
                    await Task.Delay(TimeSpan.FromSeconds(5), _timeProvider, cancellationToken);
                }
                catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
                {
                    return;
                }
            }
        }
    }

    protected abstract Task<TListResource> RetrieveResourceListAsync(
        string namespaceParameter,
        string? labelSelector,
        string? resourceVersion,
        string? continuationToken,
        CancellationToken cancellationToken);

    protected abstract IAsyncEnumerable<(WatchEventType, TResource)> WatchResourceListAsync(
        string namespaceParameter,
        string? labelSelector,
        string? resourceVersion,
        string? continuationToken,
        Action<Exception> onError,
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
            var list = await RetrieveResourceListAsync(_namespace, _labelSelector, _lastResourceVersion, continueParameter, cancellationToken);

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

    private async Task<WatchCompletion> WatchAsync(CancellationToken cancellationToken)
    {
        using var cts = CancellationTokenSource.CreateLinkedTokenSource(cancellationToken);
        var watchStartedAt = _timeProvider.GetUtcNow();
        var requestLifetimeExceeded = 0;
        using var watchLifetimeTimer = _timeProvider.CreateTimer(
            _ =>
            {
                if (Interlocked.Exchange(ref requestLifetimeExceeded, 1) == 0)
                {
                    _logger.ResourceInformerWatchRequestExceededLifetime((WatchRequestTimeout + WatchRequestTimeoutGracePeriod).TotalSeconds);
                    TryCancel(cts);
                }
            },
            state: null,
            dueTime: WatchRequestTimeout + WatchRequestTimeoutGracePeriod,
            period: Timeout.InfiniteTimeSpan);

        // The k8s client invokes this callback for two distinct kinds of
        // problems, neither of which throws out of the async enumerator:
        //   1. Server-side errors delivered as in-band "Status" events
        //      (e.g. Reason == "Expired" for too-old resourceVersion, or
        //      any other Status the apiserver writes before closing the
        //      stream). These mean the watch is over; we must surface
        //      them so the outer loop can reconnect, and re-list when
        //      the resourceVersion is no longer valid.
        //   2. Per-line deserialization errors (JsonException, etc.). A
        //      single bad line is harmless - the next ReadLineAsync may
        //      well succeed - so we just log and keep enumerating. As a
        //      circuit breaker, if they keep arriving we eventually drop
        //      the connection and reconnect.
        //
        // Transport-level failures (IOException, HttpRequestException,
        // socket resets, TLS errors, ObjectDisposedException, ...) do
        // NOT come through this callback; they are thrown out of the
        // underlying ReadLineAsync and propagate out of the await
        // foreach below, where the outer ExecuteAsync handles them.
        KubernetesException? streamEndedError = null;
        var deserializeErrorCount = 0;
        void OnWatchError(Exception error)
        {
            if (error is KubernetesException kubernetesError)
            {
                streamEndedError = kubernetesError;
                TryCancel(cts);
                return;
            }

            _logger.ErrorWatchingResources(error);

            if (Interlocked.Increment(ref deserializeErrorCount) >= MaxWatchDeserializeErrors)
            {
                // Not fatal in itself, but we shouldn't sit in a loop
                // logging the same parse failure forever. Drop the
                // connection and let the outer loop reconnect.
                TryCancel(cts);
            }
        }

        // begin watching where list left off
        var watchStream = WatchResourceListAsync(_namespace, _labelSelector, _lastResourceVersion, null, OnWatchError, cts.Token);

        var sawEvents = false;
        try
        {
            await foreach (var (watchEventType, item) in watchStream.WithCancellation(cts.Token))
            {
                sawEvents = true;
                await OnEvent(watchEventType, item);
            }
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            // Cancelled either by the in-band Status handler above or by the
            // deserialize-error circuit breaker. In both cases we want to
            // fall through so the outer loop can react: rethrow the Status as
            // a KubernetesException below (forcing a re-list), or simply
            // reconnect.
        }

        if (streamEndedError != null)
        {
            throw streamEndedError;
        }

        if (sawEvents)
        {
            return WatchCompletion.ObservedEvents;
        }

        var watchDuration = _timeProvider.GetUtcNow() - watchStartedAt;
        if (Volatile.Read(ref requestLifetimeExceeded) != 0 || watchDuration >= HealthyEmptyWatchDuration)
        {
            return WatchCompletion.HealthyNoEventClosure;
        }

        return WatchCompletion.SuspiciousEmptyClosure;
    }

    private static void TryCancel(CancellationTokenSource cancellationTokenSource)
    {
        try
        {
            cancellationTokenSource.Cancel();
        }
        catch (ObjectDisposedException)
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

    protected override Task<V1PodList> RetrieveResourceListAsync(string namespaceParameter, string? labelSelector, string? resourceVersion, string? continuationToken, CancellationToken cancellationToken)
    {
        return Client.CoreV1.ListNamespacedPodAsync(
            namespaceParameter,
            labelSelector: labelSelector,
            resourceVersion: resourceVersion,
            continueParameter: continuationToken,
            allowWatchBookmarks: false,
            cancellationToken: cancellationToken);
    }

    protected override IAsyncEnumerable<(WatchEventType, V1Pod)> WatchResourceListAsync(string namespaceParameter, string? labelSelector, string? resourceVersion, string? continuationToken, Action<Exception> onError, CancellationToken cancellationToken)
    {
        return Client.CoreV1.WatchListNamespacedPodAsync(
            namespaceParameter,
            labelSelector: labelSelector,
            resourceVersion: resourceVersion,
            continueParameter: continuationToken,
            timeoutSeconds: (int)WatchRequestTimeout.TotalSeconds,
            allowWatchBookmarks: true,
            onError: onError,
            cancellationToken: cancellationToken);
    }
}
