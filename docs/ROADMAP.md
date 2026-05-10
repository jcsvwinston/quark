# Quark ORM Roadmap

> Aligned with the phased plan in [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) §4. Each Phase ends with a release tag.

## v0.1.x (baseline) — completed

- [x] Type-safe `Query[T]` API.
- [x] Six dialects: SQLite, PostgreSQL, MySQL, MariaDB, MSSQL, Oracle.
- [x] Nested transactions with savepoints.
- [x] Eager loading via `.Preload()`.
- [x] Lifecycle hooks (`BeforeCreate` / `AfterUpdate` / …).
- [x] Tag- and method-based validation (`validator/v10`).
- [x] Schema migrations (auto + versioned via CLI).
- [x] Multi-tenant routing (database / schema / row-level strategies).
- [x] Streaming via `Iter` and `Cursor`.
- [x] M2M and polymorphic relations.
- [x] Custom dialects (`RegisterDialect`).
- [x] OpenTelemetry tracing + query observers.
- [x] L2 query cache (memory + Redis).
- [x] JSON column queries (now bound, not interpolated — see Phase 0).

## v0.3.0 — Phase 0 + Phase 1 (this release)

### Phase 0 — security & correctness

- [x] **P0-1** — Tenant isolation in `Or()` (closed via `cloneForGroup`).
- [x] **P0-2** — `WhereJSON` SQL injection (path validated + bound; `ErrInvalidJSONPath`).
- [x] **P0-3** — `linkM2M` swallowed errors (driver-aware `isUniqueViolation`).
- [x] **P0-4** — `Update` zero-value trap (`UpdateFields` + Phase 1 `Tracked.Save`).
- [x] **P0-5** — `JOIN ... ON` raw concatenation (`ValidateJoinOn` + `ErrInvalidJoin`).

### Phase 1 — rich types + dirty tracking

- [x] **F1-1** — Dirty tracking (`Track().Find().Save()` snapshot pipeline).
- [x] **F1-2** — Rich types core: `JSON[T]` typed wrapper, `[]byte` → BLOB / BYTEA / VARBINARY mapping.
- [x] **F1-3** — `Nullable[T]` generic + auto migrate detection.
- [x] **F1-4** — `RegisterTypeMapper` + db-tag sizing options (`size=N`, `precision=N`, `scale=N`); `time.Duration` shipped.
- [x] **F1-5** — Soft-delete scopes: `WithTrashed` / `OnlyTrashed` / `Restore`.
- [x] **F1-6** — Optimistic locking (`quark:"version"` + `ErrStaleEntity`).

## v0.4.0 — Phase 2 (this release)

- [x] **F2-AST** — Composable expression AST (`Expr`, `Col`, `Lit`, `Func`, `And`/`Or`/`Not`, `In`, `Cmp` + `Eq`/`Ne`/`Lt`/`Gt`/`Lte`/`Gte`); `Query[T].WhereExpr` / `HavingExpr` integration.
- [x] **F2-subqueries** — Typed subqueries (`AsSubquery()` + `Sub` / `Exists` / `NotExists` / `InSub` / `NotInSub`).
- [x] **F2-CTE** — `With(name, sub)` / `WithRecursive(name, sub)` prefix on outer SELECT.
- [x] **F2-window** — `SelectExpr(alias, e)` projection + `Over(inner, w)` + `RowNumber` / `Rank` / `DenseRank` / `Lag` / `Lead`.
- [x] **F2-set** — `Union` / `UnionAll` / `Intersect` / `Except` between `Query[T]` operands.
- [x] **F2-locking** — Pessimistic locking (`ForUpdate`, `ForShare`, `SkipLocked`, `NoWait`) + `Dialect.LockSuffix`.
- [x] **F2-nested-preload** — Dotted-path `.Preload("A.B.C")` walks the chain in one pass; shared prefixes deduped.
- [x] **F2-IN-chunking** — Eager-loading paths chunk parent keys at 1000 (Oracle/MSSQL caps).
- [x] **F2-having-agg** — `HavingAggregate(fn, column, op, value)` with COUNT/SUM/AVG/MIN/MAX whitelist.
- [x] **F2-join-builder** — Structured `Join(table).On(col, op, otherCol)` retires the v0.3.x string-raw form (BREAKING; see [`MIGRATION_v0.4.0.md`](MIGRATION_v0.4.0.md)).

## Phase 3 — schema diff + migrations (v0.5)

- [ ] Real introspection-based schema diff (types, NOT NULL, defaults, indexes, FKs).
- [ ] Distributed migration locking (`pg_advisory_xact_lock` / `GET_LOCK` / `sp_getapplock` / `DBMS_LOCK`).
- [ ] Transactional migrations where the engine allows; resumable migrations on MySQL.
- [ ] Backfill orchestration with resume tokens.

## Phase 4 — observability + cache (v0.6)

- [ ] OTel metrics (counters, histograms).
- [ ] SQL redaction in spans.
- [ ] Cache stampede protection + granular invalidation.
- [ ] Deadlock retry with exponential backoff.

## Phase 5 — RLS + hooks + events (v0.7)

- [ ] Real Postgres RLS (`SET LOCAL app.tenant_id` + `CREATE POLICY` template).
- [ ] Transactional hooks (`OnCommit` / `OnRollback`, `BeforeFind` / `AfterFind`).
- [ ] Real `EventBus`.
- [ ] Optional audit log.

## Phase 6 — codegen + HA (v1.0)

- [ ] Codegen path (typed scanners, no reflect).
- [ ] Read replicas / pool routing / failover.
- [ ] Sharding pluggable.
- [ ] Real benchmarks vs `database/sql` / GORM / ent / sqlc.

The v1.0 honest checklist is in [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) §3 (gaps).
