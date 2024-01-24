# What is Tyger?

Tyger is a framework for remote signal processing. It allows reliably
transmitting data to remote computational resources where it can be processed
and transformed as it streams in. The scenario it was designed for is streaming
the raw signal data that an MRI scanner acquires to the cloud, where much more
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

## Example

Suppose we have a video file called `hanoi.mp4`. We can use
[FFmpeg](https://ffmpeg.org/) pipe a video stream to stdout and
[ffplay](https://ffmpeg.org/ffplay.html) to view it. (Normally, you would just
play the file directly in ffplay, but here we explicitly want to work with pipe
streams).

You can do this with the following command line:

```bash
ffmpeg -i hanoi.mp4 -f nut pipe:1 \
| ffplay -autoexit  -
```

The first couple of seconds of this video look like this:

![Original Video](/hanoi.gif)

Now, let's say we want to transform this video stream by applying an edge
detection filter. We can do that by inserting another ffmpeg step in the
pipeline:

```bash:line-numbers{2}
ffmpeg -i hanoi.mp4 -f nut pipe:1 \
| ffmpeg -i pipe:0 -vf edgedetect -f nut pipe:1 \
| ffplay -autoexit -
```

The output video would now look something like this:

![Converted Video](/hanoi_edge.gif)

Now let's use Tyger. We'll pipe the input data to the cloud, perform the edge
detection there, and stream the transformed video back. We'll do this with the
following pipeline:

```bash:line-numbers{2}
ffmpeg -i hanoi.mp4 -f nut pipe:1 \
| tyger run exec -f edgedetect.yml \
| ffplay -autoexit -
```

Where `edgedetect.yml` looks like this:

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
      - edgedetect
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

Tyger is built around three main concepts: buffers, codespecs, and runs.

### Buffers


### Codespecs


### Runs
