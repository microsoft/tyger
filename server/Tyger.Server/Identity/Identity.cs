// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Azure.Core;
using Azure.Identity;

namespace Tyger.Server.Identity;

public static class Identity
{
    public static void AddManagedIdentity(this IHostApplicationBuilder builder)
    {
        // AzureCliCredential is for when we are running on a local dev machine
        builder.Services.AddSingleton<TokenCredential>(new ChainedTokenCredential(new WorkloadIdentityCredential(), new AzureCliCredential()));
    }
}
