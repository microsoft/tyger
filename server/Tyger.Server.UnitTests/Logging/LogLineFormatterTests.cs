using System.Text;
using Shouldly;
using Tyger.Server.Logging;
using Xunit;

namespace Tyger.Server.UnitTests.Logging;

public class LogLineFormatterTests
{
    [Theory]
    [InlineData("2022-04-14T14:46:43.948731756Z 1", "2022-04-14T14:46:43.948731756Z [abc] 1")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n", "2022-04-14T14:46:43.948731756Z [abc] 1\n")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756Z 2", "2022-04-14T14:46:43.948731756Z [abc] 1\n2022-04-14T14:46:44.948731756Z [abc] 2")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756Z 2\n", "2022-04-14T14:46:43.948731756Z [abc] 1\n2022-04-14T14:46:44.948731756Z [abc] 2\n")]
    public async Task IncludeTimestampsAndAddContext(string input, string expected)
    {
        var pipeline = new Pipeline(Encoding.UTF8.GetBytes(input), new LogLineFormatter(true, "[abc]"));
        (await pipeline.ReadAllAsString()).ShouldBe(expected);
    }

    [Theory]
    [InlineData("2022-04-14T14:46:43.948731756Z 1", "[abc] 1")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n", "[abc] 1\n")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756Z 2", "[abc] 1\n[abc] 2")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756Z 2\n", "[abc] 1\n[abc] 2\n")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44Z 2\n", "[abc] 1\n[abc] 2\n")]
    public async Task RemoveTimestampsAndAddContext(string input, string expected)
    {
        var pipeline = new Pipeline(Encoding.UTF8.GetBytes(input), new LogLineFormatter(false, "[abc]"));
        (await pipeline.ReadAllAsString()).ShouldBe(expected);
    }
}
