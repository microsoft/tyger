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

Once created, you should inspect the file and adjust its contents as needed. See [Installation configuration file](../reference/config.md) for more details on this file.

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
