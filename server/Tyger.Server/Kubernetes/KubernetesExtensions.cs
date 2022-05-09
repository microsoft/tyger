using System.Reflection;
using System.Runtime.CompilerServices;
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
}
