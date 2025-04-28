// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Compute.Kubernetes;

public static partial class LoggerExtensions
{
    [LoggerMessage(LogLevel.Information, "Kubernetes object {path} already exists.")]
    public static partial void KubernetesObjectAlreadyExists(this ILogger logger, string path);

    [LoggerMessage(LogLevel.Error, "The job {job} for run was not found in the cluster")]
    public static partial void RunMissingJob(this ILogger logger, string job);

    [LoggerMessage(LogLevel.Information, "Executed Kubernetes API request {method} {url}. {durationMs} ms. Status code {statusCode}. {errorBody}")]
    public static partial void ExecutedKubernetesRequest(this ILogger logger, HttpMethod method, string? url, double durationMs, int statusCode, string? errorBody);

    [LoggerMessage(LogLevel.Error, "Error listening for new runs.")]
    public static partial void ErrorListeningForNewRuns(this ILogger logger, Exception exception);

    [LoggerMessage(LogLevel.Error, "Error creating run {runId} resources.")]
    public static partial void ErrorCreatingRunResources(this ILogger logger, long runId, Exception exception);

    [LoggerMessage(LogLevel.Warning, "Retryable error creating run {runId} resources.")]
    public static partial void RetryableErrorCreatingRunResources(this ILogger logger, long runId, Exception exception);

    [LoggerMessage(LogLevel.Error, "Error while watching resources.")]
    public static partial void ErrorWatchingResources(this ILogger logger, Exception exception);

    [LoggerMessage(LogLevel.Error, "Error listening for changes to run records.")]
    public static partial void ErrorListeningForRunCanges(this ILogger logger, Exception exception);

    [LoggerMessage(LogLevel.Information, "Finalizing run {runId}.")]
    public static partial void FinalizingRun(this ILogger logger, long runId);

    [LoggerMessage(LogLevel.Information, "Finalized run {runId}.")]
    public static partial void FinalizedRun(this ILogger logger, long runId);

    [LoggerMessage(LogLevel.Error, "Error finalizing run {runId}.")]
    public static partial void ErrorFinalizingRun(this ILogger logger, long runId, Exception exception);

}
