# What is Tyger?

Tyger is a framework for remote signal processing. It enables reliable
transmission of data to remote computational resources, where the data can be
processed and transformed as it streams in. It was designed for streaming raw
signal data from an MRI scanner to the cloud, where much more compute power is
typically available to reconstruct images from the signal. However, its
application is not limited to MRI and it could be used in a variety of domains
and scenarios.

At a high level, Tyger is a REST API that abstracts over an Azure Kubernetes
cluster and Azure Blob storage. Future plans include support for on-prem
deployments. It includes a command-line tool, `tyger`, for easy interaction with
this API. Users specify signal processing code as a container image.

Tyger is centered around **stream processing**, allowing data to be processed as
it is acquired, without needing to wait for the complete dataset. It is based on
an **asynchronous** model, where data producers to do not need to wait for the
availability of data consumers. Additionally, data consumers can operate during
or after data production, since data streams are Write Once Read Many
(**WORM**).

Signal processing code can be written in any language, as long as it can read
and write to named pipes (which are file-like but do not support random access).
There is no SDK, meaning you can develop, test, and debug code on your laptop
using only files, without Tyger dependencies. Then, you build a container image
to run the same code in the cloud with Tyger.

Tyger is designed to be both powerful and easy to use. Its implementation is
also simple, since a lot of the heavy lifting is done by proven technologies
like Kubernetes and Azure Blob Storage.

## A Simple Example

::: tip

If you've already followed the [installation instructions](installation.md), you
can run this example too!

First, make sure you have [`ffmpeg`](https://ffmpeg.org/download.html) (which
comes with `ffplay`).

Next, download the sample video used in this exercise:

```bash
curl -OL https://aka.ms/tyger/docs/samples/hanoi.nut
```
:::

Suppose we have a video file named `hanoi.nut`. We can pipe its contents to
[`ffplay`](https://ffmpeg.org/ffplay.html), a video player. (Normally, you would
play the file directly in ffplay, but for this example, we explicitly want to
work with pipe streams.)

You can do this with the following command line:

```bash
cat hanoi.nut \
| ffplay -autoexit -
```

The first couple of seconds of this video look like this:

![Original Video](hanoi.gif)

Now, suppose we want to apply a color negation filter to this video stream. We
can do this by adding an ffmpeg step in the pipeline:

```bash:line-numbers{2}
cat hanoi.nut \
| ffmpeg -i pipe:0 -vf negate -f nut pipe:1 \
| ffplay -autoexit -
```

The output video will look like this:

![Converted Video](hanoi_negated.gif)

Now let's run the color negation in the cloud. To do that, modify the pipeline
as follows, which now uploads the input data to the cloud to apply the filter and
feeds the downloaded result into ffplay:

```bash:line-numbers{2}
cat hanoi.nut \
| tyger run exec -f negate.yml \
| ffplay -autoexit -
```

`negate.yml` is a run configuration file that looks like this:

```yaml
job:
  codespec:
    image: quay.io/linuxserver.io/ffmpeg
    buffers:
      inputs:
        - input
      outputs:
        - output
    args:
      - -i
      - $(INPUT_PIPE)
      - -vf
      - negate
      - -f
      - nut
      - -y
      - $(OUTPUT_PIPE)
```

This file specifies running an FFmpeg container image, declaring input and
output "buffer" parameters, and includes command-line arguments for `ffmpeg`.
These arguments are similar to the earlier example, except that inputs and
outputs are pipes to these buffers.

## Concepts

Tyger is built around three main concepts: buffers, codespecs, and runs. Buffers
are used for transporting data, codespecs describe the code to run, and runs
execute a codespec, reading and writing to buffers.

### Buffers

A buffer is an abstraction over an Azure Blob storage container. Data streams
are split into fixed-size blobs (files) with a sequential naming scheme and
uploaded and downloaded in parallel. The result is a conceptually similar to a
queueing service, but simpler and optimized for a single writer that can produce
gigabits or even tens of gigabits per second.

Advantages of this design include:

- Simplicity and reliability, thanks to the use of Azure storage services.
- Decoupled producers and consumers, allowing asynchronous data transmission.
- Data is readable multiple times.
- High throughput achieved through parallelism.
- Resilience to network failures via simple HTTP retries.
- Data in motion is secured with standard TLS.

### Codespecs

Codespecs are reusable specifications for code execution in a Tyger cluster.
They define a container image, command-line arguments, environment variables,
required resources (like GPUs and memory), and buffer parameters. Buffer
parameters, like function parameters, allow a codespec to be reused for many
runs.

### Runs

Runs are instances of codespecs executed in a cluster, with buffer instances as
arguments. They execute until the container process exits.
