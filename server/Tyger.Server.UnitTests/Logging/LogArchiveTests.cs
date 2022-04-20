using System.Globalization;
using System.IO.Pipelines;
using System.Text;
using Shouldly;
using Tyger.Server.Logging;
using Xunit;

namespace Tyger.Server.UnitTests.Logging;

public class LogArchiveTests
{
    [Theory]
    [InlineData("2022-04-14T14:46:43.948731756Z 1")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756Z 2")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756Z 2\n")]
    public async Task Passthrough(string input)
    {
        using var inputStream = new MemoryStream(Encoding.UTF8.GetBytes(input));
        using var outputStream = new MemoryStream();
        var writer = PipeWriter.Create(outputStream);
        await LogArchive.WriteFilteredLogStream(inputStream, true, 0, null, writer, CancellationToken.None);

        outputStream.Position = 0;
        new StreamReader(outputStream).ReadToEnd().ShouldBe(input);
    }

    [Theory]
    [InlineData("2022-04-14T14:46:43.948731756Z 1", "1")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n", "1\n")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756Z 2", "1\n2")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44.948731756Z 2\n", "1\n2\n")]
    [InlineData("2022-04-14T14:46:43.948731756Z 1\n2022-04-14T14:46:44Z 2\n", "1\n2\n")]
    public async Task RemoveTimestamps(string input, string expected)
    {
        using var inputStream = new MemoryStream(Encoding.UTF8.GetBytes(input));
        using var outputStream = new MemoryStream();
        var writer = PipeWriter.Create(outputStream);
        await LogArchive.WriteFilteredLogStream(inputStream, false, 0, null, writer, CancellationToken.None);

        outputStream.Position = 0;
        new StreamReader(outputStream).ReadToEnd().ShouldBe(expected);
    }

    [Theory]
    [InlineData(0, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9\n")]
    [InlineData(1, "1\n2\n3\n4\n5\n6\n7\n8\n9\n")]
    [InlineData(9, "9\n")]
    [InlineData(10, "")]
    public async Task SkipLinesAndRemoveTimestamps(int skipLines, string expected)
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
        using var inputStream = new MemoryStream(Encoding.UTF8.GetBytes(input));
        using var outputStream = new MemoryStream();
        var writer = PipeWriter.Create(outputStream);
        await LogArchive.WriteFilteredLogStream(inputStream, false, skipLines, null, writer, CancellationToken.None);

        outputStream.Position = 0;
        new StreamReader(outputStream).ReadToEnd().ShouldBe(expected);
    }

    [Theory]
    [InlineData("2022-04-14T16:22:16Z", "0\n1\n2\n3\n4\n5\n6\n7\n8\n9\n")]
    [InlineData("2022-04-14T16:22:17.9Z", "1\n2\n3\n4\n5\n6\n7\n8\n9\n")]
    [InlineData("2023-09-24T16:22:17.5Z", "")]
    public async Task SinceAndRemoveTimestamps(string since, string expected)
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
        using var inputStream = new MemoryStream(Encoding.UTF8.GetBytes(input));
        using var outputStream = new MemoryStream();
        var writer = PipeWriter.Create(outputStream);
        await LogArchive.WriteFilteredLogStream(inputStream, false, 0, DateTimeOffset.Parse(since, CultureInfo.InvariantCulture), writer, CancellationToken.None);

        outputStream.Position = 0;
        new StreamReader(outputStream).ReadToEnd().ShouldBe(expected);
    }
}
