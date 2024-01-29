# Tyger

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

## Start using Tyger

See the documentation at https://microsoft.github.io/tyger

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
