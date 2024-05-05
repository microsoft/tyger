# Ephemeral buffers

::: warning Note

Ephemeral buffers are currently only supported in local Docker installations of
Tyger.

:::

While buffers are very powerful, there are cases when you don't want data to be
persisted and/or you want low-latency communication between a data source and a
run's container. For these scenario's you can use **ephemeral buffers**.

When using ephemeral buffers, instead of data being sent to the data plane
service (Azure Storage or the local data plane service), a sidecar container to
the job's container opens up an HTTP listener and streams data to or from the
named pipe that the job container reads or writes to. From the run code's
perspective, ephemeral buffers are thus no different from regular buffers.
However, there are a few important differences between the two:

- Ephemeral buffers can only be read once, and therefore are not WORM.
- Ephemeral buffers are not created with `tyger buffer create`. Rather, they are
  created with, and are owned by, a run.
- You can only read or write to an ephemeral buffer during while its associated
  run is active.

## Using ephemeral buffers

You can specify that a run should use ephemeral buffers using a config file:

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

  buffers:
    input: _
    output: _
```

The `input: _` and `output: _` means to use ephemeral buffers.

You can run this example with:

``` bash
echo "Paul" | tyger run exec -f hello.yml > result.txt
```


Instead of specifying `input: _` and `output: _` in the config file, you could
instead specify them as command-line arguments:

``` bash
echo "Paul" | tyger run exec -f hello.yml -b input=_ -b output=_ > result.txt
```
