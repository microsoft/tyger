# Azure cloud installation

To use Tyger with Azure, you need an Azure subscription and the [Azure
CLI](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli) installed.

The steps for installing Tyger are:

1. Install the `tyger` CLI.
2. Create an installation config file.
3. Set up access control.
4. Install cloud resources.
5. Install the Tyger API.

After installing the `tyger` CLI, you will use it for the subsequent steps.

For step (3), you will need permissions to create applications in your Microsoft
Entra tenant.

For step (4), you will need to have the Owner role at a resource group or the
subscription level.

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

  # Whether to use private networking. The default is false.
  # If true, Tyger service, storage storage accounts, and all other created resources
  # will not be accessible from the public internet.
  privateNetworking: false

  # Optionally point an existing Log Analytics workspace to send logs to.
  # logAnalyticsWorkspace:
    # resourceGroup:
    # name:

  # Optionaly use a custom DNS zone
  # dnsZone:
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
        kubernetesVersion: "1.32"
        sku:
        # location: defaults to Standard

        # An existing virtual network subnet to deploy the cluster into.
        # existingSubnet:
        #   resourceGroup:
        #   vnetName:
        #   subnetName:

        # A CIDR notation IP range from which to assign pod IPs.
        # This range must not overlap with the service CIDR range, the cluster subnet range,
        # and IP ranges used in peered VNets and on-premises networks.
        # podCidr: defaults to 10.244.0.0/16

        # A CIDR notation IP range from which to assign service cluster IPs.
        # This range must not overlap with the pod CIDR range, the cluster subnet range,
        # and IP ranges used in peered VNets and on-premises networks.
        # serviceCidr: defaults to 10.0.0.0/16

        # The IP address assigned to the Kubernetes DNS service.
        # It must be within the service address range specified in serviceCidr.
        # dnsServiceIp: defaults to 10.0.0.10

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
    #   traefik:
    #     repoName:
    #     repoUrl: not set if using `chartRef`
    #     chartRef: e.g. oci://...
    #     version:
    #     values:
    #   certManager:
    #   nvidiaDevicePlugin:

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
            # dnsEndpointType can be set to `AzureDNSZone` when creating large number of accounts in a single subscription.
            # dnsEndpointType: defaults to Standard.

        # defaultBufferLocation: Can be set if there are buffer storage accounts in multiple locations

        # The storage account where run logs will be stored.
        logs:
          name: demotygerlogs
          # location: defaults to defaultLocation
          # sku: defaults to Standard_LRS
          # dnsEndpointType can be set to `AzureDNSZone` when creating large number of accounts in a single subscription.
          # dnsEndpointType: defaults to Standard.

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

      accessControl:
        tenantId: 72f988bf-86f1-41af-91ab-2d7cd011db47
        apiAppUri: api://tyger-server
        cliAppUri: api://tyger-cli

        apiAppId: "" # `tyger access-control apply` will fill in this value
        cliAppId: "" # `tyger access-control apply` will fill in this value

        # Principals in role assignments are specified in the following ways:
        #
        # For users, specify the object ID and/or the user principal name.
        #   - kind: User
        #     objectId: <objectId>
        #     userPrincipalName: <userPrincipalName>
        #
        # For groups, specify the object ID and/or the group display name.
        #   - kind: Group
        #     objectId: <objectId>
        #     displayName: <displayName>
        #
        # For service principals, specify the object ID and/or the service principal display name.
        #   - kind: ServicePrincipal
        #     objectId: <objectId>
        #     displayName: <displayName>

        roleAssignments:
          owner:
            - kind: User
              objectId: 18c9e451-88aa-47d2-ae4f-1d34d55dc50c
              userPrincipalName: me@example.com

          contributor: []

      # buffers:
        # TTL for active buffers before they are automatically soft-deleted (D.HH:MM:SS) (0 = never expire)
        # activeLifetime: defaults to 0.00:00
        # TTL for soft-deleted buffers before they are automatically purged forever (D.HH:MM:SS) (0 = purge immediately)
        # softDeletedLifetime: default to 1.00:00

      # Optional Helm chart overrides
      # helm:
      #   tyger:
      #     repoName:
      #     repoUrl: not set if using `chartRef`
      #     chartRef: e.g. oci://...
      #     version:
      #     values:
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

## Set up access control

Tyger uses [Microsoft Entra
ID](https://www.microsoft.com/en-ca/security/business/identity-access/microsoft-entra-id)
for authentication and authorization. You will need to create app registrations
for the API and CLI so that users can obtain an OAuth token to the API, and
then explicitly grant users access to the API. Both of these can be done by editing
the config file and running:

``` bash
tyger access-control apply -f config.yml
```

The part of the config file to edit is under the path `organizations[*].api.accessControl`.

The first part of that section is parameters for authentication:

```yaml
tenantId: 72f988bf-86f1-41af-91ab-2d7cd011db47
apiAppUri: api://tyger-server
cliAppUri: api://tyger-cli

apiAppId: "" # `tyger access-control apply` will fill in this value
cliAppId: "" # `tyger access-control apply` will fill in this value
```

Review the values for `tenantId`, `apiAppUri`, and `apiAppUri`. `apiAppId` and
`cliAppId` will be updated with IDs when `tyger config apply` is run.

The next section determines who can access to the Tyger API. There are two
roles: owner and contributor. Owners can perform any operation. Contributors can
perform all operation except deleting, exporting, and importing buffers.

```yaml
roleAssignments:
  owner:
    - kind: User
      objectId: 18c9e451-88aa-47d2-ae4f-1d34d55dc50c
      userPrincipalName: me@example.com

  contributor: []
```

You can assign users, groups, and service principals to these two roles. To
specify a principal, provide its object ID and/or its userPrincipalName (for
users) or displayName (for groups and service principals).

`tyger access-control apply` is idempotent and can be run multiple times.
Normally, you would run it every time you want to add or remove role
assignments. Role assignments are stored in Entra ID and not in the Tyger API.
After updating role assignments, you will likely have to perform [`tyger
login`](../../guides/login.md) in order for the new role assignment to take
effect. This is because the roles assignments are part of the OAuth token that
Entra issues, and these tokens are typically valid for an hour and cached by the
`tyger` CLI.

## Install cloud resources

To create and configure the necessary cloud components for Tyger (Azure
Kubernetes Service, storage accounts, etc.), run:

```bash
tyger cloud install -f config.yml
```

If later on you need to make changes to your cloud resources, you can update the
config file and run this command again.

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
tyger cloud uninstall -f config.yml --all
```

::: danger Warning
`tyger cloud uninstall` will permanently delete all database and buffer data.
:::

## Private networking

Currently a preview feature, the entire Tyger environment can use private
networking, meaning that none of the endpoints can be accessed from the public
internet.

To enable this mode, you will need to create an Azure virtual network (VNet) for
the Kubernetes cluster to be deployed into. You will reference a subnet in this
VNet in the `cloud.compute.clusters.existingSubnet` field. To enable
private networking, set the `cloud.privateNetworking` field to `true`.

This mode cannot use Let's Encrypt for TLS certificate creation, and you will
need to provide your own TLS certificate in a referenced Key Vault.

If you want to use private networking for this Key Vault, you will need to
[create private
endpoints](https://learn.microsoft.com/en-us/azure/key-vault/general/private-link-service?tabs=portal)
for the Key Vault in the subnet before running `tyger cloud install`.

Similarly, if you want to use an Azure Container Registry that cannot be
publicly accessed, you will need to follow the
[instructions](https://learn.microsoft.com/en-us/azure/container-registry/container-registry-private-link)
to set up a private registry with private endpoints in the subnet.

You will need to run `tyger` commands, including the installation commands from
a virtual machine in this VNet, or set up peering and DNS forwarding to this
VNet.

To use Tyger from another Azure VNet, you can set up VNet peering or a
VNet-to-VNet VPN connection and configure DNS forwarding. Or you can create
private link endpoints for all the Tyger services in the other VNet. To use a
private Tyger environment from outside of Azure, you will need to configure DNS
forwarding and use a VPN or ExpressRoute.

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
