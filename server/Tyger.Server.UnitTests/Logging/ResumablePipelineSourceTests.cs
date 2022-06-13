using System.Buffers;
using System.Globalization;
using System.IO.Pipelines;
using System.Text;
using Microsoft.Extensions.Logging.Abstractions;
using Shouldly;
using Tyger.Server.Logging;
using Xunit;

namespace Tyger.Server.UnitTests.Logging;

public class ResumablePipelineSourceTests
{
    [Fact]
    public async Task InnerSourceInitiallyNull()
    {
        var pipeline = new Pipeline(
            new ResumablePipelineSource(
                o => Task.FromResult<IPipelineSource?>(null),
                new() { IncludeTimestamps = true },
                NullLogger<ResumablePipelineSource>.Instance));

        (await pipeline.ReadAllAsString()).ShouldBe("");
    }

    [Fact]
    public async Task ResumedAfterCompleteLine()
    {
        var pipeline = new Pipeline(
            new ResumablePipelineSource(
                GetCompoundInnerSourceFactory(
                    o => Task.FromResult<IPipelineSource?>(new SimplePipelineSource(new TestPipelineReader("2022-04-14T14:46:43.000000000Z a\n"))),
                    o =>
                    {
                        o.Since.ShouldBe(DateTimeOffset.Parse("2022-04-14T14:46:43.000000000Z", CultureInfo.InvariantCulture));
                        return Task.FromResult<IPipelineSource?>(new SimplePipelineSource(Encoding.UTF8.GetBytes("2022-04-14T14:46:44.000000000Z b")));
                    }),
                new() { IncludeTimestamps = true },
                NullLogger<ResumablePipelineSource>.Instance));

        (await pipeline.ReadAllAsString()).ShouldBe("2022-04-14T14:46:43.000000000Z a\n2022-04-14T14:46:44.000000000Z b");
    }

    [Fact]
    public async Task ResumedAfterIncompleteLine()
    {
        var pipeline = new Pipeline(
            new ResumablePipelineSource(
                GetCompoundInnerSourceFactory(
                    o => Task.FromResult<IPipelineSource?>(new SimplePipelineSource(new TestPipelineReader("2022-04-14T14:46:43.000000000Z a"))),
                    o =>
                    {
                        o.Since.ShouldBe(DateTimeOffset.Parse("2022-04-14T14:46:43.000000000Z", CultureInfo.InvariantCulture));
                        return Task.FromResult<IPipelineSource?>(new SimplePipelineSource(Encoding.UTF8.GetBytes("2022-04-14T14:46:44.000000000Z b")));
                    }),
                new() { IncludeTimestamps = true },
                NullLogger<ResumablePipelineSource>.Instance));

        (await pipeline.ReadAllAsString()).ShouldBe("2022-04-14T14:46:43.000000000Z a\n2022-04-14T14:46:44.000000000Z b");
    }

    [Fact]
    public async Task ResumedCompletesWithNull()
    {
        var pipeline = new Pipeline(
            new ResumablePipelineSource(
                GetCompoundInnerSourceFactory(
                    o => Task.FromResult<IPipelineSource?>(new SimplePipelineSource(new TestPipelineReader("2022-04-14T14:46:43.000000000Z a"))),
                    o =>
                    {
                        o.Since.ShouldBe(DateTimeOffset.Parse("2022-04-14T14:46:43.000000000Z", CultureInfo.InvariantCulture));
                        return Task.FromResult<IPipelineSource?>(null);
                    }),
                new() { IncludeTimestamps = true },
                NullLogger<ResumablePipelineSource>.Instance));

        (await pipeline.ReadAllAsString()).ShouldBe("2022-04-14T14:46:43.000000000Z a");
    }

    private static Func<GetLogsOptions, Task<IPipelineSource?>> GetCompoundInnerSourceFactory(params Func<GetLogsOptions, Task<IPipelineSource?>>[] behaviors)
    {
        int i = -1;
        return async o => await behaviors[++i](o);
    }

    private class TestPipelineReader : PipeReader
    {
        private readonly string _data;
        private int _readCount;

        public TestPipelineReader(string data) => _data = data;

        public override ValueTask<ReadResult> ReadAsync(CancellationToken cancellationToken = default)
        {
            if (_readCount++ == 0)
            {
                return new ValueTask<ReadResult>(new ReadResult(new ReadOnlySequence<byte>(Encoding.UTF8.GetBytes(_data)), false, false));
            }

            throw new IOException("simulated exception");
        }

        public override void AdvanceTo(SequencePosition consumed)
        {
        }

        public override void AdvanceTo(SequencePosition consumed, SequencePosition examined)
        {
        }

        public override void CancelPendingRead() => throw new NotImplementedException();

        public override void Complete(Exception? exception = null)
        {
        }

        public override bool TryRead(out ReadResult result) => throw new NotImplementedException();
    }
}
