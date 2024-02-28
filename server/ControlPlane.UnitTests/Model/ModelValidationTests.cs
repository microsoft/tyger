// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Shouldly;
using Tyger.ControlPlane.Model;
using Xunit;

namespace Tyger.ControlPlane.UnitTests.Model;

public class ModelValidationTests
{
    private static readonly JobCodespec s_validCodespec = new() { Image = "abc", Buffers = new(new[] { "a" }, new[] { "b" }) };

    [Fact]
    public void Codespec_Valid() => Validate(s_validCodespec);

    [Fact]
    public void Codespec_MissingImage()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Image = null! }));
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Image = "" }));
    }

    [Fact]
    public void Codespec_DuplicatedBuffer()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Buffers = new(new[] { "a" }, new[] { "a" }) }));
    }

    [Fact]
    public void Codespec_EmptyBuffer()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Buffers = new(new[] { "" }, null) }));
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Buffers = new(new string[] { null! }, null) }));
    }

    private static void Validate(object o) => Validator.ValidateObject(o, new ValidationContext(o), true);
}
