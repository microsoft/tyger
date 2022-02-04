using System.ComponentModel.DataAnnotations;
using Shouldly;
using Tyger.Server.Model;
using Xunit;

namespace tyger.server.unittests;

public class ModelValidationTests
{
    private static readonly Codespec ValidCodespec = new Codespec { Image = "abc", Buffers = new(new[] { "a" }, new[] { "b" }) };

    [Fact]
    public void Codespec_Valid() => Validate(ValidCodespec);

    [Fact]
    public void Codespec_MissingImage()
    {
        Should.Throw<ValidationException>(() => Validate(ValidCodespec with { Image = null! }));
        Should.Throw<ValidationException>(() => Validate(ValidCodespec with { Image = "" }));
    }

    [Fact]
    public void Codespec_DuplicatedBuffer()
    {
        Should.Throw<ValidationException>(() => Validate(ValidCodespec with { Buffers = new(new[] { "a" }, new[] { "a" }) }));
    }

    [Fact]
    public void Codespec_EmptyBuffer()
    {
        Should.Throw<ValidationException>(() => Validate(ValidCodespec with { Buffers = new(new[] { "" }, null) }));
        Should.Throw<ValidationException>(() => Validate(ValidCodespec with { Buffers = new(new string[] { null! }, null) }));
    }

    private static void Validate(object o) => Validator.ValidateObject(o, new ValidationContext(o), true);
}
