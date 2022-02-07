using Tyger.Server.Model;

namespace Tyger.Server;

public static class Responses
{
    public static IResult NotFound() => Results.NotFound(new ErrorBody("NotFound", "The resource was not found"));
    public static IResult BadRequest(string code, string message) => Results.BadRequest(new ErrorBody(code, message));
}
