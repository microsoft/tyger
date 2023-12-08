using Azure.Core;
using Azure.Identity;

namespace Tyger.Server.Identity;

public static class Identity
{
    public static void AddManagedIdentity(this IServiceCollection services)
    {
        services.AddSingleton<TokenCredential, WorkloadIdentityCredential>();
    }
}
