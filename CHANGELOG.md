# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`RegisterTypeMapper(reflect.Type, TypeMapper)` (Phase 1 F1-4)**: extensible
  Go-type → SQL-type mapping for `client.Migrate` and `client.Sync`. Custom
  types (decimal.Decimal, uuid.UUID, IP addresses, vector types, …) can plug
  their own DDL emission without forking Quark. Pointer types are stripped
  before registration so registering for `time.Duration` also covers
  `*time.Duration`. The migrate layer also accepts new sizing options on the
  db tag — `db:"name,size=512"`, `db:"price,precision=18,scale=4"` — that
  flow into `TypeOptions` and are propagated to mappers and to the built-in
  VARCHAR/DECIMAL emitters. As the canonical example, Quark now ships with
  `time.Duration` registered to `BIGINT` (or `NUMBER(19)` on Oracle) so
  `Duration` columns stop falling back to `TEXT`.

- **Dirty tracking ligero (Phase 1)**: new `Query[T].Track()` modifier returns
  a `*TrackedQuery[T]` whose `Find` / `First` / `List` yield `*Tracked[T]`
  wrappers carrying a column-value snapshot taken at load time. Calling
  `Tracked.Save(ctx)` emits an UPDATE that touches only the columns whose
  values actually differ from the snapshot — and writes them whether they
  are zero or non-zero. This is the permanent fix for the P0-4 zero-value
  trap: `tracked.Entity.Active = false; tracked.Save(ctx)` writes `false`
  to the database without the caller resorting to `UpdateFields` or
  `UpdateMap`. `Tracked.Changed()` exposes the changed column list for
  tests and observability. The snapshot lives on the wrapper, not in the
  Client, so there is no shared map to grow or evict; tenant predicates
  from the loading query are propagated to Save's WHERE clause; the
  primary-key column and the configured tenant column are never written
  even if the caller mutates them on the entity.

### Security

- **`JOIN ... ON` clause concatenated raw (P0-5)**: `Join`/`LeftJoin`/
  `RightJoin` accepted the `on` argument as an opaque string and emitted it
  verbatim into the SELECT/Count SQL with no validation — an inconsistency
  with `WHERE` (which already validated identifiers) and an injection vector
  if the `on` came from dynamic input. Fixed: `internal/guard.ValidateJoinOn`
  enforces the minimal grammar `[ident.]ident OP [ident.]ident
  ((AND|OR) [ident.]ident OP [ident.]ident)*` (operators
  `=`, `!=`, `<>`, `<`, `<=`, `>`, `>=`; max 512 chars). Both call sites
  (`buildSelect` and `Count`) now reject malformed clauses with the new
  sentinel `ErrInvalidJoin`. The string-raw signature is marked deprecated
  in godoc; the structured `Join(table).On(col, op, otherCol)` builder is
  scheduled for v0.4 (Phase 2 AST). Regression: `testJoinOnSecurity` wired
  into the shared suite — valid identifier joins, valid AND-joined clauses,
  8 injection vectors rejected, and a Count-path check.

### Added

- **`ErrInvalidJoin`** sentinel for malformed `Join`/`LeftJoin`/`RightJoin`
  ON clauses (P0-5).

- **`UpdateFields(entity, fields ...string)` API (P0-4 escape hatch)**:
  explicit partial-update method that writes only the named fields, bypassing
  the zero-value filter `Update(entity)` applies. Recommended path for
  writing `false`, `0`, `""`, or `nil` until dirty tracking lands in Phase 1.
  Refuses to overwrite the primary key, errors on unknown field names or an
  empty list. Hooks `BeforeUpdate`/`AfterUpdate` still run.

### Changed

- **`Update(entity)` logs a WARN when it skips zero-value fields**, listing
  the skipped column names. Lets users see the P0-4 trap instead of having
  values silently disappear. The behaviour itself does not change.

### Fixed

- **Silent error swallowing in `linkM2M` (P0-3)**: when Quark inserted into a
  many-to-many join table, every driver error was returned as `nil` under the
  comment `// Ignore duplicate key errors - already linked`. The intent was
  to keep re-linking idempotent for unique-key violations, but the
  implementation masked foreign-key violations, missing tables, broken
  connections, and any other failure as success. Fixed: only real unique-key
  violations (PG SQLSTATE 23505, MySQL 1062, MSSQL 2627/2601, Oracle ORA-00001,
  SQLite extended codes 2067/1555 — both mattn and modernc drivers) are now
  treated as idempotent; everything else is wrapped with `wrapDBError` and
  propagated. Added `testM2MLinkErrors` to the shared suite (idempotent
  re-link + missing-join-table propagation). No public API change.

### Security

- **`WhereJSON` SQL injection via path interpolation (P0-2)**: every dialect's
  `JSONExtract` was building the SQL with `fmt.Sprintf("'%s'", path)` (or the
  Postgres `->>'%s'` equivalent), so a path containing a single quote either
  broke the SQL or could be weaponised when the path came from user input.
  Fixed in two layers: (1) the path is now bound as a parameter in every
  dialect — Postgres uses `jsonb_extract_path_text(col, VARIADIC text)` with
  one bind per segment, the rest use `JSON_EXTRACT`/`JSON_VALUE(col, ?)` with
  the `$.path` form; (2) `internal/guard.ValidateJSONPath` enforces the
  grammar `^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$` (max 256
  chars) and is called from each dialect before the bind. Invalid paths now
  return `ErrInvalidJSONPath` (new sentinel) at execution time.
  **Breaking**: `Dialect.JSONExtract` signature changed from
  `(column, path string) string` to
  `(column, path string) (sql string, args []any, err error)`. Custom
  dialects registered via `RegisterDialect` must update.
  Regression test `testJSONPathSecurity` wired into the shared suite covers
  valid paths (asserts the path is in bind args, never in the SQL surface)
  and 8 injection vectors (quotes, semicolons, comments, leading `$`, dashes,
  whitespace, empty).

- **Tenant isolation leak in `Or()` under `RowLevelSecurity` (P0-1)**: an `Or(...)`
  group used to be built on a fresh `BaseQuery` that did not carry the active
  `tenantID` / `tenantCol`. Combined with SQL operator precedence
  (`A AND B OR C` parses as `(A AND B) OR C`), the OR branch escaped the outer
  `tenant_id = ?` predicate and could return rows from other tenants. Fixed by
  introducing an internal `BaseQuery.cloneForGroup()` helper that propagates
  isolation/context state to the callback's blank query and pre-injects the
  tenant predicate into the OR group, so the rendered SQL becomes
  `WHERE tenant_id=? AND ... OR (tenant_id=? AND ...)`. Added a regression
  test (`testOrRLSLeak`) wired into the shared multi-engine suite that fails
  before the fix and passes after, including a nested-`Or` variant.
  No public API change.

### Changed

- **`Dialect.JSONExtract` signature** is now
  `(column, path string) (sql string, args []any, err error)` (was
  `(column, path string) string`). Required to bind the path as a parameter
  for P0-2. Custom dialects registered via `RegisterDialect` must update.

## [0.1.1] - 2026-05-06

### Breaking Changes

- **Client Creation API**: Changed `quark.New()` signature from `New(db *sql.DB, opts ...Option)` to `New(driverName, dataSource string, opts ...any)`
  - The function now accepts a driver name and data source string instead of a `*sql.DB` instance
  - `sql.Open()` is now called internally by `New()`
  - Dialect is now auto-detected from the driver name, removing the need for explicit `WithDialect()` option
  - Connection pool options (`WithMaxOpenConns`, `WithMaxIdleConns`, `WithConnMaxLifetime`, `WithConnMaxIdleTime`) are now applied during client creation

- **Removed Options**: 
  - `WithDialect()` option is no longer needed as dialect is auto-detected from driver name
  - Passing `*sql.DB` directly to `New()` is no longer supported

### Added

- **New Client Method**: Added `WithOptions(opts ...any) (*Client, error)` method to `Client` for recreating clients with different options without exposing the underlying `*sql.DB`
- **Connection Pool Options**: Added pool configuration options:
  - `WithMaxOpenConns(maxOpenConns int)` - Sets maximum number of open connections
  - `WithMaxIdleConns(maxIdleConns int)` - Sets maximum number of idle connections
  - `WithConnMaxLifetime(d time.Duration)` - Sets maximum connection lifetime
  - `WithConnMaxIdleTime(d time.Duration)` - Sets maximum idle connection time

### Migration Guide

**Old API:**
```go
db, err := sql.Open("sqlite", ":memory:")
if err != nil {
    log.Fatal(err)
}
defer db.Close()

client, err := quark.New(db, quark.WithDialect(quark.SQLite()))
```

**New API:**
```go
client, err := quark.New("sqlite", ":memory:")
if err != nil {
    log.Fatal(err)
}
defer client.Close()
```

**Recreating client with different options:**

**Old API:**
```go
newClient, err := quark.New(client.Raw(), quark.WithLimits(newLimits))
```

**New API:**
```go
newClient, err := client.WithOptions(quark.WithLimits(newLimits))
```

### Supported Drivers for Auto-Detection

- `"sqlite"`, `"sqlite3"`, `"modernc"` → SQLite
- `"postgres"`, `"pgx"`, `"pgx/v5"`, `"pq"` → PostgreSQL
- `"mysql"` → MySQL
- `"mariadb"` → MariaDB
- `"mssql"`, `"sqlserver"`, `"azuresql"` → MSSQL
- `"oracle"`, `"godror"`, `"oci8"` → Oracle

## [0.1.0] - Previous Release

Initial release
