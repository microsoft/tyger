# Tyger: Signal Processing Control Plane

## Proposed Control-Plane Features

### Use AAD to secure Kubernetes Access.

This is an upcoming security [requirement](https://microsoft.sharepoint.com/teams/AzureTenantBaseline2/SitePages/AAD-should-be-enabled-in-Kubernetes-Service.aspx).

### Use AAD Workload Identity

See https://learn.microsoft.com/en-us/azure/aks/workload-identity-overview. This feature allows pods to use a managed identity, which we would use to access storage accounts and managed Postgres databases instead of storing connections strings as Kubernetes secrets.

### Support Long Runs

The SAS tokens that we generate only last one hour. We need to support renewing these on the recon side, during `tyger buffer read|write`, and within the data-plane proxy.

### Use Azure Database for PostgreSQL

Use Azure-managed databases instead installing Postgres locally as a Helm chart in our cluster.

### Support Database Schema Migrations

Today, a schema change requires deleting and recreating the database, which is obviously not a shippable option.

### Design and Implement an Access Control Model

It should be possible to lock down what a principal can do. For instance, the identity on a Siemens Host system:

- Should only be able run a certain set of codespecs
- Cannot create codespecs
- Can only query the status of runs it has created, and only from the last day.
- Should only be able to get data-plane access to buffers that are associated with runs it has access to.

There will need to be admin capabilities to define these roles and assign principals to them.

### Support Improved Searching

Support metadata tags for codespecs, runs, and buffers. Allow searching/listing these types by tag value and other metadata like status, created time, and finished time.

Examples:
- `tyger run list --status "Running || Pending"`
- `tyger run list --status "!Succeeded"`
- `tyger run list --tag mytag`

### Support Running On-Prem

Support installing Tyger on an on-prem Kubernetes cluster. We will need an alternative to Azure Storage for both buffers and log archival.

## Proposed Data-Plane Features

### Ensure Blobs Are Not Overwritten

Provide the `If-None-Match:*` header on upload to ensure that we are always creating a new blob.

### Metadata File

Write a `.metadata` file to the container with the following JSON format:

```json
{
    // Set to true once the buffer has been written
    // to completion.
    "isComplete": true,

    // Excludes the final empty blob.
    // Until "isComplete" is true, this number
    // is a lower bound on the number of blobs
    "blobCount": 2000,

    // If this field is set, then all blobs
    // must be of this size, with the following
    // exceptions:
    // the penultimate blob, which may be smaller,
    // and the last blob, which is always empty.
    // If this field is set, then we can easily seek
    // to any offset in the buffer
    "blobSize": 8388608,

    // The total size of the buffer.
    // Until "isComplete" is true, it is a lower
    // bound.
    "totalSize": 16777220096,

    // If true, there was a error writing
    // to this buffer and it is probably not
    // properly terminated.
    "failed": false
}
```

When writing to a buffer, the tool would periodically update the metadata file, and then write the final version when the upload completes. On the reader side, if the buffer is not complete, the `Last-Modified` response header can be used to know whether this buffer is still being actively updated. If not updated after a certain time, we can give up reading instead of hanging indefinitely.

When reading a buffer that has `isComplete` set to true, prefetching can be optimized to stop at the last blob, and the absence of an expected blob implies that that the buffer is somehow corrupted, not that the blob has not been written yet.

### AAD Credentials

In addition to SAS URIs, we could also support AAD authentication. We could use [`DefaultAzureCredential`](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk/azidentity#readme-defaultazurecredential) for flexible options (environment variables, Managed Identity, Azure CLI).

### Benchmark Feature

Similar to [`azcopy bench`](https://learn.microsoft.com/en-us/azure/storage/common/storage-ref-azcopy-bench), this feature would try to find the values for `--block-size` and `--dop` for reads and writes that maximize throughput to a storage account.

### Saved Configuration

Save values for `--block-size` and `--dop` in a file in `$XDG_CONFIG_HOME`. These values, if present, would become the default.

We could also support different values per hostname:

```json=
{
    "myeastusaccount.blob.core.windows.net": {
        "read": {
            "dop": 32
        },
        "write": {
            "dop": 64",
            "blockSize": "16MiB"
        }
    },
    "mywestusaccount.blob.core.windows.net": {
        "dop": 512
        "blockSize": "4MiB"
    },
    "*": {
        "dop": 32
        "blockSize": "16MiB"
    }
}
```

### Improve Pipe I/O Performance

Our bandwidth is currently limited by our ability to read and write to pipes. [This article](https://mazzo.li/posts/fast-pipes.html) has an excellent summary of the problems and how to overcome them on Linux. In short, we would gain a lot by using the splice/vmsplice system calls read/write to pipes.

## Contributing

This project welcomes contributions and suggestions.  Most contributions require you to agree to a
Contributor License Agreement (CLA) declaring that you have the right to, and actually do, grant us
the rights to use your contribution. For details, visit https://cla.opensource.microsoft.com.

When you submit a pull request, a CLA bot will automatically determine whether you need to provide
a CLA and decorate the PR appropriately (e.g., status check, comment). Simply follow the instructions
provided by the bot. You will only need to do this once across all repos using our CLA.

This project has adopted the [Microsoft Open Source Code of Conduct](https://opensource.microsoft.com/codeofconduct/).
For more information see the [Code of Conduct FAQ](https://opensource.microsoft.com/codeofconduct/faq/) or
contact [opencode@microsoft.com](mailto:opencode@microsoft.com) with any additional questions or comments.

## Trademarks

This project may contain trademarks or logos for projects, products, or services. Authorized use of Microsoft
trademarks or logos is subject to and must follow
[Microsoft's Trademark & Brand Guidelines](https://www.microsoft.com/en-us/legal/intellectualproperty/trademarks/usage/general).
Use of Microsoft trademarks or logos in modified versions of this project must not cause confusion or imply Microsoft sponsorship.
Any use of third-party trademarks or logos are subject to those third-party's policies.
