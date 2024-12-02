# Working with buffers

Buffers are central to Tyger, serving as the mechanism for
transmitting and storing signal data. They are Azure Blob storage
containers, with data stored in a series of blobs.

Buffers follow a Write Once Read Many (WORM) principle and cannot be
overwritten (unless you bypass `tyger` and directly manipulate the underlying
storage container). The integrity of each blob is ensured through hash
verification during reading and writing, with a cumulative hash of hashes
checked for each blob to ensure the integrity of the sequence of blobs.

## Creating a buffer

To create a buffer, run:

```bash
tyger buffer create
```

This command will output the new buffer's ID, which is used for operations like
buffer reading, buffer writing, and creating runs.

## Writing to a buffer

To write to a buffer, you will use `tyger buffer write ID`. The simplest way to
use the command is to pipe data from another process into it. For testing
purposes, you can use `tyger buffer gen SIZE` to generate arbitrary data:

```bash
tyger buffer gen 10G | tyger buffer write $buffer
```

Here, `$buffer` is typically the buffer ID, but it can also be an [access
URL](#buffer-access-urls) or a file path containing an access URL.

In cases of high latency to the Azure region hosting the storage account,
increase parallelism with the `--dop` parameter (by default, up to 16 blobs are
uploaded concurrently).

The `--flush-interval` parameter specifies the maximum time interval that data
 is accumulated before being sent to the remote service. This is to avoid
 buffering for an excessive amount of time when the incoming data rate is low.

You can specify the block size with `--block-size`, for example,
`--block-size 16M`. The default block size is 4MB.

Instead of standard in, you can use `-i|--input` to read from a file or named
pipe.

## Reading from buffers

Reading from buffers is similar to writing:

```bash
tyger buffer read $buffer > destination_file
```

`tyger buffer read` continues until it has read the entire buffer, the buffer is
marked as failed, or an unrecoverable error occurs.

As with the `write` command, the `$buffer` argument is typically the buffer ID.
It can also be an [access URL](#buffer-access-urls) or a file path containing an
access URL.

`read` also supports the `--dop` parameter for parallelism control and
`-o|--output` for writing to a file instead of standard output.

## Buffer access URLs

To get an access URL for `tyger buffer read` or `tyger buffer write`, run:

```bash
tyger buffer access $buffer_id [-w|--write]
```

Access URLs are valid for one hour and are read-only by default, unless
`-w|--write` is specified.

These URLs can be used with a `tyger` CLI that isn't logged in.

## Tagging buffers

Buffers can be tagged with key-value metadata pairs. You can assign tags to a
buffer when creating it like this:

```bash
buffer_id=$(tyger buffer create --tag mykey1=myvalue1 --tag mykey2=myvalue2)
```

You can view the tags on a buffer with:

```bash
tyger buffer show $buffer_id
```

The response looks like:

```json
{
  "id": "yf4sx2aqzitepjhmxjhanomn5e",
  "etag": "638418036499348393",
  "createdAt": "2024-01-25T18:20:49.951262Z",
  "tags": {
    "mykey1": "myvalue1",
    "mykey2": "myvalue2"
  }
}
```

Update a buffer's tags with:

```bash
tyger buffer set $buffer_id --tag myKey1=myvalue1Updated --tag mykey3=myvalue3
```

```json
{
  "id": "yf4sx2aqzitepjhmxjhanomn5e",
  "etag": "638418039170119977",
  "createdAt": "2024-01-25T18:20:49.951262Z",
  "tags": {
    "myKey1": "myvalue1Updated",
    "mykey2": "myvalue2",
    "mykey3": "myvalue3"
  }
}
```

To replace the **entire** set of tags, specify `--clear-tags`:

```bash
tyger buffer set $buffer_id --clear-tags --tag myKey1=yetAnotherValue
```

```json
{
  "id": "yf4sx2aqzitepjhmxjhanomn5e",
  "etag": "638418039170119988",
  "createdAt": "2024-01-25T18:20:49.951262Z",
  "tags": {
    "myKey1": "yetAnotherValue",
  }
}
```

Use the `--etag` parameter to ensure the `set` command only succeeds if there
have been no changes to the buffer since the last `show` command.

## Listing buffers

You can list buffers with:

```bash
tyger buffer list [--tag KEY=VALUE] [--limit N]
```

Results are ordered by descending creation time and are limited by the `--limit`
value. If no limit is set, a maximum of 1000 buffers are returned with a warning
if more exist.

To filter the results by tags:

```bash
tyger buffer list --tag mykey1=myvalue1
```

```json
[
  {
    "id": "yf4sx2aqzitepjhmxjhanomn5e",
    "etag": "638418036499348393",
    "createdAt": "2024-01-25T18:20:49.951262Z",
    "tags": {
      "mykey1": "myvalue1",
      "mykey2": "myvalue2"
    }
  }
]
```

All tag arguments must match:

```bash
tyger buffer list --tag mykey1=myvalue1 --tag mykey2=myvalue2
```

```json
[
  {
    "id": "yf4sx2aqzitepjhmxjhanomn5e",
    "etag": "638418036499348393",
    "createdAt": "2024-01-25T18:20:49.951262Z",
    "tags": {
      "mykey1": "myvalue1",
      "mykey2": "myvalue2"
    }
  }
]
```

```bash
tyger buffer list --tag mykey1=myvalue1 --tag missingkey=missingvalue
```

```json
[]
```

## Copying buffers between Tyger instances
::: warning Note
This functionality is only supported when Tyger is running in the cloud.
:::

Suppose you have two Tyger instances, and you want to copy all buffers including
their tags from one instance to another. This can be accomplished in two steps.

### Export the buffers

```bash
tyger buffer export DESTINATION_STORAGE_ENDPOINT [--tag KEY=VALUE ...]
```

`DESTINATION_STORAGE_ENDPOINT` should be the blob endpoint of the destination
Tyger instance's storage account. The Tyger server's managed identity needs to have
`Storage Blob Data Contributor` access on this storage account.

To only export a subset of buffer, you can filter the buffers to be exported by
tags.

This command starts a special [run](./runs). Logs are displayed inline, but can also be
retrieved later using [`tyger run logs ID`](./runs#viewing-logs).

### Import the buffers
Once the export run has completed successfully, you can import these buffers
into the destination Tyger instance's database with the command:

```bash
tyger buffer import
```

This starts a run that scans though the instance's storage account and imports
new buffers. Note that existing buffers are not touched and their tags will not
be updated.
