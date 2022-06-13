using System.Reflection;
using System.Runtime.CompilerServices;
using System.Runtime.ExceptionServices;
using k8s;
using k8s.Models;
using k8s.Util.Common.Generic;
using k8s.Util.Common.Generic.Options;

namespace Tyger.Server.Kubernetes;

public static class KubernetesExtensions
{
    public static IAsyncEnumerable<V1Pod> EnumeratePodsInNamespace(this IKubernetes client, string @namespace, string? fieldSelector = default, string? labelSelector = default, CancellationToken cancellationToken = default)
    {
        var genericClient = new GenericKubernetesApi(apiGroup: V1PodList.KubeGroup, apiVersion: V1PodList.KubeApiVersion, resourcePlural: V1PodList.KubePluralName, client);
        return EnumerateObjectsInNamespace<V1Pod, V1PodList>(genericClient, @namespace, fieldSelector, labelSelector, cancellationToken);
    }

    public static IAsyncEnumerable<V1Job> EnumerateJobsInNamespace(this IKubernetes client, string @namespace, string? fieldSelector = default, string? labelSelector = default, CancellationToken cancellationToken = default)
    {
        var genericClient = new GenericKubernetesApi(apiGroup: V1JobList.KubeGroup, apiVersion: V1JobList.KubeApiVersion, resourcePlural: V1JobList.KubePluralName, client);
        return EnumerateObjectsInNamespace<V1Job, V1JobList>(genericClient, @namespace, fieldSelector, labelSelector, cancellationToken);
    }

    public static IAsyncEnumerable<TElement> EnumerateObjectsInNamespace<TElement, TList>(this IKubernetes client, string @namespace, string? fieldSelector = default, string? labelSelector = default, CancellationToken cancellationToken = default)
        where TElement : IKubernetesObject
        where TList : class, IItems<TElement>, IKubernetesObject<V1ListMeta>
    {
        var entityMetadata = typeof(TList).GetCustomAttribute<KubernetesEntityAttribute>()!;
        var genericClient = new GenericKubernetesApi(apiGroup: entityMetadata.Group, apiVersion: entityMetadata.ApiVersion, resourcePlural: entityMetadata.PluralName, client);

        return EnumerateObjectsInNamespace<TElement, TList>(genericClient, @namespace, fieldSelector, labelSelector, cancellationToken);
    }

    private static async IAsyncEnumerable<TElement> EnumerateObjectsInNamespace<TElement, TList>(GenericKubernetesApi genericClient, string @namespace, string? fieldSelector = default, string? labelSelector = default, [EnumeratorCancellation] CancellationToken cancellationToken = default)
        where TElement : IKubernetesObject
        where TList : class, IItems<TElement>, IKubernetesObject<V1ListMeta>
    {
        string? continuationToken = null;

        do
        {
            var options = new ListOptions(fieldSelector: fieldSelector, labelSelector: labelSelector, @continue: continuationToken);
            var resp = await genericClient.ListAsync<TList>(@namespace, options, cancellationToken: cancellationToken);

            foreach (var element in resp.Items)
            {
                yield return element;
            }

            continuationToken = resp.Metadata.ContinueProperty;
        }
        while (continuationToken != null);
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
                cancellationToken: cancellationToken);

            await using var enumerator = request.WatchAsync<TElement, object>(onError: e => ExceptionDispatchInfo.Throw(e)).WithCancellation(cancellationToken).GetAsyncEnumerator();
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
                yield return enumerator.Current;
            }
        }
    }
}
