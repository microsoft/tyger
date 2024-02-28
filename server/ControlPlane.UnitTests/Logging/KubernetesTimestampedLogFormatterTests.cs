// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Text;
using Shouldly;
using Tyger.ControlPlane.Logging;
using Xunit;

namespace Tyger.ControlPlane.UnitTests.Logging;

public class KubernetesTimestampedLogFormatterTests
{
    [Fact]
    public async Task NormalLines()
    {
        var input = @"2022-04-14T16:22:17.803090288Z 0
2022-04-14T16:22:18.803090288Z 1
2022-04-14T16:22:19.803090288Z 2
2022-04-14T16:22:20.803090288Z 3
2022-04-14T16:22:21.803090288Z 4
2022-04-14T16:22:22.803090288Z 5
2022-04-14T16:22:23.803090288Z 6
2022-04-14T16:22:24.803090288Z 7
2022-04-14T16:22:25.803090288Z 8
2022-04-14T16:22:26.803090288Z 9
";
        using var ms = new MemoryStream(Encoding.UTF8.GetBytes(input));
        var pipeline = new Pipeline(Encoding.UTF8.GetBytes(input), new KubernetesTimestampedLogReformatter());
        (await pipeline.ReadAllAsString()).ShouldBe(input);
    }

    [Fact]
    public async Task ManyNormalLines()
    {
        var seed = @"2022-04-14T16:22:17.803090288Z 0
2022-04-14T16:22:18.803090288Z 1
2022-04-14T16:22:19.803090288Z 2
2022-04-14T16:22:20.803090288Z 3
2022-04-14T16:22:21.803090288Z 4
2022-04-14T16:22:22.803090288Z 5
2022-04-14T16:22:23.803090288Z 6
2022-04-14T16:22:24.803090288Z 7
2022-04-14T16:22:25.803090288Z 8
2022-04-14T16:22:26.803090288Z 9
";

        var sb = new StringBuilder();
        for (int i = 0; i < 1000; i++)
        {
            sb.Append(seed);
        }

        var input = sb.ToString();

        var pipeline = new Pipeline(Encoding.UTF8.GetBytes(input), new KubernetesTimestampedLogReformatter());
        (await pipeline.ReadAllAsString()).ShouldBe(input);
    }

    [Fact]
    public async Task LineWithoutTimestamp()
    {
        var input = @"unable to retrieve container logs for containerd://90cf2459f602ed64a3898dba30f698b6abf8e11f3ab0d12f1a570a9e7ce213d3";
        var expected = @"0001-01-01T00:00:00.000000000Z " + input;
        using var ms = new MemoryStream(Encoding.UTF8.GetBytes(input));
        var pipeline = new Pipeline(Encoding.UTF8.GetBytes(input), new KubernetesTimestampedLogReformatter());
        (await pipeline.ReadAllAsString()).ShouldBe(expected);
    }
}
