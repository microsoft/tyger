# Gadgetron examples

[Gadgetron](https://github.com/gadgetron/gadgetron) is an open source project
for medical image reconstruction and can be used with Tyger. These examples
assume that you have `ismrmrd` installed.

## Basic example

Generate some test data with:

```bash
ismrmrd_generate_cartesian_shepp_logan
```

This will generate a `testdata.h5` file. You can then run a reconstruction with:

```bash
ismrmrd_hdf5_to_stream -i testdata.h5 --use-stdout \
| tyger run exec -f basic_gadgetron.yml \
| ismrmrd_stream_to_hdf5 --use-stdin -o out_basic.h5
```

`basic_gadgetron.yml` looks like this:

```yaml
<!--@include: ./basic_gadgetron.yml-->
```

## Using dependent measurements

It is common in MRI to perform a noise reference scan before the main scan. The
noise scan is used to estimate a noise covariance matrix and the subsequent scan
is reconstructed after noise pre-whitening using this covariance matrix.

For this example, download the noise and main scan raw data files:

```bash
curl -OL https://aka.ms/tyger/docs/samples/dependencies/noise_scan.h5
curl -OL https://aka.ms/tyger/docs/samples/dependencies/main_scan.h5
```

### Compute noise covariance matrix

Start by creating buffers for the noise data and covariance matrix:

```bash
noise_buffer_id=$(tyger buffer create)
noise_covariance_buffer_id=$(tyger buffer create)
```

Then write the noise data to the buffer:

```bash
ismrmrd_hdf5_to_stream -i noise_scan.h5 --use-stdout \
    | tyger buffer write $noise_buffer_id
```

Now compute the noise covariance matrix using the two buffers:

```bash
tyger run exec -f noise_gadgetron.yml \
    -b input=$noise_buffer_id \
    -b noisecovariance=$noise_covariance_buffer_id
```

`noise_gadgetron.yml` looks like this:
```yaml
<!--@include: ./noise_gadgetron.yml-->
```

### Reconstruction

We are ready to reconstruct the main scan data by referencing the noise
covariance matrix buffer:

```bash
ismrmrd_hdf5_to_stream -i main_scan.h5 --use-stdout \
    | tyger run exec -f snr_gadgetron.yml --logs -b noisecovariance=$noise_covariance_buffer_id  \
    | ismrmrd_stream_to_hdf5 --use-stdin -o main-scan-recon.h5
```


`snr_gadgetron.yml` looks like this (note the two input buffers):
```yaml
<!--@include: ./snr_gadgetron.yml-->
```

## Distributed reconstruction

Next is an example that uses a distributed run. First, download the raw data:

```bash
curl -OL https://aka.ms/tyger/docs/samples/binning.h5
```

Then run:

```bash
ismrmrd_hdf5_to_stream -i binning.h5 --use-stdout \
| tyger run exec -f distributed_gadgetron.yml --logs \
| ismrmrd_stream_to_hdf5 --use-stdin -o out_binning.h5
```

`distributed_gadgetron.yml` looks like this:

```yaml
<!--@include: ./distributed_gadgetron.yml-->
```

This run is made up of a job with two worker replicas.
