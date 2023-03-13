using Generator.Equals;
using Shouldly;
using Tyger.Server.Json;
using Xunit;

namespace Tyger.Server.UnitTests.Json;

public partial class NormalizerTests
{
    [Fact]
    public void TestNormalization()
    {
        Normalizer.NormalizeEmptyToNull(new RecWithStrings { RequiredStr = "A" }).ShouldBe(new RecWithStrings { RequiredStr = "A" });
        Normalizer.NormalizeEmptyToNull(new RecWithStrings { RequiredStr = "", NullableStr = "" }).ShouldBe(new RecWithStrings { RequiredStr = "" });
        Normalizer.NormalizeEmptyToNull(new RecWithStrings { RequiredStr = "", NullableStr = "" }).ShouldBe(new RecWithStrings { RequiredStr = "" });

        Normalizer.NormalizeEmptyToNull(new RecordWithCollections { List = new() { "A" }, Dictionary = new() { { "A", "B" } } }).ShouldBe(new RecordWithCollections { List = new() { "A" }, Dictionary = new() { { "A", "B" } } });
        Normalizer.NormalizeEmptyToNull(new RecordWithCollections { List = new() { "A" }, Dictionary = new() }).ShouldBe(new RecordWithCollections { List = new() { "A" }, Dictionary = null });
        Normalizer.NormalizeEmptyToNull(new RecordWithCollections { List = new(), Dictionary = new() { { "A", "B" } } }).ShouldBe(new RecordWithCollections { List = null, Dictionary = new() { { "A", "B" } } });
        Normalizer.NormalizeEmptyToNull(new RecordWithCollections { List = new(), Dictionary = new() }).ShouldBe(null);

        Normalizer.NormalizeEmptyToNull(new RecordWithRecords { InnerRecord1 = new() { List = new() { "A" }, Dictionary = new() { { "A", "B" } } } }).ShouldBe(new RecordWithRecords { InnerRecord1 = new() { List = new() { "A" }, Dictionary = new() { { "A", "B" } } } });
        Normalizer.NormalizeEmptyToNull(new RecordWithRecords { InnerRecord1 = new() { List = new(), Dictionary = new() } }).ShouldBe(null);
        Normalizer.NormalizeEmptyToNull(new RecordWithRecords { InnerRecord1 = new() { List = new(), Dictionary = new() }, InnerRecord2 = new() { List = new() { "a" }, Dictionary = new() } }).ShouldBe(new RecordWithRecords { InnerRecord1 = null, InnerRecord2 = new() { List = new() { "a" } } });
    }

    private record RecWithStrings
    {
        public required string RequiredStr { get; init; }

        public string? NullableStr { get; init; }
    }

    [Equatable]
    private partial record RecordWithCollections
    {
        [OrderedEquality]
        public List<string>? List { get; set; }

        [UnorderedEquality]
        public Dictionary<string, string>? Dictionary { get; set; }
    }

    private record RecordWithRecords
    {
        public RecordWithCollections? InnerRecord1 { get; init; }
        public RecordWithCollections? InnerRecord2 { get; init; }
    }
}
