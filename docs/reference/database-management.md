# Database management

Tyger uses a PostgreSQL database to store information about codespecs, runs, and
buffer metadata. As the functionality of Tyger evolves, the database schema may
occasionally require updates, such as adding tables, columns, and indexes.

These updates are published as "migrations," which are essentially numbered
database scripts. When installing a new version of the Tyger API, you will
receive a warning if there are available migrations to apply. While the new code
version will continue operate with the old database schema, it is advisable to
perform database upgrades sooner rather than later.

Database migrations are designed to run without Tyger downtime.
However, it is strongly recommended to:

- Backup the database before applying migrations.
- Schedule migrations during off-peak hours.
- Test migrations in a non-production environment first.

## Viewing available migrations

To check available migrations, execute:

```bash
tyger api migration list [--all]
```

This command shows the current database version and lists pending migrations.
Use `--all` to view all migrations, including those that have already been
already applied.

## Applying migrations

To apply migrations, use:

```bash
tyger api migration apply
    --target-version ID | --latest
    --wait
```

If multiple migrations need to be applied, they will be done sequentially. By
default, `apply` initiates the migrations and exits without waiting for
completion. Use `--wait` to make the command wait for all migrations to complete.

## Viewing migration logs

To get the logs from the application of a migration, run:

```bash
tyger api migration log ID
```

Migrations are designed to be idempotent. Retrying a migration should not cause
any issues.
