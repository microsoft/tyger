using System.ComponentModel.DataAnnotations;
using System.Text.Json;

namespace Tyger.Server;

public static class RequestBody
{
    public static async ValueTask<TValue> ReadAndValidateJson<TValue>(this HttpRequest request, CancellationToken cancellationToken)
    where TValue : new()
    {
        TValue value;
        try
        {
            value = await request.ReadFromJsonAsync<TValue>(cancellationToken) ?? new();
            Validator.ValidateObject(value, new ValidationContext(value), validateAllProperties: true);
            return value;
        }
        catch (JsonException)
        {
            throw new ValidationException("The input is syntactically invalid");
        }
    }
}
