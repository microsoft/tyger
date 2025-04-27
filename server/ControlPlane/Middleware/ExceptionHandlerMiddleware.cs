// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using k8s.Autorest;
using Tyger.Common.Api;
using ValidationException = System.ComponentModel.DataAnnotations.ValidationException;

namespace Tyger.ControlPlane.Middleware;

public static class ExceptionHandlerMiddlewareRegistration
{
    public static void ConfigureExceptionHandling(this IHostApplicationBuilder builder)
    {
        // Throw on bad request so we can catch Parameter Binding errors and return a useful error message
        builder.Services.Configure<RouteHandlerOptions>(options => options.ThrowOnBadRequest = true);
    }
    public static void UseExceptionHandling(this WebApplication app) => app.UseMiddleware<ExceptionHandlerMiddleware>();
}

/// <summary>
/// A middleware component provides top-level exception handling.
/// ValidationExceptions result in a 404. Unhandled exceptions
/// are logged and result in a 500.
/// </summary>
public class ExceptionHandlerMiddleware
{
    private readonly RequestDelegate _next;
    private readonly ILogger<ExceptionHandlerMiddleware> _logger;

    public ExceptionHandlerMiddleware(RequestDelegate next, ILogger<ExceptionHandlerMiddleware> logger)
    {
        _next = next;
        _logger = logger;
    }

    public async Task Invoke(HttpContext context)
    {
        try
        {
            await _next(context);
        }
        catch (ValidationException e)
        {
            await Responses.InvalidInput(e.ValidationResult.ErrorMessage!).ExecuteAsync(context);
        }
        catch (BadHttpRequestException e)
        {
            await Responses.BadRequest(e.Message).ExecuteAsync(context);
        }
        catch (OperationCanceledException) when (context.RequestAborted.IsCancellationRequested)
        {
        }
        catch (AggregateException e) when (context.RequestAborted.IsCancellationRequested
                                            && e.InnerExceptions.Any(ie => ie is OperationCanceledException))
        {
        }
        catch (HttpOperationException e)
        {
            if (!context.Response.HasStarted)
            {
                context.Response.StatusCode = StatusCodes.Status500InternalServerError;
            }

            _logger.UnhandledException(e, e.Response?.Content);
        }
        catch (Exception e)
        {
            if (!context.Response.HasStarted)
            {
                context.Response.StatusCode = StatusCodes.Status500InternalServerError;
            }

            _logger.UnhandledException(e);
        }
    }
}

public static partial class LoggerExtensions
{
    [LoggerMessage(LogLevel.Error, "Request failed with an unhandled exception. {innerResponseBody}")]
    public static partial void UnhandledException(this ILogger logger, Exception exception, string? innerResponseBody = null);
}
