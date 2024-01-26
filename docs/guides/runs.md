# Working with runs

A run combines a codespec with buffers and executes in a Tyger Kubernetes
cluster. Buffers serve as parameters to the buffer parameters defined in the
codespec.

::: info Note

This does not discuss runs with workers. Those are covered in [Distributed
runs](distributed-runs.md).

:::

## Creating runs with `exec`

`tyger run exec` is the easiest way to create and execute a run. It supports up to one buffer's contents being provided though standard in, and up to one buffer written to standard out.

Let's first create a codespec named `hello`:

```bash
tyger codespec create -f hello.yml
```

With `hello.yml` being:

```yaml
name: hello
image: ubuntu
buffers:
  inputs: ["input"]
  outputs: ["output"]
command:
  - "bash"
  - "-c"
  - |
    set -euo pipefail
    inp=$(cat "$INPUT_PIPE")
    echo "Hello ${inp}" > "$OUTPUT_PIPE"
```

We can now do this:

```bash
echo "Paul" | tyger run exec --codespec hello > result.txt
```

This will write status information to standard error and "Hello Paul" to
results.txt.

You can also provide the run specification as a file:

For example:

```bash
echo "Paul" | tyger run exec -f hello.yml > result.txt
```

Where the contents of `hello.yml` would be:

```yaml
job:
  codespec: hello
```

Or instead of referencing a codespec, you can declare an anonymous one inline:

```yaml
job:
  codespec:
    image: ubuntu
    buffers:
      inputs: ["input"]
      outputs: ["output"]
    command:
      - "bash"
      - "-c"
      - |
        set -euo pipefail
        inp=$(cat "$INPUT_PIPE")
        echo "Hello ${inp}" > "$OUTPUT_PIPE"
```

In each of these cases, `tyger run exec` creates buffers for the `input` and
`output` buffer parameters, copies standard in to the input buffer, copies the
output buffer to standard out, and creates and watches the run until it
completes.

## Creating runs with `create`

While exec combines synchronously executes a run, `tyger buffer create` creates
a run but does not wait on its completion, not does to read or write to its
buffers.

Using `tyger buffer create`, you would rewrite the above example as:

```bash
input_id=$(tyger buffer create)
output_id=$(tyger buffer create)

run_id=$(tyger run create --codespec hello --buffer input=$input_id --buffer output=$output_id)

echo "Paul" | tyger buffer write $input_id

tyger buffer read $output_id > result.txt
```

Notice how we pass in buffers as arguments to the codespec's buffer parameters.
Any buffers that are not given as arguments are created automatically. Their IDs
can be retried by running `tyger run show`.

## `exec` and `create` options

Both `tyger run create` and `tyger run exec` have the following command-line parameters

`-f|--file`: a YAML file with the run specification. All other flags override the values in the file.

`--buffer`: maps a codespec buffer parameter to a buffer ID. Can be specified multiple times.

`-c|--codespec`: the name of the job codespec to execute.

`--version`: the version of the job codespec to execute. The latest version is
used if this is not given.

`-r|--replicas` the number of parallel job replicas. Defaults to 1.

`--timeout`: how log before the run times out. Specified in a sequence of decimal
numbers, each with optional fraction and a unit suffix, such as "300s", "1.5h"
or "2h45m". Valid time units are "s", "m", "h"

`--tag`: add a key-value tag to be applied to any buffer created by the job. Can
be specified multiple times.

`--cluster`: specify the name of the cluster to target.

`--node-pool`: the name of the nodepool to execute the job in.

### Run specification file

The `--file` argument points to a YAML file where these options are specified.
Here is a commented specification file with all fields specified:

```yaml
# Every run has a "job"
job:

  # The codespec to run. This can either be:
  # 1. A versioned reference: <name>/versions/<version>
  # 2. A simple reference (which will use the latest version): <name>
  # 3. An inline codespec definition
  codespec: mycodespec/versions/22

  # Buffer arguments to the parameters defined in the codespec
  # in the form <parameter>: <buffer id>
  # Any missing buffers will be created automatically.
  buffers:
    input: lopoahtz7chepdpmgvunuvtqke

  # Tags to set on automatically created buffers.
  tags:
    mykey: myvalue

  # The name of the nodepool to run in
  nodePool: cpunp

  # The number of replicas.
  replicas: 1

# The name of the cluster to run in.
cluster: mycluster

# The run is given this amount of time to complete,
# starting from when the run was created, not when it
# when it started executing.
timeoutSeconds: 43200
```

## Showing runs

You can display the status and definition of a run with

```bash
tyger run show ID
```

ID corresponds to the ID returned by `tyger run create`. If using `tyger run
exec`, the ID is written in the stand error logs.

## Listing runs

You can list runs with the command:

```bash
tyger run list [--since DATE/TIME] [--limit COUNT]
```

`--since` can be a datatime or a relative string such as "1 hour ago".

If `--limit` is not provided, 1000 runs are returned and a warning is written to
standard error if more exist.

Runs are ordered by descending creation time.

::: info Tip

To get the most recent run, you can write:

```bash
tyger run list --limit 1
```

:::

## Run lifecycle

The output of `tyger run show` has a `status` field which will be one of the
following values:

- `Pending`: The run has been created but has not yet started executing. It
  could be waiting for nodes to spin up, other jobs to complete, the container
  image to be downloaded, etc.
- `Running`: The run is executing.
- `Failed`: The run failed. This could be because of a non-zero exit code, or
  because the job failed to start (e.g. the container image could not be
  downloaded). or its execution timed out. Note that runs are never restarted.
- `Succeeded`: The run completed with an exit code of 0.
- `Canceling`: Cancellation has been requested for this job.
- `Canceled`: The job has been canceled.

The `statusReason` field may have more information concerning failures, but
often you will want to view a run's logs to determine the cause of failure.

## Viewing logs

You can retrieve the logs for a run using the command

```bash
tyger run logs
    ID
    [-f|--follow]
    [-s|--since DATE/TIME]
    [--tail NUM]
    [--timestamps]
```

This return everything that all the containers that make up the run wrote to
standard out and standard error, ordered by the time the line was written.

`--follow` keeps writing logs as they are written by the containers until the
run completes.

`--since` only shows logs after a a given time

`--tail` only shows new last N log lines.

`--timestamps` prefixes each line with the timestamp that it was written

When a run completes, the logs are archived in a storage account so that they
can be retrieved later.

::: info Tip

The `exec` command can also stream run logs to standard error if `--logs` is
specified.

:::
