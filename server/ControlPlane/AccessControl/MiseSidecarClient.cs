// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Net;
using Microsoft.AspNetCore.Authentication.JwtBearer;
using Microsoft.Net.Http.Headers;

namespace Tyger.ControlPlane.AccessControl;

/// <summary>
/// Calls into a sidecar to perform additional authorization checks.
/// Only used on Microsoft internal deployments.
/// </summary>
internal sealed class MiseSidecarClient : IDisposable
{
    private const string SidecarEndpointAddress = "http://localhost:4000/ValidateRequest";
    private readonly HttpClient _httpClient;
    private readonly ILogger<MiseSidecarClient> _logger;

    public MiseSidecarClient(ILogger<MiseSidecarClient> logger)
    {
        _httpClient = new HttpClient();
        _logger = logger;
    }

    public async Task ValidateWithMiseSidecar(TokenValidatedContext context)
    {
        var originalRequest = context.HttpContext.Request;
        string originalUri = $"{originalRequest.Scheme}://{originalRequest.Host}{originalRequest.Path}{originalRequest.QueryString}";
        string originalMethod = originalRequest.Method;
        string originalIp = context.HttpContext.Connection.RemoteIpAddress?.ToString() ?? string.Empty;
        string originalAuthHeader = originalRequest.Headers[HeaderNames.Authorization].ToString() ?? string.Empty;

        var request = new HttpRequestMessage(HttpMethod.Post, SidecarEndpointAddress);

        request.Headers.TryAddWithoutValidation("Original-Uri", originalUri);
        request.Headers.TryAddWithoutValidation("Original-Method", originalMethod);
        request.Headers.TryAddWithoutValidation("X-Forwarded-For", originalIp);
        request.Headers.TryAddWithoutValidation("Authorization", originalAuthHeader);

        using var response = await _httpClient.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, context.HttpContext.RequestAborted);

        if (response.StatusCode == HttpStatusCode.OK)
        {
            return;
        }

        string errorDescription = string.Empty;
        if (response.Headers.TryGetValues("Error-Description", out var errorDescVals))
        {
            errorDescription = string.Join("; ", errorDescVals);
        }

        if (response.StatusCode == HttpStatusCode.Unauthorized)
        {
            _logger.MiseAuthentationDenied(errorDescription);
            context.Fail(string.IsNullOrEmpty(errorDescription) ? "Token validation failed." : errorDescription);
            return;
        }

        throw new InvalidOperationException($"MISE sidecar returned unexpected status code: {(int)response.StatusCode} with error description: {errorDescription}");
    }

    public void Dispose()
    {
        _httpClient.Dispose();
    }
}
