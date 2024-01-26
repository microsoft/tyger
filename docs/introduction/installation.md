# Installation

To use Tyger, you will need to have an Azure subscription and the [Azure
CLI](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli) installed.

The steps to installing Tyger are:

1. Install the `tyger` CLI
2. Create an installation config file
3. Install cloud resources
4. Install authentication identities
5. Install the Tyger API

After installing the `tyger` CLI, you will use it to perform the subsequent steps.

For step (3), you will need to have the Owner role at a resource group or the subscription level.

For step (4), you will need to have permissions to create applications in your
Microsoft Entra tenant.

Step (5) does not require either of these.

## Install the `tyger` CLI

`tyger` is a single executable file. The installation steps are:

1. Head over to the latest release page.
1. Download the right archive for your platform.
1. Extract the archive and find the `tyger` executable. Copy it to a directory in your PATH environment variable.

You should now be able to run `tyger --version`.

## Generate an installation config file

We will now generate an installation configuration file, which will be used for
subsequent installation steps.

Make sure you have logged in the Azure CLI using `az login`.

Now we will generate an installation configuration file. To do this, run

```bash
tyger config create
```

This runs an interactive wizard that results in a config file that is saved on
your system. You can get its path by running:

```bash
tyger config get-path
```

Once created, you should inspect the file and adjust its contents as needed.

The installation configuration file looks like this:

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
configurations values to be overridden on the command-line with `--set`. For
example:

```bash
tyger api install --set api.helm.tyger.chartRef=oci://tyger.azurecr.io/helm/tyger --set api.helm.tyger.version=v0.4.0
```


## Install cloud resources

You will now create and configure the necessary cloud components for Tyger
(Azure Kubernetes Service, storage accounts, etc.). To do this, simply run:

```bash
tyger cloud install
```

If you need to make changes to your cloud resources, you can update the config
file and run this command again.


## Install authentication identities

Run the following command installs Entra ID applications for the `tyger` CLI and
for the Tyger server. These are needed for OAuth authentication.

```bash
tyger identities install
```

This command is idempotent can be run multiple times. You will receive errors if
you do not have sufficient permissions.

The tyger CLI can use a service principal instead of a user's identity for calls
to the Tyger API. If you want to use a service principal for this purpose, you
may use an existing one or create a new one on your own.

## Install the tyger API

The last step is to install the Tyger API, which can be done by running

```bash
tyger api install
```

This installs the Tyger API from a Helm chart in the
`tyger.azurecr.io/helm/tyger` with a tag (version) that is baked into `tyger`
CLI executable. Update the CLI and run `tyger api install` again to upgrade the
server.

The API's TLS certificate is automatically created using [Let's
Encrypt](https://letsencrypt.org/).

## Testing it out

You will now log in with the `tyger` CLI. The domain name in in the config file
under the path `api.domainName`. The simplest way to log in is by running:

```bash
tyger login https://DOMAINNAME
```
e.g.

```bash
tyger login https://demo-tyger.westus2.cloudapp.azure.com
```

This will take you through an interactive login flow similar to logging `az login`.

Once login succeeds, you should be able to run any of the core commands, such as:

```bash
tyger run list
```

## Uninstalling

To uninstall the Tyger API, run:

```bash
tyger api uninstall
```

Note that this does **not** delete the data in the database, nor does it delete any buffers.

To uninstall all cloud resources, run:

```bash
tyger cloud uninstall
```

::: danger Warning
`tyger cloud uninstall` will permanently delete all database and buffer data.
:::
