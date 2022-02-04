using Microsoft.EntityFrameworkCore;
using Tyger.Server.Model;

namespace Tyger.Server.Database;

public class TygerDbContext : DbContext
{

    public TygerDbContext(DbContextOptions<TygerDbContext> dbContextOptions)
            : base(dbContextOptions)
    {
    }

    public DbSet<CodespecEntity> Codespecs => Set<CodespecEntity>();

    protected override void OnModelCreating(ModelBuilder modelBuilder)
    {
        modelBuilder.Entity<CodespecEntity>(c =>
            {
                c.Property(c => c.Name).IsRequired();
                c.Property(c => c.Version).IsRequired();
                c.Property(c => c.CreatedAt).IsRequired().HasDefaultValueSql("now()");
                c.Property(c => c.Spec).IsRequired().HasColumnType("jsonb");

                c.HasKey(c => new { c.Name, c.Version });
            });
    }
}


public class CodespecEntity
{
    public string Name { get; set; } = null!;
    public int Version { get; set; }
    public DateTime CreatedAt { get; set; }
    public Codespec Spec { get; set; } = null!;
}
