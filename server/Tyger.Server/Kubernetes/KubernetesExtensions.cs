using System.Reflection;
using System.Runtime.CompilerServices;
using System.Runtime.ExceptionServices;
using k8s;
using k8s.Models;

namespace Tyger.Server.Kubernetes;

public static class KubernetesExtensions
{
    private const int PageSize = 100;
    public static async IAsyncEnumerable<V1Pod> EnumeratePodsInNamespace(this IKubernetes client, string @namespace, string? fieldSelector = default, string? labelSelector = default, [EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        string? continuationToken = null;
        do
        {
            var podList = await client.CoreV1.ListNamespacedPodAsync(namespaceParameter: @namespace, continueParameter: continuationToken, fieldSelector: fieldSelector, labelSelector: labelSelector, limit: PageSize, cancellationToken: cancellationToken);
            foreach (var pod in podList.Items)
            {
                yield return pod;
            }

            continuationToken = podList.Metadata.ContinueProperty;
        } while (!string.IsNullOrEmpty(continuationToken));
    }

    public static async IAsyncEnumerable<V1Job> EnumerateJobsInNamespace(this IKubernetes client, string @namespace, string? fieldSelector = default, string? labelSelector = default, [EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        string? continuationToken = null;
        do
        {
            var jobList = await client.BatchV1.ListNamespacedJobAsync(namespaceParameter: @namespace, continueParameter: continuationToken, fieldSelector: fieldSelector, labelSelector: labelSelector, limit: PageSize, cancellationToken: cancellationToken);
            foreach (var job in jobList.Items)
            {
                yield return job;
            }

            continuationToken = jobList.Metadata.ContinueProperty;
        } while (!string.IsNullOrEmpty(continuationToken));
    }

    public static IAsyncEnumerable<(WatchEventType, V1Job)> WatchNamespacedJobsWithRetry(
        this IKubernetes client,
        ILogger logger,
        string @namespace,
        string? fieldSelector = default,
        string? labelSelector = default,
        string? resourceVersion = default,
        int retryCount = 20,
        CancellationToken cancellationToken = default)
    {
        return client.WatchNamespacedObjectsWithRetry<V1Job>(logger, V1Job.KubeGroup, V1Job.KubeApiVersion, V1Job.KubePluralName, @namespace, fieldSelector, labelSelector, resourceVersion, retryCount, cancellationToken);
    }

    public static IAsyncEnumerable<(WatchEventType, V1Pod)> WatchNamespacedPodsWithRetry(
        this IKubernetes client,
        ILogger logger,
        string @namespace,
        string? fieldSelector = default,
        string? labelSelector = default,
        string? resourceVersion = default,
        int retryCount = 20,
        CancellationToken cancellationToken = default)
    {
        return client.WatchNamespacedObjectsWithRetry<V1Pod>(logger, V1Pod.KubeGroup, V1Pod.KubeApiVersion, V1Pod.KubePluralName, @namespace, fieldSelector, labelSelector, resourceVersion, retryCount, cancellationToken);
    }

    public static IAsyncEnumerable<(WatchEventType, TElement)> WatchNamespacedObjectsWithRetry<TElement>(
        this IKubernetes client,
        ILogger logger,
        string @namespace,
        string? fieldSelector = default,
        string? labelSelector = default,
        string? resourceVersion = default,
        int retryCount = 20,
        CancellationToken cancellationToken = default)
            where TElement : IKubernetesObject<V1ObjectMeta>
    {
        var entityMetadata = typeof(TElement).GetCustomAttribute<KubernetesEntityAttribute>()!;
        return client.WatchNamespacedObjectsWithRetry<TElement>(logger, entityMetadata.Group, entityMetadata.ApiVersion, entityMetadata.PluralName, @namespace, fieldSelector, labelSelector, resourceVersion, retryCount, cancellationToken);
    }

    public static async IAsyncEnumerable<(WatchEventType, TElement)> WatchNamespacedObjectsWithRetry<TElement>(
            this IKubernetes client,
            ILogger logger,
            string group,
            string version,
            string plural,
            string @namespace,
            string? fieldSelector = default,
            string? labelSelector = default,
            string? resourceVersion = default,
            int retryCount = 20,
            [EnumeratorCancellation] CancellationToken cancellationToken = default)
                where TElement : IKubernetesObject<V1ObjectMeta>
    {
        while (true)
        {
            var request = client.CustomObjects.ListNamespacedCustomObjectWithHttpMessagesAsync(
                group,
                version,
                @namespace,
                plural,
                fieldSelector: fieldSelector,
                labelSelector: labelSelector,
                watch: true,
                resourceVersion: resourceVersion,
                allowWatchBookmarks: true,
                cancellationToken: cancellationToken);

            await using var enumerator = request.WatchAsync<TElement, object>(onError: ExceptionDispatchInfo.Throw, cancellationToken).WithCancellation(cancellationToken).GetAsyncEnumerator();
            while (true)
            {
                try
                {
                    try
                    {
                        if (!await enumerator.MoveNextAsync())
                        {
                            yield break;
                        }
                    }
                    catch (Exception e) when (retryCount > 0 && e is KubernetesException or IOException or HttpRequestException)
                    {
                        logger.RestartingWatchAfterException(e);
                        retryCount--;
                        await Task.Delay(TimeSpan.FromSeconds(2), cancellationToken);
                        break;
                    }
                }
                catch (Exception e) when (e is not OperationCanceledException || !cancellationToken.IsCancellationRequested)
                {
                    logger.UnexpectedExceptionDuringWatch(e);
                    throw;
                }

                resourceVersion = enumerator.Current.Item2.ResourceVersion();
                if (enumerator.Current.Item1 != WatchEventType.Bookmark)
                {
                    yield return enumerator.Current;
                }
            }
        }
    }
}
