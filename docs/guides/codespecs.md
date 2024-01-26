# Working with codespecs

Codespecs are a specification of the code to execute during a Tyger run. Named
codespecs are reusable and can be reused across many runs. Runs can also declare
codespecs inline. In this guide, we will work with named codespecs.

Codespecs are created with the `tyger codespec create` command. The properties of the codespec can be given as command-line arguments:

```bash
tyger codespec create \
    negatingcodespec \
    --image quay.io/linuxserver.io/ffmpeg \
    --input input \
    --output output \
    -- -i '$(INPUT_PIPE)' -vf negate -f- nut -y '$(OUTPUT_PIPE)'
```

Alternatively, you could create the same codespec with a specification file:

```bash
tyger codespec create -f negating.yml
```

With `negating.yml` looking like this:

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

Any properties specified on the command-line override values given in the spec
file.

When successful, `tyger codespec create` outputs the version of the codespec. A
codespec can have many versions, but each version is immutable. The first
command above would return 1 as the version. The second command would also
return 1, because it the specification is the same as the latest version. But we
could specify something different, for example:

```bash
tyger codespec create -f negating.yml --env MY_ENV=MY_VALUE
```

This sets an environment variable on the codespec and would result in a new version of the codespec being created.

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

# The maximum number of replicas this codepec can have.
# Applies only to worker codespecs. The default is 1.
maxReplicas: 1

# Applies only to worker codespecs.
# Declares the TCP ports that workers will be listening on.
endpoints:
  myEndpoint: 8888
```

::: info Note

Properties specific to worker codespecs are explained in [Distributed runs](distributed-runs.md).

:::

These values can be provided as command-line arguments:

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

Whatever appears after the `--` is treated as `args` for the codespec, unless
`--command` is set, in which case it is treated as the `command` value.

## Showing codespecs

You can retrieve a codespec with the command:

```bash
tyger codespec show NAME [-v|--version VERSION]
```

If `--version` is not provided, the latest version of the codespec is returned.

You can list codespecs with the command:

```bash
tyger codespec list [--prefix STRING] [--limit COUNT]
```

Codespecs are returned in alphabetical order, up to the `--limit` value
provided. If no limit is provided, 1000 codespecs are returned and a
warning is written to standard error if more exist.

Use the `--prefix` argument to only show codespecs that start with the given
string, which is case-sensitive.
