# Logging in to Tyger

Before running core commands like `tyger run`, you first need to call `tyger
login`. `tyger login` signs in using Microsoft Entra ID and then caches server
endpoint and credential information on the filesystem.

By default, the cache file path is `$XDG_CACHE_HOME/tyger/.tyger`. If
`$XDG_CACHE_HOME` is not set, the fallback paths are `$HOME/.cache` on Linux,
`$HOME/Library/Caches` on macOS, and `$LocalAppData` on Windows. To use a
different cache path, set the `$TYGER_CACHE_FILE` environment variable.

## Log in as a user

To log in as a user, run:

```bash
tyger login SERVER_URL [--use-device-code] [--proxy PROXY]
```

This launches a browser tab for interactive login. If this isn't
possible, use `--use-device-code` to receive a device code and and manually open a
provided URL for authentication.

The `--proxy` option allows specifying an HTTP proxy for all HTTP requests,
including during the login process. The value can be `auto[matic]`, `none`, or a
specific URL. The default setting is `auto`, which attempts to detect proxy
settings automatically.

## Log in as a service principal

To log in as a service principal, you must provide the application ID or URI of
the service principal and a certificate. This could be a path to a `.pem` file
or, on Windows, the thumbprint of a certificate stored in the current user's or
system's certificate store.

```bash
tyger login
    SERVER_URL
    --service-principal APPID
    --certificate CERTPATH | --cert-thumbprint THUMBPRINT
    [--proxy PROXY]
```

For service principals, you can instead provide this information in a file:

```bash
tyger login -f LOGIN_FILE.yaml
```

`LOGIN_FILE.yaml` should look like this:

```yaml
# The Tyger server URI. Required.
serverUri: https://example.com

# The service principal ID or URI. Required.
servicePrincipal: api://my-client

# The path to the service principal certificate file
certificatePath: /a/path/to/a/file.pem

# The thumbprint of a certificate in a Windows certificate store for service principal authentication (Windows only)
certificateThumbprint: 92829BFAEB67C738DECE0B255C221CF9E1A46285

# The HTTP proxy setting. Options are 'auto[matic]', 'none', or a URI. Default is 'auto'.
proxy: auto
```
