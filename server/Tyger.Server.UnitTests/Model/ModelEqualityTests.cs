// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Shouldly;
using Tyger.Server.Model;
using Xunit;

namespace Tyger.Server.UnitTests.Model;

public class ModelEqualityTests
{
    [Fact]
    public void TestCodespecEquality()
    {
        var a = new JobCodespec { Image = "image", Args = new[] { "a", "b", "c" } };
        (a with { Args = new[] { "a", "b", "c" } }).ShouldBe(a);
        (a with { Args = new[] { "b", "c", "A" } }).ShouldNotBe(a);

        a = new() { Image = "image", Command = new[] { "a", "b", "c" } };
        (a with { Command = new[] { "b", "c", "A" } }).ShouldNotBe(a);
        (a with { Command = new[] { "a", "b", "c" } }).ShouldBe(a);

        a = new() { Image = "image", Env = new() { { "a", "A" }, { "b", "B" } } };
        (a with { Env = new() { { "b", "B" }, { "a", "A" } } }).ShouldBe(a);
        (a with { Env = new() { { "b", "b" }, { "a", "a" } } }).ShouldNotBe(a);

        a = new() { Image = "image", Buffers = new(new[] { "a", "b", "c" }, new[] { "A", "B", "C" }) };
        (a with { Buffers = new(new[] { "c", "b", "a" }, new[] { "C", "B", "A" }) }).ShouldBe(a);
        (a with { Buffers = new(new[] { "C", "b", "a" }, new[] { "C", "B", "A" }) }).ShouldNotBe(a);
        (a with { Buffers = new(new[] { "c", "b", "a" }, new[] { "c", "B", "A" }) }).ShouldNotBe(a);

        a = new() { Image = "image", Resources = new() { Requests = new() { Cpu = new("1") } } };
        (a with { Resources = new() { Requests = new() { Cpu = new("1") } } }).ShouldBe(a);
        (a with { Resources = new() { Requests = new() { Cpu = new("1000m") } } }).ShouldBe(a);
        (a with { Resources = new() { Requests = new() { Cpu = new("10001m") } } }).ShouldNotBe(a);
        (a with { Resources = new() { Limits = new() { Cpu = new("1") } } }).ShouldNotBe(a);

        a = new() { Image = "i1" };
        (a with { Image = "i1" }).ShouldBe(a);
        (a with { Image = "i2" }).ShouldNotBe(a);
    }
}
