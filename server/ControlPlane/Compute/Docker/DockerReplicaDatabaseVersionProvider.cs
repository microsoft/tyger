// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Net.Sockets;
using System.Runtime.CompilerServices;
using System.Text.Json;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Database.Migrations;
using Tyger.ControlPlane.Model;
using Socket = System.Net.Sockets.Socket;

namespace Tyger.ControlPlane.Compute.Docker;

public class DockerReplicaDatabaseVersionProvider : IReplicaDatabaseVersionProvider
{
    private const string EndpointAddress = "database-version-in-use";
    private const string UnixDomainSocketPrefix = "http://unix:";

    private readonly IConfiguration _configuration;
    private readonly JsonSerializerOptions _jsonSerializerOptions;
    private readonly ILogger<DockerReplicaDatabaseVersionProvider> _logger;

    public DockerReplicaDatabaseVersionProvider(IConfiguration configuration, JsonSerializerOptions jsonSerializerOptions, ILogger<DockerReplicaDatabaseVersionProvider> logger)
    {
        _configuration = configuration;
        _jsonSerializerOptions = jsonSerializerOptions;
        _logger = logger;
    }

    public async IAsyncEnumerable<(Uri, DatabaseVersion)> GetDatabaseVersionsOfReplicas([EnumeratorCancellation] CancellationToken cancellationToken)
    {
        var urls = _configuration.GetValue<string>("Urls");
        if (urls == null)
        {
            yield break;
        }

        var replicaUrls = urls.Split(";", StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
        foreach (var replicaUrl in replicaUrls)
        {
            HttpResponseMessage response;
            try
            {
                if (replicaUrl.StartsWith(UnixDomainSocketPrefix, StringComparison.OrdinalIgnoreCase))
                {
                    var socketPath = replicaUrl["http://unix:".Length..];
                    var httpClient = new HttpClient(new SocketsHttpHandler()
                    {
                        ConnectCallback = async (context, token) =>
                        {
                            var socket = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
                            await socket.ConnectAsync(new UnixDomainSocketEndPoint(socketPath), token);
                            return new NetworkStream(socket);
                        }
                    })
                    {
                        BaseAddress = new Uri("http://ignored")
                    };

                    var uri = new Uri(replicaUrl);
                    response = await httpClient.GetAsync(EndpointAddress, cancellationToken);
                }
                else
                {
                    var client = new HttpClient { BaseAddress = new Uri(replicaUrl) };
                    response = await client.GetAsync(EndpointAddress, cancellationToken);
                }
            }
            catch (Exception ex)
            {
                _logger.ErrorReadingReplicaDatabaseVersion(ex);
                continue;
            }

            if (response.IsSuccessStatusCode)
            {
                var databaseVersion = (await response.Content.ReadFromJsonAsync<DatabaseVersionInUse>(_jsonSerializerOptions, cancellationToken))!;
                yield return (new Uri(replicaUrl), (DatabaseVersion)databaseVersion.Id);
            }
            else
            {
                _logger.ErrorResponseReadingReplicaDatabaseVersion((int)response.StatusCode);
            }
        }
    }
}
