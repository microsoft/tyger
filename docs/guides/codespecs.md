# Working with codespecs

Codespecs in Tyger define the code executed during a run. Named codespecs can be
used by for multiple runs. While can also declare codespecs inline, this guide
focuses on working with named codespecs.

## Creating a codespec

Create a codespec using the `tyger codespec create` command. You can set the
properties of the codespec via command-line arguments:

```bash
tyger codespec create \
    negatingcodespec \
    --image quay.io/linuxserver.io/ffmpeg \
    --input input \
    --output output \
    -- -i '$(INPUT_PIPE)' -vf negate -f nut -y '$(OUTPUT_PIPE)'
```

Alternatively, you could create the same codespec with a specification file:

```bash
tyger codespec create -f negating.yml
```

`negating.yml` would be structured as follows:

```yaml
name: negatingcodespec
buffers:
  inputs:
    - input
  outputs:
    - output
image: quay.io/linuxserver.io/ffmpeg
args:
  - $(INPUT_PIPE)
  - -vf
  - negate
  - -f-
  - nut
  - -y
  - $(OUTPUT_PIPE)
```

Command-line arguments override values specified in the spec file.

`tyger codespec create` outputs the codespec version upon success. Each version
is immutable. If you create a codespec with specifications identical to the
current version, it will return the same version number. However, a change in
the specification, such as adding an environment variable, results in a new
version:

```bash
tyger codespec create -f negating.yml --env MY_ENV=MY_VALUE
```

## Codespec properties

Here is a commented specification file with all fields specified:

```yaml
# The codespec kind: "job" or "worker". The default is "job".
kind: job

# The name of the codespec. Required for named codespecs
name: negatingcodespec

# Buffer parameters.
# Each run crated with this codespec must provide
# a buffer for each of these parameters.
# Not supported on worker codespecs.
buffers:
  # The names of the buffers parameters that the runs will read from.
  inputs:
    - input
  # The names of the buffers parameters that the runs will write to.
  outputs:
    - output

# The container image to run
image: quay.io/linuxserver.io/ffmpeg

# Entrypoint array. Not executed within a shell.
# The container image's ENTRYPOINT is used if this is not provided.
# Variable references $(VAR_NAME) are expanded using the container's
# environment.The $(VAR_NAME) syntax can be escaped with a double $$, ie: $$(VAR_NAME).
command:
  - ffmpeg

# Arguments to the entrypoint. The container image's CMD is used if
# this is not provided. Variable references $(VAR_NAME) are expanded
# using the container's environment. If a variable cannot be resolved,
# the reference in the input string will be unchanged. The $(VAR_NAME)
# syntax can be escaped with a double $$, ie: $$(VAR_NAME).
args:
  - $(INPUT_PIPE)
  - -vf
  - negate
  - -f-
  - nut
  - -y
  - $(OUTPUT_PIPE)

# The container's working directory. If not specified, the container
# runtime's default will be used, which might be configured in the
# container image.
workingDir: /some/path

# A map of environment variables to inject into the container.
# Variable references $(VAR_NAME) are expanded using previously
# defined environment variables in the container. The $(VAR_NAME)
# syntax can be escaped with a double $$, ie: $$(VAR_NAME).
env:
  MY_VAR: myValue

# Compute Resources required by the container.
# All quantities are strings in the format described in
# https://kubernetes.io/docs/reference/kubernetes-api/common-definitions/quantity/
resources:
  # The minimum amount of compute resources required.
  requests:
    # CPU cores required.
    # See https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#meaning-of-cpu
    cpu: 1

    # Memory required.
    # See https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#meaning-of-memory
    memory: 1G

  # The maximum amount of compute resources allowed.
  limits:
     # Maximum CPU cores a container can use.
    # See https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#meaning-of-cpu
    cpu: 1

    # Maximum memory this container can use.
    # See https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/#meaning-of-memory
    memory: 1G

  # The number of GPUs required.
  gpu: 1

# The maximum number of replicas this codespec can have. The default is 1.
maxReplicas: 1

# Applies only to worker codespecs.
# Declares the TCP ports that workers will be listening on.
endpoints:
  myEndpoint: 8888
```

::: info Note

Properties specific to worker codespecs are explained in [Distributed runs](distributed-runs.md).

:::

These properties can also be provided via command-line arguments:

```
tyger codespec create
    NAME
    [--image IMAGE]
    [--kind job|worker]
    [--max-replicas REPLICAS]
    [[--input BUFFER_NAME] ...] [[--output BUFFER_NAME] ...]
    [[--env "KEY=VALUE"] ...]
    [[ --endpoint SERVICE=PORT ]]
    [--gpu QUANTITY]
    [--cpu-request QUANTITY]
    [--memory-request QUANTITY]
    [--cpu-limit QUANTITY]
    [--memory-limit QUANTITY]
    [--command] [ -- [COMMAND] [args...]]
```

Entries after `--` are treated as `args` for the codespec, unless `--command` is
specified, in which case they are treated as the `command` value.

## Showing codespecs

Retrieve a specific codespec version with:

```bash
tyger codespec show NAME [-v|--version VERSION]
```

Without `--version`, the latest version is returned.

## Listing codespecs

List **latest version** of codespecs with:

```bash
tyger codespec list [--prefix STRING] [--limit COUNT]
```

Codespecs are listed alphabetically up to the `--limit` value. If no limit is
set, a maximum of 1000 codespecs are returned with a warning if more exist.

Use `--prefix` to filter codespecs that start with a specific case-sensitive
string.
