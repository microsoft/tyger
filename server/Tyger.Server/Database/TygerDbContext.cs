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
    public DbSet<RunEntity> Runs => Set<RunEntity>();

    protected override void OnModelCreating(ModelBuilder modelBuilder)
    {
        modelBuilder.Entity<CodespecEntity>(c =>
            {
                c.Property(c => c.Name).IsRequired().UseCollation("C");
                c.Property(c => c.Version).IsRequired();
                c.Property(c => c.CreatedAt).IsRequired().HasDefaultValueSql("(now() AT TIME ZONE 'utc')");
                c.Property(c => c.Spec).IsRequired().HasColumnType("jsonb");
                c.HasNoKey();
            });

        modelBuilder.Entity<RunEntity>(r =>
            {
                r.Property(c => c.Id).ValueGeneratedOnAdd();
                r.Property(c => c.CreatedAt).IsRequired();
                r.Property(c => c.Run).IsRequired().HasColumnType("jsonb");
                r.Property(c => c.Final).HasDefaultValue(false);
                r.Property(c => c.ResourcesCreated).HasDefaultValue(false);
                r.Property(c => c.LogsArchivedAt).HasDefaultValue(null);

                r.HasKey(c => new { c.Id });
                r.HasIndex(c => new { c.CreatedAt, c.Id });
                r.HasIndex(c => new { c.CreatedAt }).HasFilter("resources_created = false");
            });
    }
}

public class CodespecEntity
{
    public string Name { get; set; } = null!;
    public int Version { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    public NewCodespec Spec { get; set; } = null!;
}

public class RunEntity
{
    public long Id { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    public Run Run { get; set; } = null!;
    public bool Final { get; set; }
    public bool ResourcesCreated { get; set; }
    public DateTimeOffset? LogsArchivedAt { get; set; }
}
