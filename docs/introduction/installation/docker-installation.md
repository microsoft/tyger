# Docker installation

 To use tyger with Docker, you will need to have Docker installed on your local system.

 The steps for installing Tyger are:

1. Install the `tyger` CLI.
2. Create an installation config file.
5. Install the Tyger API.

After installing the `tyger` CLI, you will use it for the subsequent steps.

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
kind: docker

# Optionally specify the user id that the services will run as
userId:

# Optionally specify the user group ID that will be allowed
# to access the Tyger API
allowedGroupId:

# The port on which the data plane API will listen
dataPlanePort: 46339

# Specify asymmetric signing keys for the data plane service.
# These can be generated with `tyger api generate-signing-key`
# These files must not be stored in a source code repository.
signingKeys:
  primary:
    public: ${HOME}/tyger-signing-public.pem
    private: ${HOME}/tyger-signing.pem

  # Optionally specify a secondary key pair.
  # The primary key will always be used for signing requests.
  # Signature validation will accept payloads signed with either the
  # primary or secondary key.
  # secondary:
  #  private:
  #  public:

# Optionally specify container images to use.
# controlPlaneImage:
# dataPlaneImage:
# bufferSidecarImage:
# gatewayImage:
# gatewayImage:

```

All of the installation commands (`tyger api install`, `tyger api uninstall`,
etc.) require you to give a path the the config file (`--file|-f PATH`).


## Install the Tyger API

You are now ready to install the Tyger, API, which can be done by running:

```bash
tyger api install -f config.yml
```

::: warning Note
If using Windows, you will need to run this command from a WSL prompt.
:::

Tyger requires the directory `/opt/tyger` to exist. You many run the command
with `sudo` in order to create it. This path is currently not configurable.

## Testing it out

Log in with the `tyger` CLI using

```bash
tyger login --local
```

Once logged in, you should be able to run any of the core commands, such as:

```bash
tyger run list
```

## Uninstalling

To uninstall the Tyger API, run:

```bash
tyger api uninstall -f config.yml
```

Note: This does **not** delete any database data or buffers. To **permanently**
delete all data along with the API, run:

```bash
tyger cloud uninstall -f config.yml --delete-data
```

## Security and remote access

The local Docker mode uses Unix domain sockets for communication and access to
the Tyger API is controlled by file permissions on these sockets. You can use
the `userId` and `allowedGroupId` properties on the installation [config
file](#generate-an-installation-configuration-file) to set these permissions.

To access Tyger from another machine, the `tyger` CLI supports connecting to a
Tyger server over SSH. To do this, run:

```bash
tyger login ssh://user@host
```

For this to work, it must be possible to connect to the SSH host using SSH keys,
not a password.

The format of the SSH URL is:

```
ssh://[user@]host[:port][?cliPath=/path/to/tyger]
```

All values in `[]` are optional. The user and port default values will come from
your SSH config file (~/.ssh/config). The `cliPath` query parameter only needs
to be specified if `tyger` is not installed in a directory in the SSH host's
$PATH variable.

For the best user experience with SSH, configure ~/.ssh/config as follows to
allow reusing a SSH connection for multiple invocations of the `tyger` CLI:

```
ControlMaster     auto
ControlPath       ~/.ssh/control-%C
ControlPersist    yes
```
