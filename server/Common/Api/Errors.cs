// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.Common.Api;

public static class Responses
{
    public static IResult NotFound() => Results.NotFound(new ErrorBody("NotFound", "The resource was not found"));
    public static IResult BadRequest(string message) => Results.BadRequest(new ErrorBody("BadRequest", message));
    public static IResult InvalidRoute(string message) => Results.BadRequest(new ErrorBody("InvalidRoute", message));
    public static IResult PreconditionFailed(string message) => Results.Json(new ErrorBody("PreconditionFailed", message), statusCode: StatusCodes.Status412PreconditionFailed);
}

public record ErrorBody
{
    public ErrorBody(string code, string message) => Error = new ErrorInfo(code, message);

    public ErrorInfo Error { get; init; }
    public record ErrorInfo(string Code, string Message);
}
