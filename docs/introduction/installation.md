# Installation

To use Tyger, you need an Azure subscription and the [Azure
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
tyger config create
```

This command runs an interactive wizard, resulting in a config file saved
on your system. You can retrieve its path with:

```bash
tyger config get-path
```

Review and adjust the file's contents as needed.

The installation configuration file typically looks like this:

```yaml
environmentName: demo

cloud:
  tenantId: 2dda111b-8b49-4193-b6ad-7ad797aa552f
  subscriptionId: 17f10121-f320-4d47-928f-3d42adb68e01
  resourceGroup: demo
  defaultLocation: westus2

  # Optionally point an existing Log Analytics workspace to send logs to.
  # logAnalyticsWorkspace:
  #   resourceGroup:
  #   name:

  compute:
    clusters:
      - name: demo
        apiHost: true
        kubernetesVersion: 1.27
        # location: Defaults to defaultLocation

        userNodePools:
          - name: cpunp
            vmSize: Standard_DS12_v2
            minCount: 1
            maxCount: 10
          - name: gpunp
            vmSize: Standard_NC6s_v3
            minCount: 0
            maxCount: 10

    # These are the principals that will be granted full access to the
    # "tyger" namespace in each cluster.
    # For users, kind must be "User".
    #   If the user's home tenant is this subscription's tenant and
    #   is not a personal Microsoft account, set id to the user
    #   principal name (email). Otherwise, set id to the object ID (GUID).
    # For service principals, kind must also be "User" and id must
    # be the service principal's object ID (GUID).
    # For groups, kind must be "Group" and id must be the group's
    # object ID (GUID).
    managementPrincipals:
      - kind: User
        id: me@example.com

    # The names of private container registries that the clusters must
    # be able to pull from.
    # privateContainerRegistries:
    #   - myprivateregistry

  database:
    serverName: demo-tyger
    postgresMajorVersion: 16

    # Firewall rules to control where the database can be accessed from,
    # in addition to the control-plane cluster.
    firewallRules:
      - name: installerIpAddress
        startIpAddress: 99.99.99.99
        endIpAddress: 99.99.99.99

    # location: Defaults to defaultLocation
    # computeTier: Defaults to Burstable
    # vmSize: Defaults to Standard_B1ms
    # storageSizeGB: Defaults to 32GB (the minimum supported)
    # backupRetentionDays: Defaults to 7
    # backupGeoRedundancy: Defaults to false

  storage:
    # Storage accounts for buffers.
    buffers:
      - name: demowestus2buf
        # location: Defaults to defaultLocation
        # sku: Defaults to Standard_LRS

    # The storage account where run logs will be stored.
    logs:
      name: demotygerlogs
      # location: Defaults to defaultLocation
      # sku: Defaults to Standard_LRS

api:
  # The fully qualified domain name for the Tyger API.
  domainName: demo-tyger.westus2.cloudapp.azure.com

  auth:
    tenantId: 705ef40b-9fa6-45a3-ba0c-b7ced9af6dce
    apiAppUri: api://tyger-server
    cliAppUri: api://tyger-cli

  # Optional Helm chart overrides
  # helm:
  #   tyger:
  #     repoName:
  #     repoUrl: # not set if using `chartRef`
  #     chartRef: # e.g. oci://tyger.azurecr.io/helm/tyger
  #     namespace:
  #     version:
  #     values: # Helm values overrides

  #   certManager: {} # same fields as `tyger` above
  #   nvidiaDevicePlugin: {} # same fields as `tyger` above
  #   traefik: {} # same fields as `tyger` above

```

All of the installation commands (`tyger cloud install`, `tyger api install`,
etc.) allow you to give a path the the config file (`--file|-f PATH`)instead of
the default given by `tyger config get-path`. Additionally, the commands allow
configuration values to be overridden on the command-line with `--set`. For
example:

```bash
tyger api install \
  --set api.helm.tyger.chartRef=oci://tyger.azurecr.io/helm/tyger \
  --set api.helm.tyger.version=v0.4.0
```

## Install cloud resources

To create and configure the necessary cloud components for Tyger (Azure
Kubernetes Service, storage accounts, etc.), run:

```bash
tyger cloud install
```

If later on you need to make changes to your cloud resources, you can update the
config file and run this command again.

## Install authentication identities

Execute the following to install Entra ID applications for the `tyger` CLI and
the Tyger API. These are needed for OAuth authentication:

```bash
tyger identities install
```

This command is idempotent and can be run multiple times. You will receive errors if
you do not have sufficient permissions.

For calls to the Tyger API, the `tyger` CLI can use a service principal instead
of a user's identity. If you want to use a service principal for this purpose,
you can use an existing one or create a new one on your own.

## Install the Tyger API

The last step is to install the Tyger API, which can be done by running:

```bash
tyger api install
```

This command installs the Tyger API from the Helm chart in the
`tyger.azurecr.io/helm/tyger` registry, using a version baked into the `tyger`
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

## Uninstalling

To uninstall the Tyger API, run:

```bash
tyger api uninstall
```

Note: This does **not** delete any database data or buffers.

To uninstall all cloud resources, run:

```bash
tyger cloud uninstall
```

::: danger Warning
`tyger cloud uninstall` will permanently delete all database and buffer data.
:::
