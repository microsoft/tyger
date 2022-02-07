using System.ComponentModel.DataAnnotations;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.Options;

namespace Tyger.Server.Database;

public static class Database
{
    public static void AddDatabase(this IServiceCollection services)
    {
        services.AddOptions<DatabaseOptions>().BindConfiguration("database").ValidateDataAnnotations().ValidateOnStart();
        services.AddScoped<IRepository, Repository>();
        services.AddDbContext<TygerDbContext>((sp, options) =>
            {
                var databaseOptions = sp.GetRequiredService<IOptions<DatabaseOptions>>().Value;
                var connectionString = databaseOptions.ConnectionString;
                if (!string.IsNullOrEmpty(databaseOptions.Password))
                {
                    connectionString = $"{connectionString}; Password={databaseOptions.Password}";
                }

                options.UseNpgsql(connectionString)
                    .UseSnakeCaseNamingConvention();
            },
            contextLifetime: ServiceLifetime.Scoped, optionsLifetime: ServiceLifetime.Singleton);
    }

    public static async Task EnsureCreated(IServiceProvider serviceProvider)
    {
        using var scope = serviceProvider.CreateScope();
        using var context = scope.ServiceProvider.GetRequiredService<TygerDbContext>();
        await context.Database.EnsureCreatedAsync();
    }
}

public class DatabaseOptions
{
    [Required]
    public string ConnectionString { get; set; } = null!;

    public string? Password { get; set; }
}
