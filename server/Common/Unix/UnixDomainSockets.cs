// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Net.Sockets;

namespace Tyger.Common.Unix;

public static class UnixDomainSockets
{
    public static void ConfigureUnixDomainSockets(this WebApplicationBuilder app)
    {
        // When a process is killed, it will not clean up its sockets, leaving the entry
        // in the filesystem. Here we delete the file if it exists and after trying to connect to it.
        app.WebHost.UseSockets(o =>
        {
            var defaultCreator = o.CreateBoundListenSocket;
            o.CreateBoundListenSocket = (endpoint) =>
            {
                if (endpoint is not UnixDomainSocketEndPoint)
                {
                    return defaultCreator(endpoint);
                }

                var path = endpoint.ToString();
                if (File.Exists(path))
                {
                    using var socket = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
                    bool connected = false;
                    try
                    {
                        socket.Connect(endpoint);
                        connected = true;
                    }
                    catch (SocketException)
                    {
                        File.Delete(path);
                    }

                    if (connected)
                    {
                        throw new InvalidOperationException($"Socket '{path}' appears to be in use by another process");
                    }
                }

                var createdSocket = defaultCreator(endpoint);

                if (app.Configuration.GetValue<string>("SocketPermissions") is { Length: > 0 } permString)
                {
                    int permissionsInt;
                    try
                    {
                        permissionsInt = Convert.ToInt32(permString, 8);
                    }
                    catch (Exception e)
                    {
                        throw new InvalidOperationException($"Invalid SocketPermissions value '{permString}'", e);
                    }

                    Interop.ChMod(path!, permissionsInt);
                }

                return createdSocket;
            };
        });
    }
}
