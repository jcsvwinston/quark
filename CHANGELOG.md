# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **CTE support via `With` / `WithRecursive` (Phase 2)**: any
  `*Subquery` can be attached to an outer query as a named CTE. The
  outer SELECT is prefixed with `WITH "name" AS (<inner>)` (or
  `WITH RECURSIVE ...` if any attached entry is recursive), the inner
  args are substituted and prepended to the args slice, and the outer
  WHERE / HAVING argIndex shifts accordingly so dialect placeholders
  ($N / @pN / :N) line up across the CTE-prefix → outer-WHERE
  boundary. The outer query references the CTE by name in JOIN
  clauses (the existing JoinOn grammar already accepts the
  `cte_name.col = parent.col` shape).

  ```go
  topOrders, _ := quark.For[Order](ctx, c).
      Where("amount", ">", 100).
      Select("user_id").
      AsSubquery()

  users, _ := quark.For[User](ctx, c).
      With("top_orders", topOrders).
      Join("top_orders", "users.id = top_orders.user_id").
      List()
  // WITH "top_orders" AS (SELECT "user_id" FROM "orders" WHERE "amount" > $1)
  // SELECT * FROM "users" INNER JOIN "top_orders" ON ...
  ```

  CTE names go through `SQLGuard.ValidateIdentifier`. Recursive CTEs
  emit the dialect-portable `WITH RECURSIVE` keyword; the recursive
  body itself currently requires the user to express the
  `UNION ALL`-shape — full UNION / INTERSECT / EXCEPT support arrives
  in F2-set.

- **Subqueries via `AsSubquery` + `Sub` / `Exists` / `NotExists` /
  `InSub` / `NotInSub` (Phase 2)**: any `Query[T]` can be captured as a
  `*Subquery` and embedded in the AST. The capture eagerly renders the
  inner SELECT (identifier validation, soft-delete predicate, JOINs,
  GROUP BY, HAVING, ORDER BY, LIMIT, lock suffix) using the active
  dialect's identifier quoting but with `?` as the bind marker, so the
  outer query's `buildWhereClause` swaps each `?` for the dialect's
  placeholder syntax at the correct argIndex when the wrapping Expr is
  rendered. Supports the canonical shapes:
  ```go
  // WHERE "id" IN (SELECT "user_id" FROM "orders" WHERE "amount" > ?)
  q.WhereExpr(quark.InSub(quark.Col("id"), sub))
  // WHERE "id" = (SELECT MAX("user_id") FROM "orders")
  q.WhereExpr(quark.Eq(quark.Col("id"), quark.Sub(sub)))
  // WHERE EXISTS (SELECT 1 FROM "orders" WHERE ...)
  q.WhereExpr(quark.Exists(sub))
  ```
  Internally the renderer wraps the active dialect in a `qmarkDialect`
  that delegates everything except `Placeholder`, which always returns
  `?`. So Quote, LimitOffset, and JSONExtract stay dialect-correct.
  Errors during `AsSubquery` (invalid identifier in the inner SELECT,
  or any pessimistic-lock option set on the inner query) propagate to
  the caller; `MustAsSubquery` is the panic-on-error variant for use
  inside expression composition. Pessimistic locks on the inner query
  are rejected with `ErrUnsupportedFeature` because MSSQL emits
  `WITH (UPDLOCK)` inline in the FROM clause — illegal inside an
  `IN (SELECT ...)` context — and the safe pattern is to acquire locks
  on the outer query.

- **Composable expression AST + `WhereExpr` / `HavingExpr` (Phase 2)**: a
  typed expression tree (`Expr` interface, `Col`, `Lit`, `And`, `Or`,
  `Not`, `Cmp`, `Eq`/`Ne`/`Lt`/`Gt`/`Lte`/`Gte`, `In`, `NotIn`, `Func`)
  rendered into the existing where-clause pipeline through `WhereExpr`
  and `HavingExpr`. Identifiers go through `SQLGuard.ValidateIdentifier`
  at every leaf, operators through `SQLGuard.ValidateOperator`, and
  function names against a conservative 10-name whitelist (`COUNT`,
  `SUM`, `AVG`, `MIN`, `MAX`, `LOWER`, `UPPER`, `LENGTH`, `COALESCE`,
  `ABS`). The AST emits `?` as a neutral bind marker; the existing
  `substitutePathMarkers` helper swaps each `?` for the dialect's
  placeholder syntax at render time, so the same AST renders correctly
  against PostgreSQL `$N`, MSSQL `@pN`, Oracle `:N`, MySQL/SQLite `?`
  without per-dialect indexing arithmetic in user code. Closes the gap
  where deep `(A OR (B AND C))` predicates required `RawQuery`.

- **Nested Preload via dotted paths (Phase 2)**: `Preload("Orders.Items.Product")`
  now walks the dotted path and loads each level in a single eager-loading
  pass. Multiple paths sharing a prefix are merged via `parsePreloads` so
  `Preload("Posts", "Posts.Comments")` only loads `Posts` once. Internally
  the per-relation loaders moved from `Query[T]` to `BaseQuery` and now
  accept the parent slice as a `reflect.Value`, so the recursive descent
  doesn't need a generic instantiation per level.

- **`HavingAggregate(fn, column, op, value)` (Phase 2)**: structured way to
  write `HAVING COUNT(*) > 5` / `HAVING SUM(amount) >= 100` / `HAVING
  AVG(price) < ?` etc. without falling back to `RawQuery`. Closes the
  historical limitation where the existing `Having(column, op, value)`
  validated `column` through `SQLGuard.ValidateIdentifier` and therefore
  rejected anything containing parentheses (i.e. every aggregate). The
  function name is whitelisted (`COUNT`, `SUM`, `AVG`, `MIN`, `MAX`,
  case-insensitive); the column is validated through the guard, except
  for `*` which is only allowed with `COUNT`. The fully composable form
  `Having(Func("count", Col("*")), ">", 5)` arrives with the rest of the
  Phase 2 AST.

- **Pessimistic locking (Phase 2)**: `Query[T].ForUpdate()`, `ForShare()`,
  `SkipLocked()`, `NoWait()` modifiers. The dialect emits the right shape:
  `FOR UPDATE [SKIP LOCKED|NOWAIT]` / `FOR SHARE` for PG, MySQL, MariaDB,
  and Oracle (Oracle has no `FOR SHARE` and returns `ErrUnsupportedFeature`
  for it); MSSQL emits table hints (`WITH (UPDLOCK, ROWLOCK [, READPAST])`)
  in the FROM clause; SQLite returns `ErrUnsupportedFeature` for any
  non-zero lock option (use `BEGIN IMMEDIATE` in the transaction instead).
  New error sentinel `ErrUnsupportedFeature` for these dialect-feature
  gates.

- **`Dialect.LockSuffix(LockOptions) (tableHint, suffix string, err error)`**:
  new interface method consumed by `buildSelect` to attach pessimistic-lock
  fragments to the SELECT in the right placement per dialect. Custom
  dialects must implement it.

### Fixed

- **Eager-loading paths now chunk parent keys (Phase 2)**: `Preload` over a
  large parent set used to assemble a single `IN(...)` clause with one
  bind per parent — silently broken on Oracle (1000-IN cap) and at risk on
  SQL Server (~2100 bind ceiling). The three relation loaders
  (`loadStandardRelation`, `loadM2MRelation`, `loadPolymorphicRelation`)
  now chunk at 1000 keys per query and aggregate results across chunks
  via a new internal `chunkParentKeys` helper. Tenant predicates and
  polymorphic-type discriminators are re-applied per chunk so the
  invariant survives the iteration.

## [0.3.0] - 2026-05-10

First proper tag since `v0.1.1`. Bundles Phase 0 P0 fixes (security, correctness)
with the Phase 1 deliverables (rich types, dirty tracking, optimistic locking,
soft-delete scopes). Full release notes in
[`docs/RELEASE_NOTES_v0.3.0.md`](docs/RELEASE_NOTES_v0.3.0.md). Migration
steps for breaking changes in
[`docs/MIGRATION_v0.3.0.md`](docs/MIGRATION_v0.3.0.md).

### Added

- **`JSON[T any]` generic + `[]byte` BLOB mapping (Phase 1 F1-2)**:
  `quark.JSON[T]` is a typed wrapper that round-trips a Go value through a
  SQL JSON column via `encoding/json`. It implements
  [`sql.Scanner`](https://pkg.go.dev/database/sql#Scanner) and
  [`driver.Valuer`](https://pkg.go.dev/database/sql/driver#Valuer) directly,
  so the round-trip uses the standard library's plumbing — no extra reflect
  in Quark's hot paths. The migrate layer detects `JSON[T]` and emits the
  dialect-native column type:
  Postgres `JSONB`; MySQL/MariaDB `JSON`; SQLite `TEXT` (with `json_*`
  functions still available); SQL Server `NVARCHAR(MAX)`; Oracle `CLOB`.
  Pair with `Nullable[JSON[T]]` when you need to distinguish SQL NULL from
  an empty payload. The migrate layer also learned to map `[]byte` to the
  dialect-native binary column (`BYTEA` on Postgres, `VARBINARY(MAX)` on
  SQL Server, `BLOB` elsewhere) instead of the previous `TEXT` fallback.

- **`Nullable[T]` generic (Phase 1 F1-3)**: re-export of `database/sql.Null[T]`
  under a Quark-friendly name, plus the constructors `SomeOf(v)` /
  `NullOf[T]()`. Replaces the long-standing `*time.Time` / `sql.NullString`
  pointer-as-nullable hacks with `Nullable[time.Time]` / `Nullable[string]`
  while keeping the same Scanner+Valuer round-trip plumbing the standard
  library already provides. The migrate layer detects `Nullable[T]` and
  emits T's SQL type for the column, so a model that previously needed a
  custom mapper now Just Works (`Nullable[int64]` → BIGINT,
  `Nullable[time.Time]` → TIMESTAMP / DATETIME / DATETIME2 per dialect).

- **Soft-delete scopes `WithTrashed()` / `OnlyTrashed()` and `Restore` (Phase 1 F1-5)**:
  the existing automatic `deleted_at IS NULL` filter now has two named
  escape hatches: `WithTrashed()` returns both live and trashed rows
  (alias of `Unscoped`, kept for backward compatibility), and
  `OnlyTrashed()` flips the predicate to `deleted_at IS NOT NULL` so
  callers can list only the trash. Both modifiers propagate through
  `clone()`. New `Query[T].Restore(entity)` method clears `deleted_at`
  on the row identified by the entity's PK; the SQL includes
  `AND deleted_at IS NOT NULL` so a Restore on a live row is a 0-row
  no-op (no stealth NULL write). Tenant predicate from the loading
  query is preserved on `Restore`. The default scope, `Count`, and
  aggregates all consult a new centralised `softDeletePredicate`
  helper so the three call sites stay in lock-step.

- **Optimistic locking via `quark:"version"` (Phase 1 F1-6)**: tagging a numeric
  field with `quark:"version"` enables row-level optimistic-locking on
  `Update` / `UpdateFields` / `Tracked.Save`. Each successful update emits
  `SET ..., version = version + 1 WHERE pk = ? AND version = <loaded>` and
  bumps the entity's in-memory version. When the predicate doesn't match
  (another writer already advanced the column) the call returns the new
  sentinel `ErrStaleEntity` without writing. Pairs naturally with the
  Phase-1 dirty-tracking pipeline: a `Tracked.Save` after a no-op mutation
  is still a no-op (the version is not bumped on its own).

- **`ErrStaleEntity`** sentinel for optimistic-locking conflicts (F1-6).

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
