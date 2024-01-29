# Working with runs

A run in Tyger is the execution of a codespec with buffers provided as arguments
to the codespec's buffer parameters.

::: info Note

This guide does not cover runs with workers. Those are covered in [Distributed
Runs](distributed-runs.md).

:::

## Creating runs with `exec`

`tyger run exec` is a the easiest way to create and execute a run. It allows
up to one buffer's contents to be provided through standard input and up to one
buffer's output to be written to standard output.

First, create a codespec named `hello`:

```bash
tyger codespec create -f hello.yml
```

With `hello.yml` looking like this:

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

In each of these cases, `tyger run exec` creates buffers for `input` and
`output`, copies standard input to the input buffer, copies the output buffer to
standard output, and monitors the run until completion.

## Creating runs with `create`

`tyger run create` creates a run without waiting for its completion or reading
or writing to its buffers.

Using `tyger buffer create`, you accomplish the same as the previous example
with:

```bash
input_id=$(tyger buffer create)
output_id=$(tyger buffer create)

run_id=$(tyger run create --codespec hello --buffer input=$input_id --buffer output=$output_id)

echo "Paul" | tyger buffer write $input_id

tyger buffer read $output_id > result.txt
```

Notice how we pass in buffers as arguments to the codespec's buffer parameters.
Missing buffers arguments are automatically created and their IDs can be retrieved using
`tyger run show`.

## `exec` and `create` options

Both `tyger run create` and `tyger run exec` share the following command-line
parameters:

- `-f|--file`: A YAML file with the run specification. Other flags override file values.
- `--buffer`: Maps a codespec buffer parameter to a buffer ID. Can be specified for each buffer parameter.
- `-c|--codespec`: The name of the job codespec to execute.
- `--version`: The version of the job codespec. Defaults to the latest version if not provided.
- `-r|--replicas`: The number of parallel job replicas. The default is 1.
- `--timeout`: The run timeout duration, in formats like "300s", "1.5h", or "2h45m".
- `--tag`: Key-value tags for any buffer created by the job. Can be specified
  multiple times.
- `--cluster`: The target cluster name.
- `--node-pool`: The nodepool to run the job in.

### Run specification file

The `--file` argument points to a YAML file with these options:

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

## Run lifecycle

The output of `tyger run show` has a `status` field which will be one of the
following values:

- `Pending`: The run has been created but has not yet started executing. It
  could be waiting for nodes to spin up, other jobs to complete, the container
  image to be downloaded, etc.
- `Running`: The run is executing.
- `Failed`: The run failed. This could be because of a non-zero exit code, or
  because the job failed to start (e.g. the container image could not be
  downloaded), or its execution timed out. Note that runs are never restarted.
- `Succeeded`: The run completed with an exit code of 0.
- `Canceling`: Cancellation has been requested for this job.
- `Canceled`: The job has been canceled.

The `statusReason` field may have more information concerning failures, but
often you will want to view a run's logs to determine the cause of failure.

## Showing runs

You can display the status and definition of a run with:

```bash
tyger run show ID
```

ID corresponds to the ID returned by `tyger run create`. If using `tyger run
exec`, the ID is included in the standard error logs.

## Watching runs

You can monitor a run's status in real-time:

```bash
tyger run watch ID [--full-resource]
```

This will write out a JSON line whenever the status of the run changes until it
reaches a terminal state. By default, it only includes system metadata fields.
To print the entire resource, specify `--full-resource`.

## Listing runs

List runs with:

```bash
tyger run list [--since DATE/TIME] [--limit COUNT]
```

Runs are listed in descending order of creation time. If `--limit` is not
specified, a maximum of 1000 runs are shown with a warning if the output had to
be truncated.

::: info Tip

Use `tyger run list --limit 1` to fetch the most recent run.

:::

## Cancel a run

You can cancel a job with:

```bash
tyger run cancel ID
```

This an asynchronous command and the run may continue executing for some time
before being terminated.

## Viewing logs

You can retrieve the logs for a run using:

```bash
tyger run logs
    ID
    [-f|--follow]
    [-s|--since DATE/TIME]
    [--tail NUM]
    [--timestamps]
```

This returns a chronologically merged view of all standard output and error messages from all of
the run's containers.

`--follow` streams logs to standard output as they are written until the
run completes.

`--since` only shows logs after a a given time.

`--tail` only shows new last N log lines.

`--timestamps` prefixes each line with its timestamp.

When a run completes, the logs are archived to a storage account so that they
can be retrieved later.

::: info Tip

The `exec` command can stream run logs to standard error if `--logs` is
specified.

:::
