using System.IO.Pipelines;
using NSubstitute;
using Shouldly;
using Tyger.Server.Logging;
using Xunit;

namespace Tyger.Server.UnitTests.Logging;

public class PipelineTests
{
    [Fact]
    public async Task PipelineOfPassthroughElements()
    {
        var inputBuf = new byte[2 * 1024 * 1024];
        new Random().NextBytes(inputBuf);
        var source = new MemoryStream(inputBuf);
        var pipeline = new Pipeline(source, new PassthroughElement(), new PassthroughElement(), new PassthroughElement(), new PassthroughElement(), new PassthroughElement(), new PassthroughElement());
        var sink = new MemoryStream();
        var writer = PipeWriter.Create(sink);
        await pipeline.Process(writer, CancellationToken.None);

        Should.Throw<ObjectDisposedException>(() => source.ReadByte(), "source stream should have been closed");
        Should.Throw<ObjectDisposedException>(() => sink.ReadByte(), "sink stream should have been closed");

        sink.ToArray().SequenceEqual(inputBuf).ShouldBeTrue();
    }

    [Fact]
    public async Task WhenPipelineElementFailsExceptionPropagatesAndStreamsAreClosed()
    {
        var disposedCompletionSource = new TaskCompletionSource();
        var source = Substitute.ForPartsOf<Stream>();
        source.When(s => s.Dispose()).Do(_ => disposedCompletionSource.SetResult());
        var pipeline = new Pipeline(source, new PassthroughElement(), new FailingElement(), new PassthroughElement(), new PassthroughElement(), new PassthroughElement(), new PassthroughElement(), new PassthroughElement());
        var sink = new MemoryStream();
        var writer = PipeWriter.Create(sink);
        await Should.ThrowAsync<DivideByZeroException>(async () => await pipeline.Process(writer, CancellationToken.None));
        (await Task.WhenAny(disposedCompletionSource.Task, Task.Delay(TimeSpan.FromSeconds(30)))).ShouldBe(disposedCompletionSource.Task, "input stream not disposed");
        sink.CanWrite.ShouldBeFalse();
    }

    [Fact]
    public async Task SimpleSourceReaderIsClosedAfterProcess()
    {
        var inputBuf = new byte[] { 1, 2, 3 };
        var inputStream = new MemoryStream(inputBuf);
        var source = new SimplePipelineSource(inputStream);
        var pipeline = new Pipeline(source);
        await pipeline.ReadAllAsString();

        Should.Throw<ObjectDisposedException>(() => inputStream.ReadByte(), "source stream should have been closed");
    }

    [Fact]
    public async Task SimpleSourceReaderIsClosedAfterProcessThrows()
    {
        var stream = Substitute.ForPartsOf<Stream>();
#pragma warning disable CA2012 // Use ValueTasks correctly. We use them incorrectly to program the mock.
        stream.ReadAsync(Arg.Any<Memory<byte>>(), Arg.Any<CancellationToken>()).Returns(ValueTask.FromException<int>(new DivideByZeroException()));
#pragma warning restore CA2012

        var pipeline = new Pipeline(stream);
        await Should.ThrowAsync<DivideByZeroException>(pipeline.ReadAllAsString);
        stream.Received().Dispose();
    }

    private class PassthroughElement : IPipelineElement
    {
        public async Task Process(PipeReader reader, PipeWriter writer, CancellationToken cancellationToken)
        {
            await reader.CopyToAsync(writer, cancellationToken);
        }
    }

    private class FailingElement : IPipelineElement
    {
        public async Task Process(PipeReader reader, PipeWriter writer, CancellationToken cancellationToken)
        {
            await Task.CompletedTask;
            throw new DivideByZeroException();
        }
    }
}
