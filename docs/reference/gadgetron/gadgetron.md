# Gadgeton examples

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
| tyger run exec -f  basic_gadgetron.yml \
| ismrmrd_stream_to_hdf5 --use-stdin -o out_basic.h5
```

`basic_gadgetron.yml` looks like this:

```yaml
<!--@include: ./basic_gadgetron.yml-->
```

## Distributed reconstruction

Next is an example that uses a distributed run. First, download the raw data:

```bash
curl -OL https://aka.ms/tyger/docs/samples/binning.h5
```

Then run:

```bash
ismrmrd_hdf5_to_stream -i binning.h5 --use-stdout \
| tyger run exec -f  distributed_gadgetron.yml --logs \
| ismrmrd_stream_to_hdf5 --use-stdin -o out_binning.h5
```

`distributed_gadgetron.yml` looks like this:

```yaml
<!--@include: ./distributed_gadgetron.yml-->
```

This run is made up of a job with two worker replicas.
