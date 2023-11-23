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

        Normalizer.NormalizeEmptyToNull(new RecordWithCollections { List = ["A"], Dictionary = new() { { "A", "B" } } }).ShouldBe(new RecordWithCollections { List = ["A"], Dictionary = new() { { "A", "B" } } });
        Normalizer.NormalizeEmptyToNull(new RecordWithCollections { List = ["A"], Dictionary = [] }).ShouldBe(new RecordWithCollections { List = ["A"], Dictionary = null });
        Normalizer.NormalizeEmptyToNull(new RecordWithCollections { List = [], Dictionary = new() { { "A", "B" } } }).ShouldBe(new RecordWithCollections { List = null, Dictionary = new() { { "A", "B" } } });
        Normalizer.NormalizeEmptyToNull(new RecordWithCollections { List = [], Dictionary = [] }).ShouldBe(null);

        Normalizer.NormalizeEmptyToNull(new RecordWithRecords { InnerRecord1 = new() { List = ["A"], Dictionary = new() { { "A", "B" } } } }).ShouldBe(new RecordWithRecords { InnerRecord1 = new() { List = ["A"], Dictionary = new() { { "A", "B" } } } });
        Normalizer.NormalizeEmptyToNull(new RecordWithRecords { InnerRecord1 = new() { List = [], Dictionary = [] } }).ShouldBe(null);
        Normalizer.NormalizeEmptyToNull(new RecordWithRecords { InnerRecord1 = new() { List = [], Dictionary = [] }, InnerRecord2 = new() { List = ["a"], Dictionary = [] } }).ShouldBe(new RecordWithRecords { InnerRecord1 = null, InnerRecord2 = new() { List = ["a"] } });
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
