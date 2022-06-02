using System.Text;
using Shouldly;
using Tyger.Server.Logging;
using Xunit;

namespace Tyger.Server.UnitTests.Logging;

public class TimestampedLogFormatterTests
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
        var pipeline = new Pipeline(Encoding.UTF8.GetBytes(input), new TimestampedLogReformatter());
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

        var pipeline = new Pipeline(Encoding.UTF8.GetBytes(input), new TimestampedLogReformatter());
        (await pipeline.ReadAllAsString()).ShouldBe(input);
    }

    [Fact]
    public async Task LongLines()
    {
        var input = "2022-04-18T13:34:38.519160930Z " + new string('a', 0x4000) + "2022-04-18T13:34:39.519160930Z " + new string('a', 0x60) + "\n" +
                    "2022-04-18T13:34:40.519160930Z " + new string('b', 0x4000) + "2022-04-18T13:34:40.519160930Z " + new string('b', 0x4000) + "2022-04-18T13:34:41.519160930Z " + new string('b', 0x1);
        var expected = "2022-04-18T13:34:38.519160930Z " + new string('a', 0x4060) + "\n" +
                        "2022-04-18T13:34:40.519160930Z " + new string('b', 0x8001);

        var pipeline = new Pipeline(Encoding.UTF8.GetBytes(input), new TimestampedLogReformatter());
        (await pipeline.ReadAllAsString()).ShouldBe(expected);
    }

    [Fact]
    public async Task LongLinesWithTrailingNewline()
    {
        var input = "2022-04-18T13:34:38.519160930Z " + new string('a', 0x4000) + "2022-04-18T13:34:39.519160930Z " + new string('a', 0x60) + "\n" +
                    "2022-04-18T13:34:40.519160930Z " + new string('b', 0x4000) + "2022-04-18T13:34:40.519160930Z " + new string('b', 0x4000) + "2022-04-18T13:34:41.519160930Z " + new string('b', 0x1) + "\n";
        var expected = "2022-04-18T13:34:38.519160930Z " + new string('a', 0x4060) + "\n" +
                        "2022-04-18T13:34:40.519160930Z " + new string('b', 0x8001) + "\n";

        var pipeline = new Pipeline(Encoding.UTF8.GetBytes(input), new TimestampedLogReformatter());
        (await pipeline.ReadAllAsString()).ShouldBe(expected);
    }
}