// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.IO.Pipelines;
using System.Text;
using Shouldly;
using Tyger.ControlPlane.Logging;
using Xunit;

namespace Tyger.ControlPlane.UnitTests.Logging;

public class LogMergerTests
{
    [Fact]
    public async Task MergeFixedStreams()
    {
        var startTime = new DateTimeOffset(2022, 1, 1, 0, 0, 0, 0, TimeSpan.Zero);
        var random = new Random();
        var s1 = GenerateRandomStream(startTime, random, 200, "a");
        var s2 = GenerateRandomStream(startTime, random, 1200, "b");
        var s3 = GenerateRandomStream(startTime, random, 700, "c");

        var logMerger = new FixedLogMerger(CancellationToken.None, new SimplePipelineSource(s1), new SimplePipelineSource(s2), new SimplePipelineSource(s3));
        var s = await logMerger.ReadAllAsString();
        var lines = s.Split("\n", StringSplitOptions.RemoveEmptyEntries);
        lines.Length.ShouldBe(2100);

        lines.ShouldBe(lines.OrderBy(l => l[0..l.IndexOf(' ')]), ignoreOrder: false);
    }

    [Fact]
    public async Task MergeWithoutTrailingNewlines()
    {
        string input1 = "2022-04-18T13:34:38.519160930Z abc";
        string input2 = "2022-04-18T13:34:39.519160930Z def";
        var merger = new LiveLogMerger();
        merger.Activate(CancellationToken.None, new SimplePipelineSource(Encoding.UTF8.GetBytes(input1)), new SimplePipelineSource(Encoding.UTF8.GetBytes(input2)));
        (await merger.ReadAllAsString()).ShouldBe(input1 + "\n" + input2 + "\n");
    }

    [Fact]
    public async Task LiveMergeSingleSource()
    {
        string input = "2022-04-18T13:34:38.519160930Z abc\n2022-04-18T13:34:39.519160930Z def";
        var merger = new LiveLogMerger();
        merger.Activate(CancellationToken.None, new SimplePipelineSource(Encoding.UTF8.GetBytes(input)));
        (await merger.ReadAllAsString()).ShouldBe(input);
    }

    [Fact]
    public async Task LiveMergeStreams()
    {
        var startTime = new DateTimeOffset(2022, 1, 1, 0, 0, 0, 0, TimeSpan.Zero);
        var random = new Random();
        var s1 = GenerateRandomStream(startTime, random, 2700, "a");
        var s2 = GenerateRandomStream(startTime, random, 1200, "b");
        var s3 = GenerateRandomStream(startTime, random, 3000, "c");

        var logMerger = new LiveLogMerger();
        logMerger.Activate(CancellationToken.None, new SimplePipelineSource(s1), new SimplePipelineSource(s2), new SimplePipelineSource(s3));
        var s = await logMerger.ReadAllAsString();
        var lines = s.Split("\n", StringSplitOptions.RemoveEmptyEntries);
        lines.Length.ShouldBe(6900);
    }

    [Fact]
    public async Task LiveMergeWithInitiallyDelayedStreams()
    {
        var startTime = new DateTimeOffset(2022, 1, 1, 0, 0, 0, 0, TimeSpan.Zero);
        var random = new Random();
        var streams = new[]
        {
            GenerateRandomStream(startTime, random, 2700, "a"),
            GenerateRandomStream(startTime, random, 1200, "b"),
            GenerateRandomStream(startTime, random, 3000, "c"),
        };

        var leafMergers = streams.Select(_ => new LiveLogMerger()).ToArray();

        // create a live merger and start it right away, but its inputs have not started yet
        var rootMerger = new LiveLogMerger();
        rootMerger.Activate(CancellationToken.None, leafMergers);

        // now start the sources
        for (int i = 0; i < leafMergers.Length; i++)
        {
            await Task.Delay(1);
            leafMergers[i].Activate(CancellationToken.None, new SimplePipelineSource(streams[i]));
        }

        var s = await rootMerger.ReadAllAsString();
        var lines = s.Split("\n", StringSplitOptions.RemoveEmptyEntries);
        lines.Length.ShouldBe(6900);
    }

    private static PipeReader GenerateRandomStream(DateTimeOffset startingPoint, Random random, int count, string prefix)
    {
        var pipe = new Pipe();

        _ = Write();
        return pipe.Reader;

        async Task Write()
        {
            DateTimeOffset current = startingPoint;
            for (int i = 0; i < count; i++)
            {
                current = current.AddSeconds(random.NextDouble());
                await pipe.Writer.WriteAsync(Encoding.UTF8.GetBytes($"{current:O} {prefix} {i}\n"));
            }

            await pipe.Writer.CompleteAsync();
        }
    }
}
