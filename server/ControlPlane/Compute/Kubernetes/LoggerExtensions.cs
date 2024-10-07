// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Compute.Kubernetes;

public static partial class LoggerExtensions
{
    [LoggerMessage(1, LogLevel.Information, "Created secret {secretName}")]
    public static partial void CreatedSecret(this ILogger logger, string secretName);

    [LoggerMessage(5, LogLevel.Error, "The job {job} for run was not found in the cluster")]
    public static partial void RunMissingJob(this ILogger logger, string job);

    [LoggerMessage(14, LogLevel.Information, "Executed Kubernetes API request {method} {uri}. Status code {statusCode}. {errorBody}")]
    public static partial void ExecutedKubernetesRequest(this ILogger logger, HttpMethod method, string? uri, int statusCode, string? errorBody);

    [LoggerMessage(15, LogLevel.Error, "Error listening for new runs.")]
    public static partial void ErrorListeningForNewRuns(this ILogger logger, Exception exception);

    [LoggerMessage(16, LogLevel.Error, "Error creating run {runId} resources.")]
    public static partial void ErrorCreatingRunResources(this ILogger logger, long runId, Exception exception);
}
