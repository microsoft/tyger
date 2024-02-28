// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Buffers;
using System.IO.Pipelines;
using System.Text;
using Shouldly;
using Tyger.ControlPlane.Logging;
using Xunit;

namespace Tyger.ControlPlane.UnitTests.Logging;

public class TerminablePipelineElementTests
{
    [Theory]
    [InlineData("2022-04-14T14:46:43.948731756Z 1", "2022-04-14T14:46:43.948731756Z 1")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n", "2022-04-14T14:46:43.948731756Z 1\n")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2", "2022-04-14T14:46:43.948731756Z 1\n")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756", "2022-04-14T14:46:43.948731756Z 1\n")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756Z", "2022-04-14T14:46:43.948731756Z 1\n")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756Z 2", "2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756Z 2")]
    public async Task TerminateDoesNotSplitTimestamp(string input, string expected)
    {
        var terminablePipelineElement = new TerminablePipelineElement();
        var reader = new TestPipelineReader(input);
        var pipeline = new Pipeline(reader, terminablePipelineElement);
        _ = Task.Run(async () =>
            {
                await reader.DataRead;
                terminablePipelineElement.Terminate();
            });

        (await pipeline.ReadAllAsString()).ShouldBe(expected);
    }

    private class TestPipelineReader : PipeReader
    {
        private readonly string _data;
        private readonly TaskCompletionSource _dataRead = new();
        private readonly TaskCompletionSource _canceled = new();

        public TestPipelineReader(string data) => _data = data;

        public Task DataRead => _dataRead.Task;

        public override async ValueTask<ReadResult> ReadAsync(CancellationToken cancellationToken = default)
        {
            if (!_dataRead.Task.IsCompleted)
            {
                _dataRead.SetResult();
                return new ReadResult(new ReadOnlySequence<byte>(Encoding.UTF8.GetBytes(_data)), false, false);
            }

            await _canceled.Task;
            return new ReadResult(new ReadOnlySequence<byte>(), isCanceled: true, false);
        }

        public override void AdvanceTo(SequencePosition consumed)
        {
        }

        public override void AdvanceTo(SequencePosition consumed, SequencePosition examined)
        {
        }

        public override void CancelPendingRead() => _canceled.SetResult();

        public override void Complete(Exception? exception = null)
        {
        }

        public override bool TryRead(out ReadResult result) => throw new NotImplementedException();
    }
}
