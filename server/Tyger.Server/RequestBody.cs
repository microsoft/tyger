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
        catch (JsonException e)
        {
            var message = e.Message;
            if (e.Path != null && !message.Contains("Path:"))
            {
                message += $"{(message[^1] == '.' ? null : '.')} Path: {e.Path}";
            }

            throw new ValidationException("Error deserializing input: " + message);
        }
    }
}
