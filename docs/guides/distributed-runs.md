# Distributed runs


Every run is made up of a "job" codespec, which runs until it exits. A run can
optionally use a set of workers for work distribution. These workers use a
different codespec and typically will start listening on a network port. The job
can then communicate with these worker over the local Kubernetes cluster
network.

For a distributed run, you will need to define a codespec of kind `worker`.

To create a worker codespec, you will run:

```bash
tyger codespec create --kind worker [...] [--endpoint NAME=PORT]
```

For worker codespecs, you can optionally specify endpoints, which are named
ports that the worker will be listening on.

If using a specification file, you will need to provide:

```yaml
kind: worker

# ...

endpoints:
    name: port
```

To create distributed run, there are some additional command-line parameters to
`tyger run create`:


`--worker-codespec`: the name of the worker codespec to execute

`--worker-version`: the version of the worker codespec. The latest version is
used if not provided.

`--worker-node-pool`: the name of the nodepool to execute the workers in

`--worker-replicas`: the number of parallel workers to run. Defaults to 1.

When creating a run from a specification file, the following fields can be set:

```yaml
# ...

# a worker specification, which mostly has the
# same fields as the job specification.
worker:

  # The codespec reference (optionally versioned)
  # or a codespec defined inline.
  codespec: myworkercodespec/versions/33

  # The name of the nodepool that the workers should run in.
  nodePool: gpunp

  # The number of worker replicas.
  # Defaults to 1.
  replicas: 2
```

## Worker discovery

The job container only begins executing when all worker containers are up.The job can then discover the hostnames of the worker nodes with the environment variable `TYGER_WORKER_NODES`. This will be a JSON array of strings.

For each endpoint that the worker declares in the codespec, there is an environment variable named `TYGER_<ENDPOINT_NAME_UPPERCASE>_WORKER_ENDPOINT_ADDRESSES`, where `<ENDPOINT_NAME_UPPERCASE>` is the endpoint name in uppercase. The value is a JSON array of the strings in the form `hostname:port`.
