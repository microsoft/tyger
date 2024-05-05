# Distributed Runs

::: warning Note

Distributed runs are not supported when Tyger is installed in a local Docker
environment.

:::

All runs in Tyger use a "job" codespec for primary execution. Distributed runs
additionally employ workers for distributing workloads. These workers, defined
by a separate codespec, typically listen on network ports so that the the job
can communicate with them over the cluster's local network.

## Creating a worker codespec

To create a worker codespec, use:

```bash
tyger codespec create --kind worker [...] [--endpoint NAME=PORT]
```

For worker codespecs, you can specify endpoints, which are named ports that the
worker will be listening on. Worker codespecs do not support buffer parameters.

When using a specification file to create the codespec, include the following:

```yaml
kind: worker

# ...

endpoints:
  name: port
```

## Creating a Distributed Run

Creating a distributed run requires additional parameters for `tyger run create`:

- `--worker-codespec`: The name of the worker codespec.
- `--worker-version`: The version of the worker codespec. Defaults to the latest
  version if unspecified.
- `--worker-node-pool`: The name of the nodepool for executing workers.
  Optional.
- `--worker-replicas`: The number of parallel workers. Defaults to 1.

If using a specification file, a distributed run must include a top-level
`worker` field:

```yaml
# ...

# a worker specification, which mostly has the
# same fields as the job specification.
worker:

  # The codespec reference (optionally versioned)
  # or a codespec defined inline.
  codespec: myworkercodespec/versions/33

  # Optional name of the nodepool that the workers should run in.
  nodePool: gpunp

  # The number of worker replicas.
  # Defaults to 1.
  replicas: 2
```

## Worker Discovery

The job container only starts when all worker containers are up. The job
can discover worker node hostnames via the `TYGER_WORKER_NODES` environment
variable, which contains a JSON array of strings.

For each endpoint declared in the worker codespec, there is a corresponding
environment variable
`TYGER_<UPPERCASE_ENDPOINT_NAME>_WORKER_ENDPOINT_ADDRESSES`. This variable,
where `<UPPERCASE_ENDPOINT_NAME>` is the endpoint name in uppercase, holds a
JSON array of `hostname:port` strings.

## Example

[Gadgetron
examples](../reference/gadgetron/gadgetron.md#distributed-reconstruction) has an
example that uses a disributed run.
