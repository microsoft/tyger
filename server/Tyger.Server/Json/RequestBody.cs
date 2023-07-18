using System.ComponentModel.DataAnnotations;
using System.Text.Json;

namespace Tyger.Server.Json;

public static class RequestBody
{
    public static async ValueTask<TValue> ReadAndValidateJson<TValue>(this HttpRequest request, CancellationToken cancellationToken, bool allowEmpty = false) where TValue : class
    {
        TValue? value;
        try
        {
            value = await request.ReadFromJsonAsync<TValue>(cancellationToken);
            if (value == null)
            {
                throw new ValidationException("null is not a valid input value");
            }

            if (!allowEmpty)
            {
                value = Normalizer.NormalizeEmptyToNull(value);
                if (value == null)
                {
                    throw new ValidationException("An empty object is not a valid input value");
                }
            }

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
