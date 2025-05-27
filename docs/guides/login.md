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

## Log in using a managed identity

If you are running on an Azure VM, you can login to tyger using a managed identity with:

```bash
tyger login SERVER_URL --identity [--identity-client-id MI_ID] [--federated-identity TARGET_CLIENT_ID]
```

If you have user-assigned identities on the VM, you can specify which identity to
use with the `--identity-client-id` parameter.

To use the managed identity to get a token as another identity using [federated
credentials](https://learn.microsoft.com/en-us/entra/workload-id/workload-identity-federation-config-app-trust-managed-identity?tabs=microsoft-entra-admin-center),
specify the client ID of the target identity with `--federated-identity`.

## Log in from GitHub Actions

Similar to Azure managed identities, you can use [federated
credentials](https://learn.microsoft.com/en-us/entra/workload-id/workload-identity-federation-create-trust?pivots=identity-wif-apps-methods-azp#github-actions∑)
to log in from a GitHub Actions runner:

```bash
tyger login SERVER_URL --github --federated-identity TARGET_CLIENT_ID
```

You will need to follow GitHub
[documentation](https://docs.github.com/en/actions/security-for-github-actions/security-hardening-your-deployments/configuring-openid-connect-in-azure)
in order to ensure you can use this feature from your pipeline.

## Specifying login options from a configuration file

Instead of command-line flags, you can specify login parameters in a
configuration file:

```bash
tyger login -f LOGIN_FILE.yml
```

`LOGIN_FILE.yml` should look like this:

```yaml
# The Tyger server URL. Required.
serverUrl: https://example.com

# The service principal ID.
servicePrincipal: api://my-client

# The path to a file with the service principal certificate.
# Can only be specified if servicePrincipal is set.
certificatePath: /a/path/to/a/file.pem

# The thumbprint of a certificate in a Windows certificate store to use for service principal authentication (Windows only)
# Can only be specified if servicePrincipal is set.
certificateThumbprint: 92829BFAEB67C738DECE0B255C221CF9E1A46285

# Whether to use Azure managed identity for authentication.
managedIdentity: false
managedIdentityClientId: # Optionally specify the client ID of the managed identity to use.

# Whether to use GitHub Actions tokens with federated identity for authentication.
github: false

# If using managed identity or GitHub Actions, specify the client ID of the federated identity to authenticate as.
targetFederatedIdentity: # Optionally specify a federated identity to authenticate as using the managed identity.

# The HTTP proxy to use. Can be 'auto[matic]', 'none', or a URL. The default is 'auto'.
proxy: auto
```
