# Working with buffers

Buffers are central to Tyger and are the mechanism for transmitting data and
storing signal data. Behind the scenes, they are just an Azure Blob storage
container with data stored in a series of blobs.

Buffers are Write once read many (WORM) and cannot be be overwritten (unless you
bypass `tyger` and manipulate the underlying storage container directly). The
hash of each blob is verified when reading and writing, and an overall
cumulative hash of hashes is computed and verified with each blob to ensure data
integrity.

## Creating a buffer

To create a buffer, run:

```bash
tyger buffer create
```

After a moment, it will output the ID of the new buffer. You will use this id
when reading or writing to the buffer, when creating a run using it, etc.

## Writing to a buffer

To write to a buffer, you will use `tyger buffer write ID`. The simplest way to
use the command is to pipe data from another process into its standard input
stream. You can use `tyger buffer gen SIZE` to generate arbitrary data for testing:

```bash
tyger buffer gen 10G | tyger buffer write $buffer
```

The `$buffer` argument will normally be the buffer ID. It can also be an [access
URL](#buffer-access-urls) or a path to a file containing an access URL.

If the latency is high between your machine and the Azure region hosting the
storage account, you can specify a higher degree of parallelism. By default, up
to 16 blobs are uploaded concurrently, but you can control this with the `--dop`
parameter.

You can also specify the size of each blob with the `-b|--block-size` parameter,
e.g. `--block-size 16M`. The default block size is 4MB. This means no data is
sent until 4MB has been piped to `tyger buffer write`.

Finally, instead of using standard in, you can specify the file or named pipe to
read from with the `-i|--input` parameter.

## Reading from buffers

Reading from buffers is similar to writing:

```bash
tyger buffer read $buffer > destination_file
```

`tyger buffer read` will not exit until it has read the entire buffer, the buffer
is marked as failed, or it encounters another unrecoverable error.

As with the `write` command, the `$buffer` argument will normally be the buffer
ID. It can also be an [access URL](#buffer-access-urls) or a path to a file
containing an access URL.

It also has a `--dop` parameter for controlling parallelism and has an
`-o|--output` parameter to write to a file instead of standard out.

## Buffer access URLs

To obtain an access URL that can be passed in to `tyger buffer read` or `tyger buffer write`, you can run:

```bash
tyger buffer access $buffer_id [-w|--write]
```

Access URLs are valid for one hour and are read-only unless `-w|--write` is specified.

You can then pass this URL to a `tyger` CLI that does not need to be logged in.

## Tagging buffers

Buffers can be tagged with simple metadata key-value pairs. You can tag a buffer when creating it:

```bash
tyger buffer create --tag mykey1=myvalue1 --tag mykey2=myvalue2
```

You can then retrieve the tags on a buffer using

```bash
$tyger buffer show $buffer_id
```

Which will give you a JSON response similar to the following:

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

You can replace the tags set on a buffer with

```bash
tyger buffer set $buffer_id --tag newkey=newvalue
```

Note that this **replaces** all existing tags with give new given set of tags.

```json
{
  "id": "362z6u2h7voevic7zt3kkhpwxm",
  "etag": "638418039170119977",
  "createdAt": "2024-01-25T18:19:55.423949Z",
  "tags": {
    "newkey": "newvalue"
  }
}
```

You can optionally pass in an `--etag` parameter with the value of the `etag`
field of the last `show` response. The `set` command will fail if the ETag in
the database does not match the given value. This helps guard against possible
changes made by another user to the same buffer after you run `show` but before
you run `set`.

## Listing buffers

You can list all buffers with the command:

```bash
tyger buffer list
```

This lists tag metadata and orders the result in descending creation time.

You can limit the number of results with the `--limit` parameter.

You can also limit results to buffers that have tag values set:

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

All tag arguments must match (they are ANDed together):

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
