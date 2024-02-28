// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Text;
using Shouldly;
using Tyger.ControlPlane.Logging;
using Xunit;

namespace Tyger.ControlPlane.UnitTests.Logging;

public class LineCountingReadStreamTests
{
    [Theory]
    [InlineData("", 0)]
    [InlineData("\n", 1)]
    [InlineData("\n\n", 2)]
    [InlineData("\n\n\n", 3)]
    [InlineData("a", 1)]
    [InlineData("a\n", 1)]
    [InlineData("a\n\n", 2)]
    [InlineData("a\n\n\n", 3)]
    [InlineData("a\nb", 2)]
    [InlineData("a\nb\n", 2)]
    [InlineData("a\nb\n\n", 3)]
    [InlineData("a\nb\n\nc", 4)]
    [InlineData("a\nb\n\nc\n", 4)]
    public void LinesCounted(string s, int expectedCount)
    {
        using var ms = new MemoryStream(Encoding.UTF8.GetBytes(s));
        var lcs = new LineCountingReadStream(ms);
        new StreamReader(lcs).ReadToEnd().ShouldBe(s);
        lcs.LineCount.ShouldBe(expectedCount);
    }
}
