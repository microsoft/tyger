# What is Tyger?

Tyger is a framework for remote signal processing. It allows reliably
transmitting data to remote computational resources where the data can be
processed and transformed as it streams in. It was designed for streaming the
raw signal data that an MRI scanner acquires to the cloud, where much more
compute power is typically available to reconstruct the signal into images.
However, nothing ties it to MRI and it can be used for a variety of domains and
scenarios.

At a high level, Tyger is a REST API that provides an abstraction over an Azure
Kubernetes cluster and Azure Blob storage, though we intend to support on-prem
deployments in the future. It ships with a command-line tool, `tyger`, that
facilitates interacting with this API. Signal processing code is specified as a
container image.

Tyger is built around **stream processing**, where data can be processed as it
is acquired, without first waiting for a the dataset to have been completely
produced. It is also a based on an **asynchronous** model, where data producers
do not need to wait for data consumers (processors) to be available before
sending data, and similarly data consumers can run during data production or
after. Data streams are Write once read many (**WORM**).

Signal processing code can be written in any language and only needs to be able
to read and write to named pipes (which are essentially files that do no support
random access). There is no SDK. This that you can write, test, debug code on
your laptop using only files and without anything to do with Tyger, and build a
container image and run the same code in the cloud using Tyger.

Tyger is designed to be powerful and easy to use. Its implementation is also
really simple, thanks to it being built on battle-tested technologies like
Kubernetes and Azure Blob Storage.

## A simple example

::: tip

If you've already followed the [installation instructions](installation.md), you
can run this example too!

First, make sure you have [`ffmpeg`](https://ffmpeg.org/download.html) (which
comes with `ffplay`).

Next, download the sample video that we'll use for this exercise and you'll be all set.

```bash
curl -OL https://aka.ms/tyger/docs/samples/hanoi.nut
```
:::

Suppose we have a video file called `hanoi.nut`. We can pipe it contents to the
[`ffplay`](https://ffmpeg.org/ffplay.html) video player to view it. (Normally, you would just
play the file directly in ffplay, but here we explicitly want to work with pipe
streams).

You can do this with the following command line:

```bash
cat hanoi.nut \
| ffplay -autoexit  -
```

The first couple of seconds of this video look like this:

![Original Video](hanoi.gif)

Now, let's say we want to transform this video stream by applying a color
negation filter. We can do that by inserting another ffmpeg step in the
pipeline:

```bash:line-numbers{2}
cat hanoi.nut \
| ffmpeg -i pipe:0 -vf negate -f nut pipe:1 \
| ffplay -autoexit -
```

The output video would now look something like this:

![Converted Video](hanoi_negated.gif)

Now let's run the color negation in the cloud. To do that, we change our
pipeline to the following, which will send the input data to the cloud where a
"run" will perform the data and then we'll feed the result into ffplay:

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

For now, we'll ignore the details of this file, but in summary, it says to run
an FFmpeg container image, it declares input and output "buffer" parameters, and
specifies command-line arguments to `ffmpeg` that are very similar to the
example before, except that the input and outputs are expressed in terms of
pipes to these buffers.

## Concepts

Tyger is built around three main concepts: buffers, codespecs, and runs. Buffers
are used for transporting data, codespecs describe what code to run, and runs
are the execution of a codespec that read and write to buffers.

### Buffers

A buffer is an abstraction over an Azure Blob storage container. Data streams
are broken up into (usually) fixed-size blobs (files) with a sequential naming
scheme and uploaded and downloaded in parallel. It's conceptually similar to a
service like Apache Kafka, but much simpler and optimized for a single writer
than can produce gigabits or even tens of gigabits per second.

There are several advantages to this design:

- It's extremely simple and Azure storage is an extremely reliable service.
- Producers and consumers are decoupled. The producer can send its data without
  waiting for the consumer to come online.
- Data can be read again and again.
- We can achieve throughput can be achieved by simple parallelism,
- We can recover from network transmission failures by doing simple HTTP
  retries.
- Data in motion is protected with standard TLS.

We issue time-bound access tokens to read or write to buffers and always verify
the integrity of the contents with hashes.

### Codespecs

Codespecs are a reusable specification of the code to run in a Tyger cluster. It
specifies a container image, command-line arguments, environment variables,
required resources (like GPUs and memory), and a set of buffer parameters.
Buffer parameters do not refer to actual buffer instances, since codespecs are
meant to be reusable, but are instead a lot like function parameters. A codespec
declares zero or more input and zero or more output buffer parameters. These can
be referred to in the codespec's command-line arguments and environment
variables.

### Runs

Runs are instances of a codespec that are executed in a cluster and provided
buffer instances as arguments to the codespec's buffer parameters. Runs execute
until the container process exits.

Runs can optionally declare a codespec inline instead of referencing an existing
codespec.
