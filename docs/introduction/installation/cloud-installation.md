# Azure cloud installation

To use Tyger with Azure, you need an Azure subscription and the [Azure
CLI](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli) installed.

The steps for installing Tyger are:

1. Install the `tyger` CLI.
2. Create an installation config file.
3. Install cloud resources.
4. Install authentication identities.
5. Install the Tyger API.

After installing the `tyger` CLI, you will use it for the subsequent steps.

For step (3), you will need to have the Owner role at a resource group or the
subscription level.

For step (4), you will need permissions to create applications in your Microsoft
Entra tenant.

Step (5) does not require either of these.

## Install the `tyger` CLI

`tyger` is a single executable file. To install it:

1. Visit the [releases](https://github.com/microsoft/tyger/releases) page.
2. Download the right archive for your platform.
3. Extract the archive to find the `tyger` executable. Copy it to a directory
   included in your PATH environment variable.

You should now be able to run `tyger --version`.

## Generate an installation configuration file

We will now generate an installation configuration file, which will be used for
subsequent installation steps.

First, ensure you're logged into the Azure CLI using `az login`.

Next, generate the file with:

```bash
tyger config create -f config-path.yml
```

This command runs an interactive wizard, resulting in a config file saved at the
specified location. We recommend keeping this file under version control.

Review and adjust the file's contents as needed.

The installation configuration file typically looks like this:

```yaml
kind: azureCloud
environmentName: demo

cloud:
  tenantId: 2dda111b-8b49-4193-b6ad-7ad797aa552f
  subscriptionId: 17f10121-f320-4d47-928f-3d42adb68e01
  resourceGroup: demo
  defaultLocation: westus2

  # Optionally point an existing Log Analytics workspace to send logs to.
  # logAnalyticsWorkspace:
    # resourceGroup:
    # name:

  # Optionaly use a custom DNS zone
  # dnsZone
    # resourceGroup:
    # name:

  # Optionally provide a TLS certificate, if not using Let's Encrypt
  # tlsCertificate:
    # keyVault:
    #   resourceGroup:
    #   name:
    # certificateName:

  compute:
    clusters:
      - name: demo
        apiHost: true
        kubernetesVersion: "1.30"
        sku:
        # location: defaults to Standard
        systemNodePool:
          name: system
          vmSize: Standard_DS2_v2
          minCount: 1
          maxCount: 3
          # osSku: defaults to AzureLinux

        userNodePools:
          - name: cpunp
            vmSize: Standard_DS2_v2
            minCount: 1
            maxCount: 10
            # osSku: defaults to AzureLinux
          - name: gpunp
            vmSize: Standard_NC6s_v3
            minCount: 0
            maxCount: 10
            # osSku: defaults to AzureLinux

    # These are the principals that will have the ability to run `tyger api install`.
    # They will have access to the "tyger" namespace in each cluster and will have
    # the necessary Azure RBAC role assignments.
    # For users:
    #   "kind" must be set to "User"
    #   "objectId" must be set to the object ID GUID
    #   "userPrincipalName" must be set (this is usually the email address, unless this is a guest account)
    # For groups:
    #   "kind" must be set to "Group"
    #   "objectId" must be set to the object ID GUID
    # For service principals:
    #   "kind" must be set to "ServicePrincipal"
    #   "objectId" must be set to the object ID GUID
    managementPrincipals:
      - kind: User
        userPrincipalName: me@example.com
        objectId: 18c9e451-88aa-47d2-ae4f-1d34d55dc50c

    # The names of private container registries that the clusters must
    # be able to pull from.
    # privateContainerRegistries:
    #   - myprivateregistry

    # This must be set if using a custom DNS zone and needs to be globally unique for the Azure region.
    # Each organization's domain name will have a CNAME record pointing to the domain name formed
    # by this value, which will be <dnslabel>.<region>.cloudapp.azure.com
    # dnsLabel:

    # Optional Helm chart overrides
    # helm:
      # traefik:
        # repoName:
        # repoUrl: not set if using `chartRef`
        # chartRef: e.g. oci://...
        # version:
        # values:
      # certManager:
      # nvidiaDevicePlugin:

  database:
    serverName: demo-tyger
    postgresMajorVersion: 16
    # location: Defaults to defaultLocation
    # computeTier: Defaults to Burstable
    # vmSize: Defaults to Standard_B1ms
    # storageSizeGB: Defaults to 32 (the minimum supported)
    # backupRetentionDays: Defaults to 7
    # backupGeoRedundancy: Defaults to false

    # Firewall rules to control where the database can be accessed from,
    # in addition to the control-plane cluster.
    # firewallRules:
    #  - name:
    #    startIpAddress:
    #    endIpAddress:

organizations:
  - name: default
    cloud:
      storage:
        # Storage accounts for buffers.
        buffers:
          - name: demowestus2buf
            # location: defaults to defaultLocation
            # sku: defaults to Standard_LRS

        # The storage account where run logs will be stored.
        logs:
          name: demotygerlogs
          # location: defaults to defaultLocation
          # sku: defaults to Standard_LRS

      # An optional array of managed identities that will be created in the resource group.
      # These identities are available to runs as workload identities. When updating this list
      # both `tyger cloud install` and `tyger api installed` must be run.
      # identities:
      # - my-identity

    api:
      # The fully qualified domain name for the Tyger API.
      domainName: demo-tyger.westus2.cloudapp.azure.com

      # Set to KeyVault if using a custom TLS certificate, otherwise set to LetsEncrypt
      tlsCertificateProvider: LetsEncrypt

      auth:
        tenantId: 72f988bf-86f1-41af-91ab-2d7cd011db47
        apiAppUri: api://tyger-server
        cliAppUri: api://tyger-cli

      # buffers:
        # TTL for active buffers before they are automatically soft-deleted (D.HH:MM:SS) (0 = never expire)
        # activeLifetime: defaults to 0.00:00
        # TTL for soft-deleted buffers before they are automatically purged forever (D.HH:MM:SS) (0 = purge immediately)
        # softDeletedLifetime: default to 1.00:00

      # Optional Helm chart overrides
      # helm:
        # tyger:
          # repoName:
          # repoUrl: not set if using `chartRef`
          # chartRef: e.g. oci://...
          # version:
          # values:
```

All of the installation commands (`tyger cloud install`, `tyger api install`,
etc.) require you to give a path the the config file (`--file|-f PATH`).

You can validate this configuration with:

``` bash
tyger config validate -f PATH
```

You can also reformat it with the default comments with:

``` bash
tyger config pretty-print -i FILE.yml -o FILE.yml
```

## Install cloud resources

To create and configure the necessary cloud components for Tyger (Azure
Kubernetes Service, storage accounts, etc.), run:

```bash
tyger cloud install -f config.yml
```

If later on you need to make changes to your cloud resources, you can update the
config file and run this command again.

## Install authentication identities

Execute the following to install Entra ID applications for the `tyger` CLI and
the Tyger API. These are needed for OAuth authentication:

```bash
tyger identities install -f config.yml
```

This command is idempotent and can be run multiple times. You will receive errors if
you do not have sufficient permissions.

For calls to the Tyger API, the `tyger` CLI can use a service principal instead
of a user's identity. If you want to use a service principal for this purpose,
you can use an existing one or create a new one on your own.

## Install the Tyger API

The last step is to install the Tyger API, which can be done by running:

```bash
tyger api install -f config.yml
```

This command installs the Tyger API from the Helm chart in the
`mcr.microsoft.com/tyger/helm/tyger` registry, using a version baked into the `tyger`
CLI. Upgrade the server by updating the CLI and rerunning `tyger api install`.

The API's TLS certificate is automatically created using [Let's
Encrypt](https://letsencrypt.org/).

## Testing it out

Log in with the `tyger` CLI using the domain name specified in the config file
under `api.domainName`. For example:

```bash
tyger login https://demo-tyger.westus2.cloudapp.azure.com
```

This will take you through an interactive login flow similar to logging `az login`.

Once logged in, you should be able to run any of the core commands, such as:

```bash
tyger run list
```

## API Logs

If ever the Tyger API fails unexpectedly, you can inspect server logs with

```bash
tyger api logs -f config.yml [--follow] [--tail LINES]
```

`--follow` will stream new out new log lines as they are produced by the server.

`--tail` starts from the last N log lines.

## Uninstalling

To uninstall the Tyger API, run:

```bash
tyger api uninstall -f config.yml
```

Note: This does **not** delete any database data or buffers.

To uninstall all cloud resources, run:

```bash
tyger cloud uninstall -f config.yml
```

::: danger Warning
`tyger cloud uninstall` will permanently delete all database and buffer data.
:::


## Multi-tenancy

Tyger supports running separate services for separate organizations on shared
compute infrastructure. Each organization has its own service API domain name,
its own auth parameters, and separate database and storage resources. This
feature can be useful for supporting different organizations that need to use
Tyger while sharing compute infrastructure costs.

In the configuration file, multiple organizations can be added under the
`organizations` list. In order to support multiple organizations, you will need
to provide a custom Azure DNS zone resource (to control the records of a domain)
and provide a TLS certificate in PEM format in an Azure Key Vault.

When an environment has multiple organization the commands under `tyger cloud`,
`tyger api`, and `tyger identities` can be scoped to one or more organizations
using the `--org` flag. Some commands can only be applied to a single
organization.
