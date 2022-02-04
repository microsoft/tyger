using System.ComponentModel.DataAnnotations;

namespace Tyger.Server.StorageServer;

public static class StorageServer
{
    public static void AddStorageServer(this IServiceCollection services)
    {
        services.AddOptions<StorageServerOptions>().BindConfiguration("storageServer").ValidateDataAnnotations().ValidateOnStart();
        services.AddHttpClient();
        services.AddSingleton<StorageServerHealthCheck>();
        services.AddHealthChecks().AddCheck<StorageServerHealthCheck>("Storage server");
    }
}

public class StorageServerOptions
{
    [Required]
    public string Uri { get; init; } = "";
}
