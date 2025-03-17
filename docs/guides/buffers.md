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

### Specifying the location of a buffer

In a cloud installation, you can use multiple storage accounts for buffer
storage. If those accounts are in different cloud locations (regions), you can
specify the location that that you would like the buffer to be crated in with
the `--location` parameter. The location is the lowercase name of the cloud
location with spaces removed, such as `eastus` or `westus2`. If multiple storage
accounts are in the same location, accounts are selected in a round-robin
fashion.

The available regions for a tyger installation can fetched with:

```bash
tyger buffer storage-account list
```


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
  "location": "eastus",
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
  "location": "eastus",
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
  "location": "eastus",
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
    "location": "eastus",
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
    "location": "eastus",
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

## Deleting buffers

You can delete individual buffers using

```bash
tyger buffer delete $buffer_id
```

```json
{
  "id": "4xr6p5pdmy7ujanqhh7xkz2f3a",
  "createdAt": "2025-03-14T06:43:18.95849Z",
  "eTag": "10072690995705686547",
  "expiresAt": "2025-03-18T02:55:10.114186Z"
}
```

This will "soft" delete the buffer.
Soft-deleted buffers are hidden from all Tyger commands. To show, list, or modify soft-deleted buffers, use the `--soft-deleted` command-line argument:

```bash
tyger buffer show $buffer_id --soft-deleted
```

```json
{
  "id": "4xr6p5pdmy7ujanqhh7xkz2f3a",
  "createdAt": "2025-03-14T06:43:18.95849Z",
  "eTag": "10072690995705686547",
  "expiresAt": "2025-03-18T02:55:10.114186Z"
}
```

Soft-deleted buffers will be automatically purged when they expire.
The default time-to-live for a soft-deleted buffer is configured during Tyger installation via the `buffers.softDeletedLifetime` field.

By default, active buffers do not have an expiration date and will never be automatically deleted.
This behavior is configured during Tyger installation via the `buffers.activeLifetime` field.
If an active buffer expires, it will be automatically soft-deleted by Tyger.

To set the TTL for a buffer manually, use the format `DD.HH:MM:SS`.

```bash
tyger buffer set --ttl 2.12:00 $buffer_id --soft-deleted
```

```json
{
  "createdAt": "2025-03-14T06:43:18.95849+00:00",
  "eTag": "11844248791289668196",
  "expiresAt": "2025-03-20T03:12:53.584841+00:00",
  "id": "4xr6p5pdmy7ujanqhh7xkz2f3a",
  "location": "local",
  "tags": {}
}
```


To restore a soft-deleted buffer, use

```bash
tyger buffer restore $buffer_id
```

```json
{
  "id": "4xr6p5pdmy7ujanqhh7xkz2f3a",
  "createdAt": "2025-03-14T06:43:18.95849Z",
  "eTag": "3726351716079049356"
}
```

To permanently delete (purge) a soft-deleted buffer, use:

```bash
tyger buffer purge $buffer_id
```

```json
{
  "id": "4xr6p5pdmy7ujanqhh7xkz2f3a",
  "createdAt": "2025-03-14T06:43:18.95849Z",
  "eTag": "9598312053235431473",
  "expiresAt": "2025-03-17T15:18:23.305393Z"
}
```

You can also delete, restore, or purge multiple buffers matching a set of tags

```bash
tyger buffer delete --tag mykey1=myvalue1 --exclude-tag mykey2=myvalue2 --force

Deleted 54 buffers.
```

or **all** buffers using

```bash
tyger buffer delete --all

2025-03-17T15:24:12.012Z WRN Deleting 372 buffers. Use --force to delete without confirmation.
Are you sure you want to delete 372 buffers? ▸Yes  No
Deleted 372 buffers.
```


## Copying buffers between Tyger instances
::: warning Note
This functionality is only supported when Tyger is running in the cloud.
:::

Suppose you have two Tyger instances, and you want to copy all buffers including
their tags from one instance to another. This can be accomplished in two steps.

### Export the buffers

```bash
tyger buffer export DESTINATION_STORAGE_ENDPOINT [--source-storage-account SOURCE_ACCOUNT_NAME] [--tag KEY=VALUE ...]
```

`DESTINATION_STORAGE_ENDPOINT` should be the blob endpoint of the destination
Tyger instance's storage account. The Tyger server's managed identity needs to have
`Storage Blob Data Contributor` access on this storage account.


If multiple storage accounts are configured for the source Tyger installation
`--source-storage-account` must be provided. The export command will only export
from a single storage account. The available storage accounts can be listed with
the command `tyger buffer storage-account list`.

To only export a subset of buffer, you can filter the buffers to be exported by
tags.

This command starts a special [run](./runs). Logs are displayed inline, but can also be
retrieved later using [`tyger run logs ID`](./runs#viewing-logs).

### Import the buffers
Once the export run has completed successfully, you can import these buffers
into the destination Tyger instance's database with the command:

```bash
tyger buffer import [--storage-account STORAGE_ACCOUNT_NAME]
```

This starts a run that scans though the instance's storage account and imports
new buffers. Note that existing buffers are not touched and their tags will not
be updated.

If multiple storage accounts are configured for the Tyger installation
`--storage-account` must be provided. The import command will only import from a
from a single storage account. The available storage accounts can be listed with
the command `tyger buffer storage-account list`.
