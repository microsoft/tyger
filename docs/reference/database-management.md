# Database management

Tyger uses a PostgreSQL to store information about codespecs, runs, and buffer metadata. Occasionally, we will need to evolve the database schema (add new tables, columns, etc.).

These changes are published as "migrations", which named sequences of SQL
statements. When installing a new version of the tyger API, you will get a
warning if there are migrations to apply. The new code will still run on the old
database schema, but you should upgrade the database as sooner rather than
later.

Database migrations are designed to be run without needing to take Tyger offline. However, we strongly recommend taking a backup of the database before apply migrations, running the migrations at a time outside of peak hours, and testing in a non-production first.

To see what migrations are available, run:

```bash
tyger api migration list [--all]
```

This will show you the current database version and the migrations that are
available to be applied. If you specify `--all`, all migrations are displayed,
even previously applied ones.

To apply one or more migrations, you run:

```bash
tyger api migration apply
    --target-version ID | --latest
    --wait
```

If multiple migrations need to be applied, each one is applied sequentially.

Migrations might take a while to run, so by default `apply` starts the
migrations and does not wait for them to finish. If you specify `--wait`, the
command waits until all migrations have been applied or one of them fails.

To get the logs of a particular migration, run:

```bash
tyger api migration log ID
```

Migrations are designed to be idempotent, and retrying an migration should not
cause any harm.
