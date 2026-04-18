// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Text.Json;
using System.Threading.Channels;
using System.Threading.RateLimiting;
using k8s;
using k8s.Models;
using Microsoft.Extensions.Logging.Abstractions;
using Microsoft.Extensions.Time.Testing;
using NSubstitute;
using Shouldly;
using Tyger.ControlPlane.Compute.Kubernetes;
using Xunit;

namespace Tyger.ControlPlane.UnitTests.Compute.Kubernetes;

public class ResourceInformerTests
{
    [Fact]
    public async Task InitialList_EmitsToInitialChannelAndCompletes()
    {
        var fixture = new InformerFixture
        {
            NextList = Pods("rv-1", ("a", "1"), ("b", "1")),
            NextWatchEvents = Array.Empty<(WatchEventType, V1Pod)>()
        };

        await fixture.RunUntilAsync(async () =>
        {
            var pods = new List<V1Pod>();
            await foreach (var pod in fixture.InitialReader.ReadAllAsync())
            {
                pods.Add(pod);
            }

            pods.Select(p => p.Name()).ShouldBe(["a", "b"]);
        });
    }

    [Fact]
    public async Task WatchEvents_FlowToUpdatesChannel_AndBookmarkAdvancesResourceVersion()
    {
        var fixture = new InformerFixture
        {
            NextList = Pods("rv-1"),
            NextWatchEvents = new[]
            {
                (WatchEventType.Added, Pod("a", "2")),
                (WatchEventType.Modified, Pod("a", "3")),
                (WatchEventType.Bookmark, Pod(name: null, "rv-bookmark")),
                (WatchEventType.Deleted, Pod("a", "4")),
            }
        };

        await fixture.RunUntilAsync(async () =>
        {
            await fixture.InitialReader.Completion;

            var observed = new List<(WatchEventType, string?)>();
            for (int i = 0; i < 3; i++)
            {
                var (t, pod) = await fixture.UpdatesReader.ReadAsync();
                observed.Add((t, pod.Name()));
            }

            observed.ShouldBe(new[]
            {
                (WatchEventType.Added, (string?)"a"),
                (WatchEventType.Modified, (string?)"a"),
                (WatchEventType.Deleted, (string?)"a"),
            });

            // Wait for the next watch reconnect; it should use the bookmark's RV.
            await fixture.WaitForWatchCountAsync(2);
        });

        // Initial watch used the list's RV; subsequent watches use the bookmark.
        fixture.WatchResourceVersions[0].ShouldBe("rv-1");
        fixture.WatchResourceVersions[1].ShouldBe("rv-bookmark");
        // No re-list was needed.
        fixture.ListCalls.Count.ShouldBe(1);
    }

    [Fact]
    public async Task KubernetesExceptionFromWatchEnumerator_ForcesReList()
    {
        var fixture = new InformerFixture
        {
            NextList = Pods("rv-1"),
            NextWatchEvents = new Func<IAsyncEnumerable<(WatchEventType, V1Pod)>>(() =>
                    ThrowingStream(new KubernetesException(new V1Status { Reason = "Expired" })))
        };

        await fixture.RunUntilAsync(async () =>
        {
            await fixture.InitialReader.Completion;

            // Wait until we see a second list call (re-list after Expired).
            await fixture.WaitForListCountAsync(2);
        });

        fixture.ListCalls[1].ResourceVersion.ShouldBeNull();
    }

    [Fact]
    public async Task OnError_WithKubernetesException_ForcesReList()
    {
        var fixture = new InformerFixture
        {
            NextList = Pods("rv-1"),
            NextWatchEvents = new Func<Action<Exception>, IAsyncEnumerable<(WatchEventType, V1Pod)>>(onError =>
                    DeliverErrorAndComplete(onError, new KubernetesException(new V1Status { Reason = "Gone" })))
        };

        await fixture.RunUntilAsync(async () =>
        {
            await fixture.InitialReader.Completion;
            await fixture.WaitForListCountAsync(2);
        });

        fixture.ListCalls[1].ResourceVersion.ShouldBeNull();
    }

    [Fact]
    public async Task OnError_WithRepeatedDeserializeErrors_DropsConnectionWithoutReList()
    {
        var fixture = new InformerFixture
        {
            NextList = Pods("rv-1")
        };

        // 1st watch floods deserialize errors (circuit breaker should fire),
        // 2nd watch immediately yields a real event so the test can observe
        // that the reconnect happened without a re-list.
        var watchCount = 0;
        fixture.NextWatchEvents = new Func<Action<Exception>, IAsyncEnumerable<(WatchEventType, V1Pod)>>(onError =>
        {
            var current = Interlocked.Increment(ref watchCount);
            return current == 1
                ? FloodErrorsAndStallForever(onError, new JsonException("bad"), count: 20)
                : SingleEventStream(WatchEventType.Added, Pod("a", "2"));
        });

        await fixture.RunUntilAsync(async () =>
        {
            await fixture.InitialReader.Completion;
            // Wait for the post-flood reconnect to deliver its event.
            var (t, _) = await fixture.UpdatesReader.ReadAsync();
            t.ShouldBe(WatchEventType.Added);
        });

        // Crucially, the circuit breaker did NOT trigger a re-list.
        fixture.ListCalls.Count.ShouldBe(1);
        fixture.WatchCallCount.ShouldBeGreaterThanOrEqualTo(2);
    }

    [Fact]
    public async Task FiveConsecutiveEmptyWatches_ForceReList()
    {
        var fixture = new InformerFixture
        {
            NextList = Pods("rv-1"),
            NextWatchEvents = Array.Empty<(WatchEventType, V1Pod)>() // every watch returns immediately with no events
        };

        await fixture.RunUntilAsync(async () =>
        {
            await fixture.InitialReader.Completion;
            await fixture.WaitForListCountAsync(2);
        });

        // After 5 empty watches, the second list call should drop the rv.
        fixture.ListCalls[1].ResourceVersion.ShouldBeNull();
        fixture.WatchCallsBeforeListCount(2).ShouldBeGreaterThanOrEqualTo(5);
    }

    [Fact]
    public async Task LongLivedEmptyWatches_DoNotForceReList()
    {
        var time = new FakeTimeProvider();
        var fixture = new InformerFixture(time)
        {
            NextList = Pods("rv-1"),
            NextWatchEvents = new Func<IAsyncEnumerable<(WatchEventType, V1Pod)>>(() => DelayThenComplete(time, TimeSpan.FromMinutes(1)))
        };

        await fixture.RunUntilAsync(async () =>
        {
            await fixture.InitialReader.Completion;
            await fixture.WaitForWatchCountAsync(1);

            for (int i = 0; i < 6; i++)
            {
                time.Advance(TimeSpan.FromMinutes(1));
                await Task.Yield();
                await fixture.WaitForWatchCountAsync(i + 2);
            }

            fixture.ListCalls.Count.ShouldBe(1);
            fixture.WatchCallCount.ShouldBeGreaterThanOrEqualTo(7);
        });
    }

    [Fact]
    public async Task WatchRequestTimeout_ReconnectsWithoutReList()
    {
        var time = new FakeTimeProvider();
        var fixture = new InformerFixture(time)
        {
            NextList = Pods("rv-1"),
            NextWatchEvents = new Func<IAsyncEnumerable<(WatchEventType, V1Pod)>>(() => StallForeverStream(default))
        };

        await fixture.RunUntilAsync(async () =>
        {
            await fixture.InitialReader.Completion;
            await fixture.WaitForWatchCountAsync(1);

            time.Advance(TimeSpan.FromMinutes(6));
            await Task.Yield();

            await fixture.WaitForWatchCountAsync(2);

            fixture.ListCalls.Count.ShouldBe(1);
            fixture.WatchResourceVersions[1].ShouldBe("rv-1");
        });
    }

    private static V1PodList Pods(string resourceVersion, params (string Name, string Rv)[] items)
    {
        return new V1PodList
        {
            Metadata = new V1ListMeta { ResourceVersion = resourceVersion },
            Items = items.Select(i => Pod(i.Name, i.Rv)).ToList(),
        };
    }

    private static V1Pod Pod(string? name, string resourceVersion)
    {
        return new V1Pod
        {
            Metadata = new V1ObjectMeta
            {
                Name = name,
                ResourceVersion = resourceVersion,
            },
        };
    }

    private static async IAsyncEnumerable<(WatchEventType, V1Pod)> ThrowingStream(Exception ex)
    {
        await Task.Yield();
        throw ex;
#pragma warning disable CS0162 // unreachable
        yield break;
#pragma warning restore CS0162
    }

    private static async IAsyncEnumerable<(WatchEventType, V1Pod)> SingleEventStream(
        WatchEventType type,
        V1Pod pod,
        [System.Runtime.CompilerServices.EnumeratorCancellation] CancellationToken ct = default)
    {
        await Task.Yield();
        yield return (type, pod);

        try
        {
            await Task.Delay(Timeout.Infinite, ct);
        }
        catch (OperationCanceledException)
        {
        }
    }

    private static async IAsyncEnumerable<(WatchEventType, V1Pod)> StallForeverStream(
        [System.Runtime.CompilerServices.EnumeratorCancellation] CancellationToken ct = default)
    {
        try
        {
            await Task.Delay(Timeout.Infinite, ct);
        }
        catch (OperationCanceledException)
        {
        }

        yield break;
    }

    private static async IAsyncEnumerable<(WatchEventType, V1Pod)> DelayThenComplete(
        TimeProvider timeProvider,
        TimeSpan delay,
        [System.Runtime.CompilerServices.EnumeratorCancellation] CancellationToken ct = default)
    {
        try
        {
            await Task.Delay(delay, timeProvider, ct);
        }
        catch (OperationCanceledException)
        {
        }

        yield break;
    }

    private static async IAsyncEnumerable<(WatchEventType, V1Pod)> DeliverErrorAndComplete(
        Action<Exception> onError,
        Exception error,
        [System.Runtime.CompilerServices.EnumeratorCancellation] CancellationToken ct = default)
    {
        await Task.Yield();
        onError(error);
        // The real k8s enumerator returns normally after invoking onError for
        // a Status event (the server typically closes the stream right after).
        // Honour cancellation requested by OnWatchError before yielding break.
        try
        {
            await Task.Delay(Timeout.Infinite, ct);
        }
        catch (OperationCanceledException)
        {
        }

        yield break;
    }

    private static async IAsyncEnumerable<(WatchEventType, V1Pod)> FloodErrorsAndStallForever(
        Action<Exception> onError,
        Exception error,
        int count,
        [System.Runtime.CompilerServices.EnumeratorCancellation] CancellationToken ct = default)
    {
        await Task.Yield();
        for (int i = 0; i < count && !ct.IsCancellationRequested; i++)
        {
            onError(error);
        }

        try
        {
            await Task.Delay(Timeout.Infinite, ct);
        }
        catch (OperationCanceledException)
        {
        }

        yield break;
    }

    private sealed class InformerFixture
    {
        private readonly Channel<V1Pod> _initial = Channel.CreateUnbounded<V1Pod>();
        private readonly Channel<(WatchEventType, V1Pod)> _updates = Channel.CreateUnbounded<(WatchEventType, V1Pod)>();
        private readonly TestableInformer _informer;
        private readonly object _gate = new();

        public InformerFixture(TimeProvider? time = null)
        {
            _informer = new TestableInformer(
                Substitute.For<IKubernetes>(),
                "default",
                "label=val",
                _initial.Writer,
                _updates.Writer,
                NullLogger.Instance,
                this,
                time ?? TimeProvider.System,
                new NoopRateLimiter());
        }

        public ChannelReader<V1Pod> InitialReader => _initial.Reader;
        public ChannelReader<(WatchEventType, V1Pod)> UpdatesReader => _updates.Reader;

        public List<(string? ResourceVersion, string? Continue)> ListCalls { get; } = [];
        public List<string?> WatchResourceVersions { get; } = [];
        public int WatchCallCount { get; private set; }

        public V1PodList NextList { get; set; } = new V1PodList { Metadata = new(), Items = [] };

        // Either Func<IAsyncEnumerable<...>>, Func<Action<Exception>,IAsyncEnumerable<...>>, or IEnumerable<(WatchEventType,V1Pod)>.
        public object NextWatchEvents { get; set; } = Array.Empty<(WatchEventType, V1Pod)>();

        public int WatchCallsBeforeListCount(int listCount)
        {
            lock (_gate)
            {
                return _watchesByListIndex.Count >= listCount ? _watchesByListIndex[listCount - 1] : WatchCallCount;
            }
        }

        public async Task WaitForListCountAsync(int target, TimeSpan? timeout = null)
        {
            timeout ??= TimeSpan.FromSeconds(5);
            var deadline = DateTime.UtcNow + timeout.Value;
            while (DateTime.UtcNow < deadline)
            {
                lock (_gate)
                {
                    if (ListCalls.Count >= target)
                    {
                        return;
                    }
                }

                await Task.Delay(20);
            }

            throw new TimeoutException($"Timed out waiting for {target} list calls; saw {ListCalls.Count}.");
        }

        public async Task WaitForWatchCountAsync(int target, TimeSpan? timeout = null)
        {
            timeout ??= TimeSpan.FromSeconds(5);
            var deadline = DateTime.UtcNow + timeout.Value;
            while (DateTime.UtcNow < deadline)
            {
                lock (_gate)
                {
                    if (WatchCallCount >= target)
                    {
                        return;
                    }
                }

                await Task.Delay(20);
            }

            throw new TimeoutException($"Timed out waiting for {target} watch calls; saw {WatchCallCount}.");
        }

        public async Task RunUntilAsync(Func<Task> assertion)
        {
            using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(10));
            // Task.Run so the informer's hot loop doesn't monopolize xUnit's
            // single-threaded synchronization context.
            var task = Task.Run(() => _informer.ExecuteAsync(cts.Token));

            try
            {
                await assertion();
            }
            finally
            {
                cts.Cancel();
                await Task.WhenAny(task, Task.Delay(TimeSpan.FromSeconds(2)));
                if (task.IsFaulted)
                {
                    await task; // surface unexpected informer failures
                }
            }
        }

        private readonly List<int> _watchesByListIndex = [];

        internal Task<V1PodList> RecordList(string? resourceVersion, string? continueParameter)
        {
            lock (_gate)
            {
                ListCalls.Add((resourceVersion, continueParameter));
                _watchesByListIndex.Add(WatchCallCount);
            }

            return Task.FromResult(NextList);
        }

        internal IAsyncEnumerable<(WatchEventType, V1Pod)> RecordWatch(string? resourceVersion, Action<Exception> onError)
        {
            lock (_gate)
            {
                WatchCallCount++;
                WatchResourceVersions.Add(resourceVersion);
            }

            return NextWatchEvents switch
            {
                Func<IAsyncEnumerable<(WatchEventType, V1Pod)>> f => f(),
                Func<Action<Exception>, IAsyncEnumerable<(WatchEventType, V1Pod)>> g => g(onError),
                IEnumerable<(WatchEventType, V1Pod)> events => ToAsync(events),
                _ => throw new InvalidOperationException("Unsupported NextWatchEvents value."),
            };
        }

        private static async IAsyncEnumerable<(WatchEventType, V1Pod)> ToAsync(IEnumerable<(WatchEventType, V1Pod)> events)
        {
            foreach (var e in events)
            {
                yield return e;
            }

            await Task.CompletedTask;
        }
    }

    private sealed class TestableInformer : ResourceInformer<V1Pod, V1PodList>
    {
        private readonly InformerFixture _fixture;

        public TestableInformer(
            IKubernetes client,
            string @namespace,
            string labelSelector,
            ChannelWriter<V1Pod> initial,
            ChannelWriter<(WatchEventType, V1Pod)> updates,
            Microsoft.Extensions.Logging.ILogger logger,
            InformerFixture fixture,
            TimeProvider time,
            RateLimiter limiter)
            : base(client, @namespace, labelSelector, initial, updates, logger, time, limiter)
        {
            _fixture = fixture;
        }

        protected override Task<V1PodList> RetrieveResourceListAsync(
            string namespaceParameter,
            string? labelSelector,
            string? resourceVersion,
            string? continuationToken,
            CancellationToken cancellationToken)
        {
            return _fixture.RecordList(resourceVersion, continuationToken);
        }

        protected override IAsyncEnumerable<(WatchEventType, V1Pod)> WatchResourceListAsync(
            string namespaceParameter,
            string? labelSelector,
            string? resourceVersion,
            string? continuationToken,
            Action<Exception> onError,
            CancellationToken cancellationToken)
        {
            return _fixture.RecordWatch(resourceVersion, onError);
        }
    }

    private sealed class NoopRateLimiter : RateLimiter
    {
        public override TimeSpan? IdleDuration => null;

        public override RateLimiterStatistics? GetStatistics() => null;

        protected override RateLimitLease AttemptAcquireCore(int permitCount) => new NoopLease();

        protected override ValueTask<RateLimitLease> AcquireAsyncCore(int permitCount, CancellationToken cancellationToken)
            => ValueTask.FromResult<RateLimitLease>(new NoopLease());

        private sealed class NoopLease : RateLimitLease
        {
            public override bool IsAcquired => true;
            public override IEnumerable<string> MetadataNames => [];
            public override bool TryGetMetadata(string metadataName, out object? metadata)
            {
                metadata = null;
                return false;
            }
        }
    }
}
