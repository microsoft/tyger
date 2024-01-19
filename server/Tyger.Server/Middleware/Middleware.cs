// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Collections.Frozen;
using System.Diagnostics;
using k8s.Autorest;
using Microsoft.AspNetCore.Mvc.ApiExplorer;
using Microsoft.AspNetCore.Mvc.ModelBinding;
using Microsoft.Net.Http.Headers;
using Tyger.Server.Model;
using ValidationException = System.ComponentModel.DataAnnotations.ValidationException;

namespace Tyger.Server.Middleware;

public static class Middleware
{
    public static void UseRequestLogging(this WebApplication app) => app.UseMiddleware<RequestLogging>();
    public static void UseExceptionHandling(this WebApplication app) => app.UseMiddleware<ExceptionHandler>();

    public static void UseRequestId(this WebApplication app)
    {
        app.Use(async (HttpContext context, Func<Task> next) =>
           {
               context.Response.Headers.RequestId = context.TraceIdentifier;
               await next();
           });
    }

    public static void UseBaggage(this WebApplication app)
    {
        app.Use(async (HttpContext context, Func<Task> next) =>
           {
               var activity = Activity.Current;
               if (activity != null)
               {
                   var baggagePairs = context.Request.Headers.GetCommaSeparatedValues(HeaderNames.Baggage);
                   if (baggagePairs != null)
                   {
                       foreach (var pairString in baggagePairs)
                       {
                           if (NameValueHeaderValue.TryParse(pairString, out var pair) && pair.Name.HasValue)
                           {
                               Activity.Current?.AddBaggage(pair.Name.Value, pair.Value.Value);
                           }
                       }
                   }
               }

               await next();
           });
    }
}

/// <summary>
/// A middleware component provides top-level exception handling.
/// ValidationExceptions result in a 404. Unhandled exceptions
/// are logged and result in a 500.
/// </summary>
public class ExceptionHandler
{
    private readonly RequestDelegate _next;
    private readonly ILogger<ExceptionHandler> _logger;

    public ExceptionHandler(RequestDelegate next, ILogger<ExceptionHandler> logger)
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
            if (!context.Response.HasStarted)
            {
                context.Response.StatusCode = StatusCodes.Status400BadRequest;
            }

            await context.Response.WriteAsJsonAsync(new ErrorBody("InvalidInput", e.ValidationResult.ErrorMessage!));
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

/// <summary>
/// A middleware component that logs every HTTP request. We implement this one
/// instead of the built-in one because the latter surprisingly does not include the request duration.
/// </summary>
public class RequestLogging
{
    private const string Redacted = "***";
    private readonly RequestDelegate _next;
    private readonly ILogger<RequestLogging> _logger;
    private readonly FrozenDictionary<string, bool> _systemQueryParameters; // key = parameter name, value = should value be redacted

    public RequestLogging(RequestDelegate next, IApiDescriptionGroupCollectionProvider apiDescriptionsProvider, ILogger<RequestLogging> logger)
    {
        _next = next;
        _logger = logger;

        _systemQueryParameters = apiDescriptionsProvider.ApiDescriptionGroups.Items
            .SelectMany(g => g.Items)
            .SelectMany(d => d.ParameterDescriptions)
            .Where(p => p.Source == BindingSource.Query)
            .GroupBy(p => p.Name, StringComparer.Ordinal)
            .ToFrozenDictionary(g => g.Key, g => g.Any(p => p.Type == typeof(string)), StringComparer.Ordinal);
    }

    public async Task Invoke(HttpContext context)
    {
        if (!_logger.IsEnabled(LogLevel.Information))
        {
            // Logger isn't enabled.
            await _next(context);
            return;
        }

        var start = Stopwatch.GetTimestamp();
        try
        {
            await _next(context);
        }
        finally
        {
            _logger.RequestCompleted(
                SanitizeUserInputForLogging(context.Request.Method),
                SanitizeUserInputForLogging(context.Request.Path.ToString()),
                RedactQueryStringValues(context.Request.Query),
                context.Response.StatusCode, (Stopwatch.GetTimestamp() - start) * 1000.0 / Stopwatch.Frequency);
        }
    }

    /// <summary>
    /// Basic protection against https://owasp.org/www-community/attacks/Log_Injection
    /// </summary>
    private static string SanitizeUserInputForLogging(string input)
    {
        return input.Replace(Environment.NewLine, string.Empty);
    }

    private string? RedactQueryStringValues(IQueryCollection query)
    {
        if (query == null)
        {
            return null;
        }

        if (query.Count == 0)
        {
            return string.Empty;
        }

        return QueryString.Create(
            query.Select(q =>
            {
                if (_systemQueryParameters.TryGetValue(q.Key, out bool redactValue))
                {
                    if (redactValue)
                    {
                        return KeyValuePair.Create(SanitizeUserInputForLogging(q.Key), (string?)Redacted);
                    }

                    return KeyValuePair.Create(SanitizeUserInputForLogging(q.Key), (string?)SanitizeUserInputForLogging(q.Value.ToString()));
                }

                return KeyValuePair.Create(Redacted, (string?)Redacted);
            })).ToString();
    }
}

public static partial class LoggerExtensions
{
    [LoggerMessage(0, LogLevel.Information, "Request {method} {path}{query} completed with status {statusCode} in {milliseconds} ms.")]
    public static partial void RequestCompleted(this ILogger logger, string method, string path, string? query, int statusCode, double milliseconds);

    [LoggerMessage(1, LogLevel.Error, "Request failed with an unhandled exception. {innerResponseBody}")]
    public static partial void UnhandledException(this ILogger logger, Exception exception, string? innerResponseBody = null);
}
