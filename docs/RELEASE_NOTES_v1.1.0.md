# Release notes — v1.1.0

**Hardening release.** v1.1.0 is the output of the post-v1.0 bug-bash: a
systematic cross-engine pass (phases F0–F14) over the whole surface, plus the
correctness fixes it surfaced. No new public API of note — the value is
fewer dialect-specific sharp edges and a tested-under-load core.

Docs (1.1.0 is the current version): <https://jcsvwinston.github.io/quark/docs/>
(older versions under `/docs/1.0.0/` etc.)

## Fixed (found by the bug-bash)

- **migrate — versioned migrations on SQL Server (BB-12):** `Migrator.Init`
  emitted `CREATE TABLE IF NOT EXISTS … TIMESTAMP …`, which SQL Server rejects
  (no `IF NOT EXISTS`; `TIMESTAMP` is a rowversion). The bookkeeping-table DDL
  is now per-dialect. Versioned migrations now work on SQL Server.
- **migrate — false-positive schema diff on MariaDB (BB-11):** MariaDB reports a
  nullable, no-default column's default as the literal string `"NULL"`, so
  `PlanMigration` emitted a phantom column-alter. The MySQL/MariaDB introspector
  now normalizes it (scoped to MariaDB; a genuine `DEFAULT 'NULL'` on MySQL is
  untouched).
- **crud — `CreateBatch` chunking (BB-10):** a large batch overran the dialect's
  bind-parameter ceiling (SQL Server ~2100; SQLite/PG/MySQL higher) and failed.
  It now chunks per dialect; Oracle keeps its single-row path.
- **tx — dialect-aware savepoints (BB-9):** nested `tx.Tx` failed on SQL Server
  and Oracle because savepoint SQL was ANSI-only. Now resolved per dialect.
- **multi-tenant — SchemaPerTenant write routing (BB-8):** writes could land in
  the default schema instead of the tenant's. Fixed.
- **preload — nullable-FK / Oracle m2m / SQL Server null `[]byte` (BB-5/6/7):**
  three eager-loading correctness fixes across engines.
- **guard — reject the `--` line-comment tail in raw queries (BB-13/F13):**
  closes a classic injection-truncation vector under `AllowRawQueries`.

## Added

- **dialects — automatic MariaDB detection** distinct from MySQL.

## Tests

- **Post-v1.0 bug-bash F0–F14 complete:** install/boot, smoke, API-surface,
  relations, volume, multi-tenancy, migrations, cache, hooks/events/audit,
  codegen, sharding, replicas, resilience/concurrency, security, and a soak
  phase — exercised cross-engine (SQLite + PostgreSQL + MySQL + MariaDB + SQL
  Server; Oracle via Docker, plus the 6-engine RC soak). All bug-bash findings (BB-1…BB-13) are closed.

## Documentation

- SQL Server `UNIQUEIDENTIFIER`/`uuid.UUID` round-trip footgun documented
  (steer to `VARCHAR(36)`/`NVARCHAR(36)`).
- Batch-operations, migrations, and codegen guides updated to match the fixes.

## Known limitations

- **Oracle runs in the blocking CI matrix via a plain `docker run` container**,
  not testcontainers (whose lifecycle crashes the `gvenzl/oracle-free` image on
  hosted runners — `ci.yml` boots it directly and hands the suite a DSN). Two
  Oracle operational quirks remain, both environment-level rather than ORM
  defects: the free-tier image needs `GRANT EXECUTE ON DBMS_LOCK` for the
  migration lock (ADR-0018) and has a low connection-pool ceiling (`ORA-12516`).
- Versioned-migration registry is still process-global (per-`Client` registry
  is deferred); `Sync` still does name-only column diff (use `PlanMigration`
  for structural diff).
