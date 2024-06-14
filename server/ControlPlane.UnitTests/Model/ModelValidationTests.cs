// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Shouldly;
using Tyger.ControlPlane.Model;
using Xunit;

namespace Tyger.ControlPlane.UnitTests.Model;

public class ModelValidationTests
{
    private static readonly JobCodespec s_validCodespec = new() { Image = "abc", Buffers = new(["a"], ["b"]) };

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
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Buffers = new(["a"], ["a"]) }));
    }

    [Fact]
    public void Codespec_EmptyBuffer()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Buffers = new([""], null) }));
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Buffers = new([null!], null) }));
    }

    [Fact]
    public void ValidSocket()
    {
        Validate(s_validCodespec with { Sockets = [new() { Port = 80, InputBuffer = "a", OutputBuffer = "b" }] });
    }

    [Fact]
    public void SocketThatHasInvalidPortNumber()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Sockets = [new() { Port = -1 }] }));
    }

    [Fact]
    public void SocketThatHasInvalidInputBuffer()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Sockets = [new() { Port = 80, InputBuffer = "missing" }] }));
    }

    [Fact]
    public void SocketThatHasInputBufferWithWrongDirectionality()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Sockets = [new() { Port = 80, InputBuffer = "b" }] }));
    }

    [Fact]
    public void SocketThatHasOutputBufferWithWrongDirectionality()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Sockets = [new() { Port = 80, OutputBuffer = "a" }] }));
    }

    [Fact]
    public void SocketThatHasInvalidOutputBuffer()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Sockets = [new() { Port = 80, OutputBuffer = "missing" }] }));
    }

    [Fact]
    public void SocketThatHasNoBuffers()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Sockets = [new() { Port = 80 }] }));
    }

    [Fact]
    public void SocketsThatReferenceSameBuffer()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Sockets = [new() { Port = 80, InputBuffer = "a" }, new() { Port = 80, InputBuffer = "a" }] }));
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Sockets = [new() { Port = 80, OutputBuffer = "b" }, new() { Port = 80, OutputBuffer = "b" }] }));
    }

    [Fact]
    public void EnvVarReferenceToBufferUsedBySocket()
    {
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Sockets = [new() { Port = 80, InputBuffer = "a" }], Args = ["$(A_PIPE)"] }));
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Sockets = [new() { Port = 80, InputBuffer = "a" }], Command = ["$(B_PIPE) $(A_PIPE)"] }));
        Should.Throw<ValidationException>(() => Validate(s_validCodespec with { Sockets = [new() { Port = 80, InputBuffer = "a" }], Env = new Dictionary<string, string> { ["Foo"] = "$(A_PIPE)" } }));
    }

    [Fact]
    public void ReferenceToBufferNotUsedBySocket()
    {
        Validate(s_validCodespec with { Sockets = [new() { Port = 80, InputBuffer = "a" }], Args = ["$(B_PIPE)"], Command = ["$(B_PIPE)"], Env = new Dictionary<string, string> { ["Foo"] = "$(B_PIPE)" } });
    }

    [Fact]
    public void EscapedReferenceToBuffer()
    {
        Validate(s_validCodespec with { Sockets = [new() { Port = 80, InputBuffer = "a" }], Args = ["$$(A_PIPE)"] });
    }

    private static void Validate(object o) => Validator.ValidateObject(o, new ValidationContext(o), true);
}
