# Log in to a Tyger server

Before running core commands like `tyger run`, you first need to call `tyger
login`. `tyger login` signs in using Microsoft Entra ID and then caches server
endpoint and credential information in the filesystem.

By default, this cache file path is `$XDG_CACHE_HOME/tyger/.tyger`. If
`$XDG_CACHE_HOME` is empty, its fallback is `$HOME/.cache` on Linux,
`$HOME/Library/Caches` on macOS, and `$LocalAppData` on Windows. To override the
cache path, set the `$TYGER_CACHE_FILE` environment variable.

## Log in as a user

To login as a user, you will run

```bash
tyger login SERVER_URL [--use-device-code] [--proxy PROXY]
```

This will launch a browser window to perform an interactive login. If that is
not possible, you can specify `--use-device-code` to get a device code and
manually launch a browser window.

`--proxy` allows specifying an HTTP proxy to use for all HTTP calls, including
during login. The value can be `auto[matic]`, `none`, or a URI. The default is
`auto`, which means that it will attempt to automatically detect proxy settings.

## Log in as a service principal

To log in as a service principal instead of a user, you will need to provide a
service principal application ID or URI and a certificate. This can be a path to
a `.pem` file or, on Windows, the thumbprint of a certificate in the current
user's or system's certificate store.

```bash
tyger login
    SERVER_URL
    [--service-principal APPID --certificate CERTPATH | --cert-thumbprint THUMBPRINT]
    [--proxy PROXY]
```

For service principals, this information can instead be provided in a file:

```bash
tyger login -f LOGIN_FILE.yaml
```

`LOGIN_FILE.yaml` looks like this:

```yaml
# The Tyger server URI. Required
serverUri: https://example.com

# The service principal ID or URI. Required.
servicePrincipal: api://my-client

# The path to a file with the service principal certificate
certificatePath: /a/path/to/a/file.pem

# The thumbprint of a certificate in a Windows certificate store to use for service principal authentication (Windows only)
certificateThumbprint: 92829BFAEB67C738DECE0B255C221CF9E1A46285

# The HTTP proxy to use. Can be 'auto[matic]', 'none', or a URI. The default is 'auto'.
proxy: auto
```
