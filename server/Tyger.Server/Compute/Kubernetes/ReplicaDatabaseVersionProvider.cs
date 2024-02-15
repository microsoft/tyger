// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Runtime.CompilerServices;
using System.Text.Json;
using k8s;
using Microsoft.Extensions.Options;
using Tyger.Server.Database;
using Tyger.Server.Database.Migrations;
using Tyger.Server.Model;

namespace Tyger.Server.Compute.Kubernetes;

public class ReplicaDatabaseVersionProvider : IReplicaDatabaseVersionProvider
{
    private readonly IKubernetes _kubernetesClient;
    private readonly KubernetesCoreOptions _kubernetesOptions;

    private readonly IHttpClientFactory _httpClientFactory;
    private readonly JsonSerializerOptions _jsonSerializerOptions;

    public ReplicaDatabaseVersionProvider(IKubernetes kubernetesClient, IOptions<KubernetesCoreOptions> kubernetesOptions, IHttpClientFactory httpClientFactory, JsonSerializerOptions jsonSerializerOptions)
    {
        _kubernetesClient = kubernetesClient;
        _kubernetesOptions = kubernetesOptions.Value;
        _httpClientFactory = httpClientFactory;
        _jsonSerializerOptions = jsonSerializerOptions;
    }

    public async IAsyncEnumerable<(Uri, DatabaseVersion)> GetDatabaseVersionsOfReplicas([EnumeratorCancellation] CancellationToken cancellationToken)
    {
        var httpClient = _httpClientFactory.CreateClient();
        var endpointSlices = await _kubernetesClient.DiscoveryV1.ListNamespacedEndpointSliceAsync(_kubernetesOptions.Namespace, labelSelector: "kubernetes.io/service-name=tyger-server", cancellationToken: cancellationToken);
        foreach (var slice in endpointSlices.Items)
        {
            var port = slice.Ports.Single(p => p.Protocol == "TCP");
            foreach (var ep in slice.Endpoints)
            {
                if (ep.Conditions.Ready != true)
                {
                    continue;
                }

                foreach (var address in ep.Addresses)
                {
                    var uri = new Uri($"http://{address}:{port.Port}/v1/database-version-in-use");

                    var message = new HttpRequestMessage(HttpMethod.Get, uri)
                    {
                        Headers =
                        {
                            // Adding custom bearer token to secure this endpoint. The token is the pod UID.
                            // See comment on enpoint.
                            Authorization = new ("Bearer", ep.TargetRef.Uid)
                        },
                    };

                    var resp = await httpClient.SendAsync(message, cancellationToken);
                    resp.EnsureSuccessStatusCode();
                    var versionInUse = (await resp.Content.ReadFromJsonAsync<DatabaseVersionInUse>(_jsonSerializerOptions, cancellationToken))!;
                    yield return (uri, (DatabaseVersion)versionInUse.Id);
                }
            }
        }
    }
}
