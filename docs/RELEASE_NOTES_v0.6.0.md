# Quark v0.6.0 — Release Notes

> **Date:** 2026-05-14
> **Status:** late-alpha. Not yet v1.0 production-ready.
> See [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) for the honest gap analysis between the current state and a planned v1.0.

Phase 3 release. Schema-as-code migrations land — neutral schema introspection across the four CI dialects + SQLite, a pure-Go schema diff, a models→Plan pipeline, transactional and resumable plan execution, a `quarkmigrate` plan/verify/apply CLI workflow, orchestrated `Backfill` with resume tokens, a per-Client model registry, and a distributed migration lock. The cycle closes the F3-1 through F3-7 backlog from `TASKS.md`. No breaking changes; no migration guide. Also lands `Array[T]` — typed wrapper for list-shaped columns, closing the Bloque B / Arrays Postgres item from Phase 1 deferred work.

## What's in this release

### Schema-as-code migrations (Phase 3)

- **F3-1 — Distributed migration lock.** `Client.AcquireMigrationLock(ctx, name, timeout) (MigrationLock, error)` returns a cluster-wide advisory lock for migration operations. First caller wins; subsequent callers block up to `timeout` or receive `ErrLockTimeout`. The lock is held by a dedicated connection for its lifetime; `Release` returns it to the pool. New optional `MigrationLocker` interface on Dialect — kept optional so custom dialects don't break. Per-dialect: PG `pg_advisory_lock(hashtext)` + `SET lock_timeout` (SQLSTATE `55P03` → `ErrLockTimeout`); MySQL/MariaDB `GET_LOCK` + `RELEASE_LOCK` (return 0 → `ErrLockTimeout`); MSSQL `sp_getapplock @LockOwner='Session'` (status `-1` → `ErrLockTimeout`). SQLite and Oracle return `ErrUnsupportedFeature` — SQLite has no distributed primitive, Oracle's `DBMS_LOCK` needs PL/SQL plumbing tracked as a follow-up. ([#44](https://github.com/jcsvwinston/quark/pull/44))

- **F3-2 — Neutral schema introspection.** `Client.IntrospectSchema(ctx) (Schema, error)` returns the database's current schema as a dialect-neutral `Schema{Tables []Table{Name, Columns, Indexes, ForeignKeys, Checks}}`. Foundation for the F3-3 diff comparator. New optional `SchemaIntrospector` interface on Dialect (same opt-in pattern as `MigrationLocker`). Implementations:
  - **SQLite** — `sqlite_master` + `PRAGMA table_info`; `Indexes` populated; `ForeignKeys` via `PRAGMA foreign_key_list`. `Checks=nil` is intentional (SQLite has no catalog for CHECK; the only path is parsing `sqlite_master.sql`, brittle and out of scope).
  - **PostgreSQL** — `information_schema.{tables,columns}` with `current_schema()` scoping + type-parameter reassembly for `varchar(N)` / `numeric(P,S)`. Indexes from `pg_index` (`NOT indisprimary`); FKs from `pg_constraint` (`contype='f'`); checks from `pg_constraint` (`contype='c'`) with `pg_get_constraintdef(oid, true)` for the canonical expression text.
  - **MySQL / MariaDB** — `INFORMATION_SCHEMA.{TABLES,COLUMNS}` scoped to `DATABASE()`, using `COLUMN_TYPE` for the full parameterised type string. Indexes from `STATISTICS`, FKs from `KEY_COLUMN_USAGE` + `REFERENTIAL_CONSTRAINTS`, checks from `TABLE_CONSTRAINTS` + `CHECK_CONSTRAINTS` (MySQL 8.0.16+, MariaDB 10.2.1+; older versions return `Error 1146` which the dialect degrades to empty results, keeping introspection usable).
  - **MSSQL** — `sys.tables` / `sys.columns` / `sys.types` / `sys.default_constraints` with type reassembly from `max_length` / `precision` / `scale`; nvarchar/nchar byte-to-char halving; `MAX` for `max_length = -1`. Indexes from `sys.indexes` + `sys.index_columns` (`is_primary_key=0 AND type>0 AND is_included_column=0`); FKs from `sys.foreign_keys` + `sys.foreign_key_columns`; checks from `sys.check_constraints`.

  Oracle returns `ErrUnsupportedFeature` until the `gvenzl/oracle-free` CI image issue resolves. ([#45](https://github.com/jcsvwinston/quark/pull/45), [#47](https://github.com/jcsvwinston/quark/pull/47), [#48](https://github.com/jcsvwinston/quark/pull/48), [#49](https://github.com/jcsvwinston/quark/pull/49), [#50](https://github.com/jcsvwinston/quark/pull/50), [#51](https://github.com/jcsvwinston/quark/pull/51))

- **F3-3 — Pure-Go schema diff + plan pipeline + executor.**
  - `Diff(desired, current Schema) []Operation` (in `migrate_diff.go`) returns the ordered list of changes needed to bring `current` into alignment with `desired`. Operations are dialect-neutral sealed types (`OpCreateTable`, `OpDropTable`, `OpAddColumn`, `OpDropColumn`, `OpAlterColumn`, `OpCreateIndex`, `OpDropIndex`, `OpAddForeignKey`, `OpDropForeignKey`, `OpAddCheck`, `OpDropCheck`). The diff is **pure and deterministic** — same input produces the same output with a stable sort — and conservatively-typed: columns / indexes / checks are matched by name; FKs match by name or by composite `(columns, ref_table, ref_columns)` when the catalog returned an empty name (the SQLite inline-FK case). Cross-dialect awareness baked in: MariaDB `RESTRICT` ≡ MySQL `NO ACTION` is treated as semantically equivalent so no spurious DROP+ADD ops appear; SQLite's `Checks=nil` skips check comparison instead of treating `nil` as "no checks". ([#52](https://github.com/jcsvwinston/quark/pull/52))
  - `Client.PlanMigration(ctx, models...) (Plan, error)` (in `migrate_plan.go`) turns Go model structs into a `Plan{Ops []Operation}` describing what would need to change. Pipeline: models → desired Schema (reflect on the cached `ModelMeta` / `FieldMeta`, reusing the migrator's `SQLTypeWithOpts`) → `IntrospectSchema` for the current state → `Diff(desired, current)` → `Plan`. The Plan is **inert** — no side effects. `Plan.IsEmpty()` and `Plan.String()` make the result trivially consumable by health endpoints, CI checks, and the F3-5 CLI. The SQLite introspector also gained a PK-nullable fix: PRAGMA's `notnull` is 0 for `INTEGER PRIMARY KEY` columns even though they're implicitly NOT NULL, so the introspector now ORs in PRAGMA's `pk` field for symmetry with the other dialects' catalogs. ([#53](https://github.com/jcsvwinston/quark/pull/53))
  - `Client.ApplyPlan(ctx, plan) error` (in `migrate_execute.go`) walks the operations in a `Plan` in order and dispatches each to the appropriate per-dialect DDL. `OpAlterColumn` covers type changes today; nullable / default deltas are no-ops (TODO `F3-3-execute-alter`). SQLite + `DropForeignKey` / `DropCheck` return `ErrUnsupportedFeature` — SQLite has no `ALTER TABLE DROP CONSTRAINT`; the 12-step table-rebuild workaround is a follow-up (`F3-3-execute-sqlite-rebuild`). ([#54](https://github.com/jcsvwinston/quark/pull/54))
  - Cross-dialect type and default normalisation in the diff's `columnsEqual` closes the round-trip contract: case-folding + trimming, PG `character varying` → `varchar`, MySQL display-width strip (`int(11)` → `int`), `int` ≡ `integer` collapse, and PG `nextval(...)` ≡ nil for SERIAL/IDENTITY columns. Headline result: **`PlanMigration` round-trip is now empty on all five CI motors** after `Migrate(model)`. `PlanMigration_RoundTripScopedToFixture` runs in SharedSuite. ([#55](https://github.com/jcsvwinston/quark/pull/55))

- **F3-4 — Transactional + resumable `ApplyPlan`.**
  - On engines with transactional DDL — **PostgreSQL, MSSQL, SQLite** — `ApplyPlan` now wraps the op loop in `BEGIN ... COMMIT`. A mid-plan failure rolls back the whole plan. Internal refactor: `Client.CreateIndex` and `Client.AddForeignKey` now wrap private `createIndexOn` / `addForeignKeyOn` helpers that take an `Executor`, so the tx path routes its DDL through the underlying `*sql.Tx`. ([#56](https://github.com/jcsvwinston/quark/pull/56))
  - On engines where DDL implicitly commits — **MySQL, MariaDB, Oracle** — `ApplyPlan` now records each successfully applied op in `quark_migration_state(plan_hash, op_index, op_string, applied_at)`. A re-invocation against the same plan (same `Plan.Hash()`) skips ops already recorded; a plan modified between runs produces a fresh hash and starts from zero. New `Plan.Hash() string` (sha256 hex of concatenated `op.String()` outputs) exposes the plan identity. ([#57](https://github.com/jcsvwinston/quark/pull/57))

- **F3-5 — `quarkmigrate` plan/verify/apply CLI workflow.** New `quarkmigrate` package exposes `Run(ctx, action, client, models...) error` (and `RunWithOutput` for test-friendly writers) with three actions:
  - `plan` — print the plan, exit `ExitSuccess` (informational).
  - `verify` — print the plan, exit `ExitDriftDetected` (1) if non-empty (CI gate use).
  - `apply` — print the plan, run it if non-empty, exit `ExitSuccess` on success.

  Operational error (PlanMigration / ApplyPlan failure, unknown action) is `ExitError` (2) across all three actions. Exit codes exposed as constants. Plan output is prefixed with the short `Plan.Hash()` for correlation with `quark_migration_state` on non-tx engines. Library not binary: Go has no runtime model registration; users own a tiny `migrations/main.go` that imports both `quarkmigrate` and their models. Complete example in `examples/migrations/main.go`. ([#58](https://github.com/jcsvwinston/quark/pull/58))

- **F3-6 — Orchestrated `Backfill` with resume tokens.** `Client.Backfill(ctx, BackfillSpec) error` iterates a table by primary key in batches, invokes a user callback per batch with the PK list, and persists the highest PK seen in `quark_backfill_state(name, last_pk, updated_at)` keyed by spec name. A process kill / callback error / deliberate retry resumes at `WHERE pk > last_pk` rather than re-running the entire table. Idempotent on completion: a re-invocation with the same Name after all batches were processed finds nothing to do and returns nil. The callback receives PKs (not row contents) because backfill SQL is rarely "SELECT + transform"; it's "UPDATE ... WHERE id IN (...)" — passing PKs keeps the helper out of the way without a generics-or-reflect API expansion. Limitations: integer PKs only; positive PKs assumed for the `last_pk=0` fresh-start case. ([#59](https://github.com/jcsvwinston/quark/pull/59))

- **F3-7 — Per-Client model registry.** Closes Phase 3. Four new methods on `*Client`:
  - `RegisterModel(models ...any) error` appends models to the per-Client registry (validates each up front; refuses partial registration on failure; safe for concurrent use; does NOT dedupe — re-registering appends and is pinned by `TestClient_RegisterModel_DoesNotDeduplicate`).
  - `RegisteredModels() []any` returns a snapshot of registered models in registration order. Mutations to the returned slice don't affect the internal registry.
  - `MigrateRegistered(ctx) error` is a convenience for `Migrate(ctx, c.RegisteredModels()...)`.
  - `PlanMigrationRegistered(ctx) (Plan, error)` is a convenience for `PlanMigration(ctx, c.RegisteredModels()...)`.

  Intentionally additive — the global type-meta cache in `internal/schema` is unchanged because it's correct as global state (deterministic per `reflect.Type`). F3-7's per-Client registry is about "which models this Client manages", NOT the meta-computation cache. Multi-tenant deployments with multiple Clients (per ADR-0007) can now each track their own model set without cross-contamination. ([#60](https://github.com/jcsvwinston/quark/pull/60))

### Rich types

- **`Array[T]` generic** — typed wrapper for SQL columns holding a list of `T`. Round-trips through JSON regardless of dialect (same wire format as `JSON[T]`; the migrator maps to the per-dialect JSON column type). Helpers `Len()` / `Slice()` over the underlying `[]T`. Semantically clearer than `JSON[[]T]` for list-shaped columns and gives the project a single upgrade path if PG-native `INT[]` / `TEXT[]` support lands later. Intentionally **not** tied to `pgx`/`pgtype` — neutral-wrapper design per TASKS § Bloque B. Inherits the MSSQL JSON Scan skip until the NVARCHAR(MAX) encoding bug (F0-8 followup E) is resolved. Closes Bloque B / Arrays Postgres from Phase 1 deferred work. ([#42](https://github.com/jcsvwinston/quark/pull/42))

### Documentation

- Phase 3 formally opened with [ADR-0009](https://github.com/jcsvwinston/quark/blob/main/docs/adr/0009-migrations-introspection-diff-not-versioned-files.md) (code-first + diff bidireccional strategy). The seven F3-N items decomposed in `TASKS.md`. ([#43](https://github.com/jcsvwinston/quark/pull/43))
- `website/docs/guides/migrations.mdx` rewritten end-to-end to cover the new `IntrospectSchema` / `PlanMigration` / `ApplyPlan` / `Backfill` / migration lock surfaces; `website/docs/guides/modeling.mdx` extended with `Array[T]`.

## Known limitations

- Quark is **late-alpha** (~v0.6). Not v1.0 production-ready. Phase 4 (observability + cache stampede protection), Phase 5 (real RLS engine via `CREATE POLICY` + `SET LOCAL`), and Phase 6 (codegen) are not yet in scope.
- **Oracle has no CI coverage.** The dialect-specific paths are exercised by code review and by manual `go test -tags=integration -run TestSuiteOracle ./...` runs on workstations with Oracle. The `F3-1` / `F3-2` Oracle deferrals carry forward.
- **`OpAlterColumn` covers type changes only.** Nullable / Default deltas are no-ops in the executor today (TODO `F3-3-execute-alter`).
- **SQLite + `DropForeignKey` / `DropCheck`** return `ErrUnsupportedFeature` because SQLite has no `ALTER TABLE DROP CONSTRAINT`. The 12-step table-rebuild workaround is a follow-up (`F3-3-execute-sqlite-rebuild`).
- **`Backfill` is integer-PK only.** Text PKs and composite PKs are out of scope for `F3-6-core`.
- **MSSQL `JSON[T]` is broken** on roundtrip (pre-existing v0.5 limitation): `Array[T]` inherits the same skip until the NVARCHAR(MAX) encoding bug is resolved.
- The set-op surface on MySQL / MariaDB is limited to `UNION` / `UNION ALL`. `INTERSECT` / `EXCEPT` return `ErrUnsupportedFeature` (rewrite as a JOIN). Oracle uses `MINUS` for `EXCEPT`. SQLite rejects `INTERSECT ALL` / `EXCEPT ALL`.

## Upgrading

```bash
go get github.com/jcsvwinston/quark@v0.6.0
```

No source-code changes required — v0.6 is additive. Existing code that uses `Client.Migrate(ctx, &Model{})` keeps working unchanged; the new `IntrospectSchema` / `PlanMigration` / `ApplyPlan` / `Backfill` / `AcquireMigrationLock` / `RegisterModel` / `Array[T]` surfaces are opt-in.

If you want the new migration workflow:

1. Embed `quarkmigrate.Run` in a thin `migrations/main.go` that imports both `quarkmigrate` and your models package — see [`examples/migrations/main.go`](https://github.com/jcsvwinston/quark/blob/main/examples/migrations/main.go) for a complete wrapper.
2. Use `quark schema verify` (or `quarkmigrate.Run(ctx, "verify", ...)` directly) as a CI gate before deploying — non-zero exit when the live DB drifts from your models.
3. For production deploys, wrap `ApplyPlan` with `AcquireMigrationLock` so concurrent instances serialise. `Plan.Hash()` plus `quark_migration_state` give you a resume-from-checkpoint contract on non-transactional engines (MySQL / MariaDB / Oracle).

## Versioned docs

The page-versioned site for v0.6.0 is at `https://jcsvwinston.github.io/quark/docs/0.6.0/`.
