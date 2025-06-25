// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Text;
using System.Text.Json;
using Azure.Core;
using k8s;
using k8s.Models;
using Microsoft.Extensions.Options;
using Microsoft.IdentityModel.JsonWebTokens;

namespace Tyger.ControlPlane.Compute.Kubernetes;

public class ContainerRegistryProxySecretUpdater : BackgroundService
{
    public const string ContainerRegistryProxySecretName = "container-registry-proxy";

    private readonly IKubernetes _kubernetes;
    private readonly KubernetesApiOptions _kubernetesOptions;
    private readonly TokenCredential _tokenCredential;
    private readonly ILogger<ContainerRegistryProxySecretUpdater> _logger;

    public ContainerRegistryProxySecretUpdater(IKubernetes kubernetes, IOptions<KubernetesApiOptions> kubernetesOptions, TokenCredential tokenCredential, ILogger<ContainerRegistryProxySecretUpdater> logger)
    {
        _kubernetes = kubernetes;
        _kubernetesOptions = kubernetesOptions.Value;
        _tokenCredential = tokenCredential;
        _logger = logger;
    }

    public override async Task StartAsync(CancellationToken cancellationToken)
    {
        if (string.IsNullOrEmpty(_kubernetesOptions.ContainerRegistryProxy))
        {
            return;
        }

        await RefreshSecret(cancellationToken);
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        if (string.IsNullOrEmpty(_kubernetesOptions.ContainerRegistryProxy))
        {
            return;
        }

        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                await Task.Delay(TimeSpan.FromMinutes(15), stoppingToken);
                await RefreshSecret(stoppingToken);
            }
            catch (TaskCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception e)
            {
                _logger.ErrorInContainerRegistryProxySecretUpdater(e);
            }
        }
    }

    protected async Task RefreshSecret(CancellationToken cancellationToken)
    {
        var token = _tokenCredential.GetToken(new TokenRequestContext(["https://management.azure.com/.default"]), cancellationToken);
        var tokenHandler = new JsonWebTokenHandler();
        var jsonToken = tokenHandler.ReadJsonWebToken(token.Token);
        var oid = jsonToken.GetPayloadValue<string>("oid");

        var dockerConfigJsonMap = new Dictionary<string, object>
        {
            ["auths"] = new Dictionary<string, object>
            {
                [_kubernetesOptions.ContainerRegistryProxy!] = new Dictionary<string, string>
                {
                    ["username"] = oid,
                    ["password"] = token.Token,
                    ["auth"] = Convert.ToBase64String(Encoding.UTF8.GetBytes($"{oid}:{token.Token}"))
                }
            },
        };

        var secret = new V1Secret
        {
            Metadata = new()
            {
                Name = ContainerRegistryProxySecretName
            },
            Data = new Dictionary<string, byte[]>
            {
                [".dockerconfigjson"] = Encoding.UTF8.GetBytes(JsonSerializer.Serialize(dockerConfigJsonMap)),
            },
            Type = "kubernetes.io/dockerconfigjson"
        };

        try
        {
            var existingSecret = await _kubernetes.CoreV1.ReadNamespacedSecretAsync(
                name: secret.Metadata.Name,
                namespaceParameter: _kubernetesOptions.Namespace,
                cancellationToken: cancellationToken);

            await _kubernetes.CoreV1.ReplaceNamespacedSecretAsync(
                body: secret,
                name: secret.Metadata.Name,
                namespaceParameter: _kubernetesOptions.Namespace,
                cancellationToken: cancellationToken);

        }
        catch (k8s.Autorest.HttpOperationException e) when (e.Response.StatusCode == System.Net.HttpStatusCode.NotFound)
        {
            await _kubernetes.CoreV1.CreateNamespacedSecretAsync(
                body: secret,
                namespaceParameter: _kubernetesOptions.Namespace,
                cancellationToken: cancellationToken);
        }

        _logger.UpdatedContainerRegistryProxySecretUpdaterSecret();
    }
}
