// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Text;
using Shouldly;
using Tyger.ControlPlane.Logging;
using Xunit;

namespace Tyger.ControlPlane.UnitTests.Logging;

public class DockerTimestampedLogFormatterTests
{
    [Fact]
    public async Task LongLines()
    {
        var input = "2022-04-18T13:34:38.519160930Z " + new string('a', 0x4000) + "2022-04-18T13:34:39.519160930Z " + new string('a', 0x60) + "\n" +
                    "2022-04-18T13:34:40.519160930Z " + new string('b', 0x4000) + "2022-04-18T13:34:40.519160930Z " + new string('b', 0x4000) + "2022-04-18T13:34:41.519160930Z " + new string('b', 0x1);
        var expected = "2022-04-18T13:34:38.519160930Z " + new string('a', 0x4060) + "\n" +
                        "2022-04-18T13:34:40.519160930Z " + new string('b', 0x8001);

        var pipeline = new Pipeline(Encoding.UTF8.GetBytes(input), new DockerTimestampedLogReformatter());
        (await pipeline.ReadAllAsString()).ShouldBe(expected);
    }

    [Fact]
    public async Task LongLinesWithTrailingNewline()
    {
        var input = "2022-04-18T13:34:38.519160930Z " + new string('a', 0x4000) + "2022-04-18T13:34:39.519160930Z " + new string('a', 0x60) + "\n" +
                    "2022-04-18T13:34:40.519160930Z " + new string('b', 0x4000) + "2022-04-18T13:34:40.519160930Z " + new string('b', 0x4000) + "2022-04-18T13:34:41.519160930Z " + new string('b', 0x1) + "\n";
        var expected = "2022-04-18T13:34:38.519160930Z " + new string('a', 0x4060) + "\n" +
                        "2022-04-18T13:34:40.519160930Z " + new string('b', 0x8001) + "\n";

        var pipeline = new Pipeline(Encoding.UTF8.GetBytes(input), new DockerTimestampedLogReformatter());
        (await pipeline.ReadAllAsString()).ShouldBe(expected);
    }
}
