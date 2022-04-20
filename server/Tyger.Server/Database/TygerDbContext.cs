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
                c.Property(c => c.Name).IsRequired();
                c.Property(c => c.Version).IsRequired();
                c.Property(c => c.CreatedAt).IsRequired().HasDefaultValueSql("(now() AT TIME ZONE 'utc')");
                c.Property(c => c.Spec).IsRequired().HasColumnType("jsonb");

                c.HasKey(c => new { c.Name, c.Version });
            });

        modelBuilder.Entity<RunEntity>(r =>
            {
                r.Property(c => c.Id).ValueGeneratedOnAdd();
                r.Property(c => c.CreatedAt).IsRequired();
                r.Property(c => c.Deadline).IsRequired();
                r.Property(c => c.Run).IsRequired().HasColumnType("jsonb");
                r.Property(c => c.Final).HasDefaultValue(false);
                r.Property(c => c.PodCreated).HasDefaultValue(false);
                r.Property(c => c.LogsArchivedAt).HasDefaultValue(null);

                r.HasKey(c => new { c.Id });
                r.HasIndex(c => new { c.CreatedAt, c.Id });
                r.HasIndex(c => new { c.CreatedAt }).HasFilter("pod_created = false");
                r.HasIndex(c => new { c.Deadline }).HasFilter("final = false");
            });
    }
}

public class CodespecEntity
{
    public string Name { get; set; } = null!;
    public int Version { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    public Codespec Spec { get; set; } = null!;
}

public class RunEntity
{
    public long Id { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    public DateTimeOffset Deadline { get; set; }
    public Run Run { get; set; } = null!;
    public bool Final { get; set; }
    public bool PodCreated { get; set; }
    public DateTimeOffset? LogsArchivedAt { get; set; }
}
