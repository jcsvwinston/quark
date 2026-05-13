# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`Array[T]` generic** — typed wrapper for SQL columns holding a list of `T`.
  Round-trips through JSON regardless of dialect (same wire format as
  `JSON[T]`; migrate maps to the per-dialect JSON column type). Helpers
  `Len()` / `Slice()` over the underlying `[]T`. Semantically clearer than
  `JSON[[]T]` for list-shaped columns and gives the project a single
  upgrade path if PG-native `INT[]` / `TEXT[]` support lands later.
  Intentionally **not** tied to `pgx`/`pgtype` — neutral-wrapper design
  per TASKS § Bloque B. Inherits the MSSQL JSON Scan skip until the
  NVARCHAR(MAX) encoding bug (F0-8 followup E) is resolved.

## [0.5.0] - 2026-05-13

Phase 0 cleanup release. No new public API — every change is
infrastructure or test-side. Closes the F0-1 through F0-10 backlog
that had been carried since the project's first audit, including the
integration matrix that finally enforces the "tests pass on 6 engines
before merge" rule that was honor-system through v0.4. Full release
notes in [`docs/RELEASE_NOTES_v0.5.0.md`](docs/RELEASE_NOTES_v0.5.0.md).

### Added

- **Integration test matrix via testcontainers-go (F0-8)** — per-engine
  helpers in `containers_test.go` (gated `//go:build integration`) boot
  PostgreSQL, MySQL, MariaDB, MSSQL, and Oracle through testcontainers
  and resolve a DSN with the precedence env var → container. Each
  suite file delegates DSN resolution to `resolve<Engine>DSN(t)`
  instead of reading `os.Getenv` directly. Default
  (`go test -short`) path stays SQLite-only and doesn't import
  testcontainers-go. CI gains an `integration` job with a 4-engine
  matrix (PG / MySQL / MariaDB / MSSQL — Oracle excluded pending the
  image issue; the helper stays for local use) that runs in parallel
  to Lint + SQLite jobs. Docker is pre-installed on `ubuntu-latest`.
  Closes the honor-system state of the "6 motores verdes antes de
  mergear" hard rule — now enforced on 4/5 engines via CI. ([#28],
  [#36])
- **release-please workflow (F0-9)** —
  `.github/workflows/release-please.yml` runs on every push to `main`
  and keeps a rolling Release PR open with the next semver bump
  derived from Conventional Commits and the CHANGELOG entries since
  the last tag. Does NOT automate the Docusaurus `docs:version`
  snapshot — that stays manual via the `/release` slash command
  before merging the release PR. Config in
  `release-please-config.json` + manifest in
  `.release-please-manifest.json`. ([#38])
- **Docs linter (F0-10)** — `scripts/lint-docs.sh` runs in the
  `Lint` CI job. Three checks: anti-marketing language
  (`production-ready` / `enterprise-grade` / `battle-tested`
  rejected unless negated), `RELEASE_NOTES_V1` leak (the deleted
  file may not be referenced), and broken relative links in `*.md` /
  `*.mdx` (Docusaurus-aware: tries `<path>`, `<path>.md`,
  `<path>.mdx`, `<path>/index.{md,mdx}`, and resolves `/docs/...`
  baseUrl-rooted paths). Meta files (CLAUDE.md, TASKS, ADRs, blog,
  versioned_docs) exempt. ([#39])

### Fixed

- **MSSQL setop ORDER BY** — `List()` over a `Union` / `Intersect` /
  `Except` triggered MSSQL's "ORDER BY items must appear in the
  select list" because the auto-injected ORDER BY for OFFSET/FETCH
  referenced the PK column, which isn't in the operand SELECT. The
  fix is test-side: an explicit `OrderBy("email", "ASC")` on the
  base. The Quark SQL was always correct; the assertion was
  SQLite-biased. ([#35])
- **MSSQL JoinBuilder ambiguous id** — `List()` over a `Join`
  between two tables that both expose `id` triggered MSSQL's
  "Ambiguous column name 'id'" on the implicit `SELECT *`. Tests
  switched to `Count()`, which exercises the same ON-clause path
  without projection ambiguity. ([#30], [#35])
- **`having_aggregate` portable shape** — `SELECT * ... GROUP BY`
  is rejected by Postgres / MySQL strict / MSSQL when non-grouped
  columns aren't aggregated. Tests now use explicit
  `Select("status")` to match the GROUP BY clause. ([#30])
- **Float precision in nullable roundtrip** — Postgres maps Go
  `float64` to SQL `real` (32-bit) by default, so the 98.6 fixture
  round-trips to 98.5999984741211. Test switched to a
  `math.Abs(diff) > 1e-4` tolerance. ([#32])
- **Outdated `quark.New(db, ...)` examples on the docs site** —
  the verbose form never existed in the public API. All snippets
  migrated to the real `quark.New(driver, dsn, opts...)` signature
  across `website/docs/`. ([#27])

### Changed

- **CI matrix is now blocking on PG / MySQL / MariaDB / MSSQL** —
  `continue-on-error: true` removed after the F0-8 follow-ups
  closed the 11 test-side bugs the first cross-engine run
  surfaced. A red light on any of those 4 engines now fails the
  PR. Oracle remains excluded until the `gvenzl/oracle-free` image
  issue on hosted runners is resolved. ([#36])

### Documentation

- README cosmetic cleanup (F0-1 through F0-5): outdated
  `examples/blog-api/` references removed; `pkg/quark/examples/`
  heritage paths in `examples/README.md` fixed; duplicate Quick
  Start section deduplicated; coverage badge no longer hardcoded;
  versioned `RELEASE_NOTES_V1.md` no longer referenced. ([#37])
- TASKS header reconciled with the actual state of Phase 0
  (F0-1..F0-10 fully closed, not just the P0 subset). ([#40])

### Tests

- Dialect-aware quote assertions in `expr_ast` / `cte` / `window`
  integration tests via new `q(client, ident)` helper — replaces
  hardcoded `"col"` literals that match SQLite/Postgres quoting
  but not MySQL / MariaDB / MSSQL. ([#29])
- Dialect-skip + mirror-contract assertions in setop tests for
  MySQL / MariaDB where `Intersect` / `Except` return
  `ErrUnsupportedFeature` by design. ([#31])
- Interim skip of `JSON[T]` roundtrip on MSSQL with diagnosis —
  NVARCHAR(MAX) encoding bug; the fix (migrate to
  `VARCHAR(MAX)`) is deferred to a future PR with MSSQL local
  access. ([#33])

[#27]: https://github.com/jcsvwinston/quark/pull/27
[#28]: https://github.com/jcsvwinston/quark/pull/28
[#29]: https://github.com/jcsvwinston/quark/pull/29
[#30]: https://github.com/jcsvwinston/quark/pull/30
[#31]: https://github.com/jcsvwinston/quark/pull/31
[#32]: https://github.com/jcsvwinston/quark/pull/32
[#33]: https://github.com/jcsvwinston/quark/pull/33
[#35]: https://github.com/jcsvwinston/quark/pull/35
[#36]: https://github.com/jcsvwinston/quark/pull/36
[#37]: https://github.com/jcsvwinston/quark/pull/37
[#38]: https://github.com/jcsvwinston/quark/pull/38
[#39]: https://github.com/jcsvwinston/quark/pull/39
[#40]: https://github.com/jcsvwinston/quark/pull/40

## [0.4.0] - 2026-05-10

Phase 2 release: composable query builder. Introduces a typed expression
AST and the structured query primitives (subqueries, CTEs, window
functions, set operators) that build on it, plus a structured Join
builder that retires the v0.3.x string-raw deprecation. Full release
notes in [`docs/RELEASE_NOTES_v0.4.0.md`](docs/RELEASE_NOTES_v0.4.0.md);
breaking-change migration in
[`docs/MIGRATION_v0.4.0.md`](docs/MIGRATION_v0.4.0.md).

### Changed (BREAKING)

- **`Join` / `LeftJoin` / `RightJoin` now return a `*JoinBuilder[T]`**:
  the v0.3.x string-raw form `q.Join(table, onClause)` is replaced by
  the structured `q.Join(table).On(left, op, right)` (or
  `.OnRaw(onClause)` for compound ON clauses that need the legacy
  free-form). Both new methods route through the same
  `guard.ValidateJoinOn` grammar the old form used, so the validation
  surface is identical — only the call shape changed. See
  [`docs/MIGRATION_v0.4.0.md`](docs/MIGRATION_v0.4.0.md) for the
  mechanical rewrite (a `gofmt -r` rule covers it). Closes the v0.2
  deprecation notice.

### Added

- **Set operators via `Union` / `UnionAll` / `Intersect` / `Except`
  (Phase 2)**: any `Query[T]` can be combined with another `Query[T]`
  through the standard SQL compound-select form. Renders flat (no
  parens around operands) — `SELECT ... UNION ALL SELECT ...` — which
  is the only shape SQLite accepts and is portable across PG, MySQL,
  MariaDB, MSSQL, Oracle, SQLite. Dialect-keyword translation lives
  in a package-level `setOpKeyword` helper (kept out of the Dialect
  interface to avoid breaking custom implementations downstream):
  Oracle maps `EXCEPT` to `MINUS`; MySQL/MariaDB return
  `ErrUnsupportedFeature` for `INTERSECT`/`EXCEPT`; SQLite rejects
  `INTERSECT ALL`/`EXCEPT ALL`. Operand restrictions enforced at
  attach time (each surfaces as `ErrUnsupportedFeature`):
  - Operand cannot have `ORDER BY`, `LIMIT`, `OFFSET`, lock options,
    its own CTEs, or nested set-ops.
  - Base cannot have pessimistic locks (the dialect-specific lock
    suffix would bind to the combined result).
  Outer ORDER BY / LIMIT on the base apply to the combined result.

- **Window functions via `SelectExpr` + `Over` / `Window` / `RowNumber` /
  `Rank` / `DenseRank` / `Lag` / `Lead` (Phase 2)**: a typed surface for
  windowed projections that fits inside the AST. `Window` is a
  partition / order specification (`NewWindow().PartitionBy(Col("status")).
  OrderBy(Col("amount"), true)`) — immutable, chain-style. `Over(inner,
  w)` wraps any AST Expr with the OVER clause; the dedicated leaves
  `RowNumber`, `Rank`, `DenseRank`, `Lag(col, offset)`, and `Lead(col,
  offset)` cover the most-used window functions and bypass the function
  whitelist (their syntax is restricted to OVER (...) contexts the
  whitelist doesn't model). The Lag/Lead offset is bound as a parameter,
  not interpolated, so the bind path stays uniform.

  The new `Query[T].SelectExpr(alias, e)` method projects an arbitrary
  AST expression into the SELECT list aliased as `alias` (validated
  through `SQLGuard.ValidateIdentifier`):
  ```go
  q := quark.For[Sale](ctx, c).
      Select("id", "region", "amount").
      SelectExpr("rk", quark.Over(quark.Rank(),
          quark.NewWindow().
              PartitionBy(quark.Col("region")).
              OrderBy(quark.Col("amount"), true)))
  // SELECT "id", "region", "amount",
  //        RANK() OVER (PARTITION BY "region" ORDER BY "amount" DESC) AS "rk"
  // FROM "sales"
  ```
  AST projections compose with regular `Select(cols...)` (comma-joined
  in order). Their bind args land in the args slice between any CTE
  args and the WHERE args, matching the SQL-surface order.

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
