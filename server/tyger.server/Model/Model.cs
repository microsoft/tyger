using System.ComponentModel.DataAnnotations;
using System.Globalization;

namespace Tyger.Server.Model;

public record Buffer(string Id);

public record BufferAccess(Uri Uri);

public record Metadata(string? Authority = null, string? Audience = null);

public record BufferParameters(string[]? Inputs, string[]? Outputs);

public record Codespec : IValidatableObject
{
    public BufferParameters? Buffers { get; init; }
    [Required]
    public string Image { get; init; } = "";
    public string[]? Command { get; init; }
    public string[]? Args { get; init; }
    public string? WorkingDir { get; init; }
    public Dictionary<string, string>? Env { get; init; }

    public IEnumerable<ValidationResult> Validate(ValidationContext validationContext)
    {
        if (Buffers != null)
        {
            var combined = (Buffers.Inputs ?? Enumerable.Empty<string>()).Concat(Buffers.Outputs ?? Enumerable.Empty<string>());
            foreach (var group in combined.ToLookup(i => i, StringComparer.InvariantCultureIgnoreCase))
            {
                if (string.IsNullOrWhiteSpace(group.Key))
                {
                    yield return new ValidationResult("A buffer name cannot be empty");
                    continue;
                }
                if (group.Count() > 1)
                {
                    yield return new ValidationResult(string.Format(CultureInfo.InvariantCulture, "All buffer names must be unique across inputs and outputs. Buffer names are case-insensitive. '{0}' is duplicated", group.Key));
                }
                if (group.Key.Contains('/'))
                {
                    yield return new ValidationResult(string.Format(CultureInfo.InvariantCulture, "The buffer '{0}' cannot contain '/' in its name.", group.Key));
                }
            }
        }
    }
}

public record Run
{
    public string? Id { get; init; }
    public Dictionary<string, string>? Buffers { get; init; }
    [Required]
    public string Codespec { get; init; } = "";
    public string? Status { get; init; }
}


public record ErrorBody
{
    public ErrorBody(string code, String message) => Error = new ErrorInfo(code, message);

    public ErrorInfo Error { get; init; }
    public record ErrorInfo(string Code, String Message);
}
