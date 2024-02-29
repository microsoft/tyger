// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Microsoft.AspNetCore.Http;

namespace Tyger.Api;

public static class Responses
{
    public static IResult NotFound() => Results.NotFound(new ErrorBody("NotFound", "The resource was not found"));
    public static IResult BadRequest(string code, string message) => Results.BadRequest(new ErrorBody(code, message));
}

public record ErrorBody
{
    public ErrorBody(string code, string message) => Error = new ErrorInfo(code, message);

    public ErrorInfo Error { get; init; }
    public record ErrorInfo(string Code, string Message);
}
