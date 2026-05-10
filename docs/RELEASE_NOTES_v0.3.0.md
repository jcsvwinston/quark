# Quark v0.3.0 — Release Notes

> **Date:** 2026-05-10
> **Status:** late-alpha. Not yet v1.0 production-ready.
> See [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) for the honest gap analysis between current state and planned v1.0.

This is the first proper tag since `v0.1.1`. It bundles the **Phase 0 P0 fixes** (security and correctness) with the **Phase 1 deliverables** (rich types, dirty tracking, optimistic locking, soft-delete scopes). Together they unblock the move to Phase 2 (composable query builder + locking).

## What's in this release

### Security (Phase 0 P0 fixes)

- **P0-1 — Tenant isolation in `Or()`**: an OR group used to be built on a fresh `BaseQuery` that did not carry `tenantID`/`tenantCol`. Combined with SQL operator precedence (`A AND B OR C` parses as `(A AND B) OR C`), the OR branch escaped the outer `tenant_id = ?` predicate and could return rows from other tenants. Closed with `BaseQuery.cloneForGroup()` that propagates isolation state into the OR sub-clause and pre-injects the tenant predicate. Public API unchanged.

- **P0-2 — `WhereJSON` SQL injection via path interpolation**: every dialect built the JSON SQL with `fmt.Sprintf("'%s'", path)`. Closed in two layers: `internal/guard.ValidateJSONPath` enforces a strict identifier-chain regex; every dialect now binds the path as a parameter (Postgres uses `jsonb_extract_path_text(col, VARIADIC text)`, the rest use `JSON_EXTRACT`/`JSON_VALUE(col, ?)` with `$.path`). New sentinel `ErrInvalidJSONPath`. **Breaking**: `Dialect.JSONExtract` signature changed — see [`MIGRATION_v0.3.0.md`](MIGRATION_v0.3.0.md).

- **P0-3 — `linkM2M` swallowed every driver error**: the join-table INSERT used to return `nil` for ANY error, masking foreign-key violations and missing tables as success. Closed with a driver-aware `isUniqueViolation` helper that only neutralises actual unique-key violations.

- **P0-5 — `JOIN ... ON` concatenated raw**: `Join`/`LeftJoin`/`RightJoin` accepted the `on` argument as opaque string and emitted it verbatim. Closed with `internal/guard.ValidateJoinOn` (identifier-only grammar, deprecation notice on the string-raw API). New sentinel `ErrInvalidJoin`.

### Added (Phase 1 deliverables)

- **F1-1 — Dirty tracking**: `Query[T].Track().Find/First/List` returns `*Tracked[T]` with a column-value snapshot. `Tracked.Save(ctx)` emits an UPDATE that touches only changed columns, including zero values. The permanent fix for the P0-4 zero-value trap.

- **F1-2 — Rich types** (core):
  - `quark.JSON[T any]` typed JSON wrapper with `Scanner`/`Valuer` round-trip. Migrate emits dialect-native JSON column (`JSONB` / `JSON` / `TEXT` / `NVARCHAR(MAX)` / `CLOB`).
  - `[]byte` columns map to `BYTEA` / `VARBINARY(MAX)` / `BLOB` per dialect (was `TEXT` fallback).
  - **Deferred to Phase 2**: Postgres native arrays, per-column timezone overrides.

- **F1-3 — `Nullable[T]`**: thin alias of `database/sql.Null[T]` with `SomeOf(v)` / `NullOf[T]()` constructors. Migrate auto-detects `Nullable[T]` and emits T's column type — no custom mapper needed.

- **F1-4 — `RegisterTypeMapper`**: extensible Go-type → SQL-type registry consulted by `Migrate`/`Sync`. db tag also accepts sizing options (`size=N`, `precision=N`, `scale=N`). `time.Duration` ships pre-registered to `BIGINT` / `NUMBER(19)`.

- **F1-5 — Soft-delete scopes**: `WithTrashed()` / `OnlyTrashed()` / `Restore(entity)` round out the existing automatic `deleted_at IS NULL` filter. `Unscoped()` kept as alias of `WithTrashed`. Restore is safe-by-construction (`AND deleted_at IS NOT NULL` guard) so a Restore on a live row is a 0-row no-op.

- **F1-6 — Optimistic locking**: tag a numeric field with `quark:"version"` and `Update`/`UpdateFields`/`Tracked.Save` automatically include `version = version + 1` in SET and `AND version = <loaded>` in WHERE. Conflict returns the new sentinel `ErrStaleEntity` without writing.

- **`UpdateFields(entity, fields...)` (Phase 0 P0-4 escape hatch)**: explicit partial-update method that bypasses the zero-value filter. Kept as a complementary API alongside `Tracked.Save` — fast direct-by-name updates.

### Infrastructure & docs

- Public docs site moved to `https://jcsvwinston.github.io/quark/` (was `…/quark-docs/`). The Docusaurus source lives in this repo under `website/` and the deploy workflow ships it on every push to `main` that touches `website/**`.
- New error sentinels: `ErrInvalidJSONPath`, `ErrInvalidJoin`, `ErrStaleEntity`. All documented in `website/docs/reference/api/errors.mdx`.

## Breaking changes

`Dialect.JSONExtract` signature changed from `(column, path string) string` to `(column, path string) (sql string, args []any, err error)`. Custom dialects registered via `RegisterDialect` must update — full migration steps in [`MIGRATION_v0.3.0.md`](MIGRATION_v0.3.0.md).

`Join` / `LeftJoin` / `RightJoin` are marked `// Deprecated:` — a structured `Join(table).On(col, op, otherCol)` API arrives in v0.4 with the Phase 2 AST. The string-raw form continues to work in v0.3 with the new identifier-only grammar.

## Known limitations

- Tests run end-to-end on SQLite by default. The Postgres / MySQL / MariaDB / MSSQL / Oracle suites are wired into `SharedSuite` but require manual DSN env vars until F0-8 (testcontainers) lands.
- Postgres native arrays, per-column timezone overrides, and pre-registered mappers for `shopspring/decimal` / `google/uuid` are out of scope for v0.3 — see [`TASKS.md`](../TASKS.md) for the deferred items.
- The query builder is still reflect-everywhere; codegen (Phase 6) is the planned exit. CTEs, window functions, `UNION`/`INTERSECT`, and pessimistic locking still require `RawQuery` and arrive with the Phase 2 AST.

## Upgrade path

```bash
go get github.com/jcsvwinston/quark@v0.3.0
```

If you implemented a custom `Dialect`, follow [`MIGRATION_v0.3.0.md`](MIGRATION_v0.3.0.md) for the `JSONExtract` signature change. Otherwise no source changes are required.

Public docs: [`https://jcsvwinston.github.io/quark/docs/0.3.0/`](https://jcsvwinston.github.io/quark/docs/0.3.0/) (after the deploy workflow finishes for this tag).
