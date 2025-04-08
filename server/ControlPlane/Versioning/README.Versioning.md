# Tyger Server API Versioning

## Server

The Tyger server uses the [Asp.Versioning.Http library for ASP.NET Core Minimal APIs](https://github.com/dotnet/aspnet-api-versioning/wiki), which requires us to declare a set of server-supported API versions, then specify which versions apply to each endpoint.

The API versions offered by the Tyger server are declared in `Versioning.cs`.
The Tyger server's top-level route (`/`) is configured as a versioned API, so, by default, all endpoints in this route will support all offered API versions.
Each endpoint can then be mapped to specific API versions as needed. See below for examples.

## Client

Clients must request a specific API version via the query parameter `api-version` for all endpoints except those marked as version neutral (e.g. `/metadata`).

The Tyger client will automatically use the most recent API version it knows, but this can be overridden by the (*undocumented*) environment variable `TYGER_CLIENT_API_VERSION`, e.g.

```bash
$ TYGER_CLIENT_API_VERSION=0.9 tyger buffer list --limit 5 --log-level trace
2025-04-08T14:04:51.081Z TRC Outgoing request request="GET /buffers?api-version=0.9&limit=5 HTTP/1.1\r\nHost: jnaegele-tyger.westus2.cloudapp.azure.com\r\nUser-Agent: Go-http-client/1.1\r\nAuthorization: Bearer --REDACTED--\r\nContent-Type: application/json\r\nAccept-Encoding: gzip\r\n\r\n"
2025-04-08T14:04:51.081Z TRC Sending request method=GET proxy= url=https://jnaegele-tyger.westus2.cloudapp.azure.com/buffers?api-version=REDACTED&limit=REDACTED
2025-04-08T14:04:51.311Z TRC Received response method=GET proxy= status=200 url=https://jnaegele-tyger.westus2.cloudapp.azure.com/buffers?api-version=REDACTED&limit=REDACTED
2025-04-08T14:04:51.311Z TRC Incoming response response="HTTP/1.1 200 OK
...
```

## Backward Compatibility

When this API versioning scheme was introduced, all Tyger server URLs changed to drop the `/v1/` prefix.

To maintain backward compatibility with previously deployed Tyger clients, the Tyger server rewrites all request paths to remove the `/v1/` prefix and adds the `api-version=1.0` query parameter.

Once we officially deprecate Tyger versions older than v0.11.0, we can remove this compatibility in the server.


## How-To

### Add a new API version

To add a new API version to Tyger, first declare the new version in `Versioning.cs`, e.g.

```c#
public static readonly ApiVersion V2p0 = new(2, 0);
```

Then, declare support for the new version in `ConfigureVersionedRouteGroup`, e.g.

```c#
var root = api.MapGroup(prefix)
    .HasApiVersion(V1p0)
    .HasApiVersion(V2p0)
    ;
```

At this point, **all** existing endpoints will support the new API version.
See below for examples of restricting endpoints to specific API versions.


### Add a new endpoint

By default, new endpoints will support all declared API versions.

To configure an endpoint to only support a specific API version, call `MapToApiVersion` on its `RouteHandlerBuilder`.

For example, to add a new `/buffers/{id}/size` endpoint that only exists in API version 2.0:

```c#
buffers.MapGet("/{id}/size", async (...) =>
    {
        // ...
    })
    .WithName("getBufferSize")
    .Produces<int>(StatusCodes.Status200OK)
    .Produces<ErrorBody>(StatusCodes.Status404NotFound);
    .MapToApiVersion(ApiVersions.V2p0) // <- This endpoint is only visible to api-version=2.0
```

To make an endpoint version-neutral, such that it does NOT require the client to specify any API version, call `IsApiVersionNeutral` on its `RouteHandlerBuilder`, e.g.

```c#
root.MapGet("/metadata", (...) =>
    {
        // ...
    }).IsApiVersionNeutral();
```


### Change an endpoint

If we make a breaking change to an endpoint, we may need to continue supporting both "versions" of the endpoint.

The corresponding `RouteHandlerBuilder` for each version of the endpoint must be annotated with the version(s) of the API it supports.

For example, say we want to change API version 1.0 to make the `limit` query parameter on `GET /buffers` *required*.
This is a breaking change, so we need to map the new endpoint to the new API version 2.0:

```c#
// Version 1.0 of the `GET /buffers` endpoint
buffers.MapGet("/", async (BufferManager manager, HttpContext context, int? limit, CancellationToken cancellationToken) =>
    {
        limit = limit is null ? 20 : Math.Min(limit.Value, 2000);
        // ...
    })
    .WithName("getBuffers_v1")
    .Produces<BufferPage>()
    .Produces<ErrorBody>(StatusCodes.Status400BadRequest)
    .MapToApiVersion(ApiVersions.V1p0)

// Version 2.0 of the `GET /buffers` endpoint
buffers.MapGet("/", async (BufferManager manager, HttpContext context, int limit, CancellationToken cancellationToken) =>
    {
        // ...
    })
    .WithName("getBuffers")
    .Produces<BufferPage>()
    .Produces<ErrorBody>(StatusCodes.Status400BadRequest)
    .MapToApiVersion(ApiVersions.V2p0)
```

Note that we also need to differentiate between the identical endpoint paths in the call to `.WithName(...)`.


### Deprecate an endpoint

If we need to formally deprecate an API endpoint/version in the future,
the Asp.Versioning library offers support for sunsetting/deprecating API versions:
- https://github.com/dotnet/aspnet-api-versioning/wiki/Deprecating-a-Service-Version
- https://github.com/dotnet/aspnet-api-versioning/wiki/Version-Policies


### Test API Version changes

When testing API version changes, the API version can be "overridden" on the Tyger client by
1.  Supplying the version in the URI directly:
    ```go
	controlplane.InvokeRequest(context.Background(), http.MethodGet, "/buffers?api-version=2.0", nil, nil, nil)
    ```
2.  (or) Setting the version in the request query parameters:
    ```go
    options := url.Values{}
    options.Add(controlplane.ApiVersionQueryParam, "2.0")
	controlplane.InvokeRequest(context.Background(), http.MethodGet, "/buffers", options, nil, nil)
    ```
3.  (or) Setting the version on the context passed to `controlplane.InvokeRequest`:
    ```go
	ctx := controlplane.SetApiVersionOnContext(context.Background(), "2.0")
	controlplane.InvokeRequest(ctx, http.MethodGet, "/buffers", nil, nil, nil)
    ```

Depending on how the API changes, we may need to maintain older versions of parts of the control plane model, and test different workflows for different API versions.
