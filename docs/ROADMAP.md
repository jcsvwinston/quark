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

## v0.3.0 — Phase 0 + Phase 1

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

## v0.8.0 — Phase 4

Observability, stampede-protected caché, resilience. Closes the F4-1
through F4-7 backlog (see [ADR-0011](adr/0011-cache-stampede-protection-wrapper.md)
for the cache wrapper decision):

- [x] **F4-1 OTel metrics** — counter `quark.queries.total`, histograms `quark.queries.duration` (ms) and `quark.queries.rows` (Exec only) on the `github.com/jcsvwinston/quark` meter. Etiquetados por `db.operation` y `db.system` (cuando `WithDBSystem` está seteado).
- [x] **F4-2 Span argument redaction** — `WithSpanRedaction(mode)`. Default `RedactArgs` keeps bind values off spans; `IncludeArgs` opts in for local debug.
- [x] **F4-3 Slow query log** — `WithSlowQueryThreshold(d)`. Single comparison on the centralised observer path; bind args never logged.
- [x] **F4-4 Cache key determinism** — type-tagged, length-prefixed encoding. Closes three collision classes (type / boundary / nil) the previous `%v` encoding allowed. Prerequisite of F4-5/F4-6.
- [x] **F4-5 Cache stampede protection** — `stampedeStore` wrapper auto-installed by `WithCacheStore`: singleflight in-process + ±jitter TTL + Vattani XFetch. `WithCacheJitter` and `WithCacheXFetchBeta` tune the knobs. Cross-instance gap documented.
- [x] **F4-6 Per-row invalidation + Redis tag-TTL fix** — `<table>:<pk>` tag in addition to the table tag on `Update`/`Delete`/`Tracked.Save`/`Create`; Redis tag-set TTL now takes the MAX via `ExpireNX` + `ExpireGT` (Redis 7+).
- [x] **F4-7 Deadlock retry** — `WithDeadlockRetry(maxAttempts)` on `Client.Tx`. Exponential backoff + jitter, opt-in, ctx-aware. PG 40P01 / MySQL 1213 / MSSQL 1205 / Oracle ORA-00060.

## v0.7.0 — Per-column timezones

Minor release. Closes the last deferred type from Phase 1's Bloque B
(see [ADR-0010](adr/0010-per-column-timezone-override.md)):

- [x] **Per-column timezones** — `time.Time` columns can declare a timezone via the `quark:"tz=Europe/Madrid"` tag or inherit a Client-wide default via `quark.WithDefaultTZ(loc)`. Precedence is column tag → client default → driver pass-through. Wire contract is UTC-always: values go to the database as UTC and are converted to the configured location in memory on scan. Honoured on `time.Time`, `*time.Time` and `Nullable[time.Time]`, including through `Preload`. An invalid IANA name fails fast in `RegisterModel` / `Migrate` with the new `ErrInvalidTimezone` sentinel. Fully opt-in — no change for callers that don't use it.

With this, Phase 1's Bloque B is closed entire (`Array[T]` shipped in v0.6.0).

## v0.6.0 — Phase 3

Schema-as-code migrations. Closes the F3-1 through F3-7 backlog:

- [x] **F3-1** — Distributed migration lock (`Client.AcquireMigrationLock`). PG `pg_advisory_lock` + `lock_timeout`; MySQL / MariaDB `GET_LOCK` / `RELEASE_LOCK`; MSSQL `sp_getapplock @LockOwner='Session'`. Optional `MigrationLocker` interface on Dialect; SQLite / Oracle return `ErrUnsupportedFeature`.
- [x] **F3-2** — Neutral schema introspection (`Client.IntrospectSchema`). `Schema{Tables[]Table{Columns, Indexes, ForeignKeys, Checks}}` populated across SQLite / PostgreSQL / MySQL / MariaDB / MSSQL. Oracle deferred pending CI image fix; SQLite `Checks` deferred (no catalog).
- [x] **F3-3** — Pure-Go schema diff (`Diff`) + models→Plan pipeline (`Client.PlanMigration`) + executor (`Client.ApplyPlan`) + cross-dialect type / default normalisation. Round-trip identity contract: `Migrate(model)` followed by `PlanMigration(model)` returns an empty `Plan` on all five CI motors.
- [x] **F3-4** — Transactional `ApplyPlan` on PG / MSSQL / SQLite; resumable `ApplyPlan` on MySQL / MariaDB / Oracle via `quark_migration_state(plan_hash, op_index)` checkpoints. `Plan.Hash()` exposes the deterministic plan identity for CI gates.
- [x] **F3-5** — `quarkmigrate` package: `plan` / `verify` / `apply` actions with explicit exit codes (`ExitSuccess` / `ExitDriftDetected` / `ExitError`). Library not binary — users embed in a thin `migrations/main.go`. Example in `examples/migrations/`.
- [x] **F3-6** — Orchestrated `Client.Backfill(ctx, BackfillSpec)` with PK-based batching and `quark_backfill_state(name, last_pk)` resume tokens. Idempotent on completion.
- [x] **F3-7** — Per-Client model registry (`Client.RegisterModel` / `RegisteredModels` / `MigrateRegistered` / `PlanMigrationRegistered`). Additive; the global type-meta cache stays.

Also lands `Array[T]` (typed wrapper for list-shaped columns; JSON-backed; closes Bloque B / Arrays Postgres from Phase 1 deferred work).

## v0.5.0 — Phase 0 cleanup

No new public API. Closes the F0-1 through F0-10 backlog:

- [x] **F0-1..F0-5** — README / examples / ROADMAP / SECURITY / version-doc cosmetic alignment.
- [x] **F0-6** — gh-pages deploy workflow for `website/`.
- [x] **F0-7** — Docusaurus versioning initialised.
- [x] **F0-8** — Per-engine integration matrix via testcontainers-go. CI **blocking** on PG / MySQL / MariaDB / MSSQL; Oracle excluded pending image fix. The "tests pass on 6 engines before merge" rule is now enforced (4/5 in CI).
- [x] **F0-9** — release-please workflow for automated version bumps + CHANGELOG generation from Conventional Commits.
- [x] **F0-10** — Docs linter (`scripts/lint-docs.sh`) in CI: anti-marketing, `RELEASE_NOTES_V1` leak detection, broken relative links (Docusaurus-aware).

## v0.4.0 — Phase 2

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

## v0.9.0 — Phase 5

RLS real + transactional hooks + EventBus + audit. ADRs archivados:
[ADR-0012](adr/0012-rls-real-postgres-set-local-plus-policies.md) (RLS
real PG `set_config` + `CREATE POLICY`, supersede ADR-0003) y
[ADR-0013](adr/0013-transactional-hooks-and-sync-eventbus.md) (hooks
transaccionales + EventBus síncrono en commit-phase). Dos breaking
minors (timing de hooks `After*` bajo `Client.Tx`; rename del
placeholder `EventBus` → `ListenerFactory`) — ver
[`MIGRATION_v0.9.0.md`](MIGRATION_v0.9.0.md):

- [x] **F5-1** — Rename `RowLevelSecurity` → `RowLevelSecurityClient` +
  alias deprecado (retirada en v1.0). Foundation-only (#78).
- [x] **F5-2** — `RowLevelSecurityNative` motor real Postgres:
  `set_config('app.tenant_id', …, true)` por tx + `nativeRLSExecutor` +
  `TenantRouter.Tx`. PG-only; rechazo `ErrUnsupportedFeature` en
  motores sin policies (#80).
- [x] **F5-3** — CLI `quarktenant install-rls-policies [--dry-run]`:
  generador de DDL (`ENABLE/FORCE ROW LEVEL SECURITY` + `CREATE POLICY`)
  por modelo registrado, apply transaccional bajo migration lock.
  Reutiliza F3-1 + F3-7 (#81).
- [x] **F5-4** — Hooks transaccionales: `After*` fire **post-commit**
  bajo `Client.Tx` (breaking minor; rollback descarta la cola) +
  `BeforeFindHook` / `AfterFindHook` (#82).
- [x] **F5-5** — `Tx.OnCommit(fn)` / `Tx.OnRollback(fn)` API pública
  (FIFO, errores no paran la cadena) + `quark.TxFromContext(ctx)` (#83).
- [x] **F5-6** — `EventBus` interfaz pública +
  `LoggerEventBus`/`OTelEventBus` in-tree + `Client.UseEventBus(bus)`.
  Emisión síncrona at-least-once vía `OnCommit`; sin outbox.
  Placeholder `EventBus` struct → `ListenerFactory` (#84).
- [x] **F5-7** — Audit log opcional: tabla `quark_audit` portable
  (modelo + migrator), `Client.EnableAuditLog(AuditConfig)`, diff por
  fila escrito **atómico** con el commit (no post-commit — ADR-0013
  "junto al commit") (#85).

## v0.10.0 — fixes + Phase 6 opening

Maintenance release: correctness fixes plus the formal opening of
Phase 6. No new public API beyond the Native-RLS warning.

- [x] Savepoint rollback unwinds the `After*`/`OnCommit`/`OnRollback`
      hooks queued in that scope, so undone work no longer fires its
      side-effects on the outer commit ([#88]).
- [x] `JSON[T]` / `Array[T]` round-trip on SQL Server — `Value()` binds
      as NVARCHAR (string), not VARBINARY (`[]byte`) ([#89]).
- [x] Real cross-engine deadlock-retry integration test (PG / MySQL /
      MariaDB) backing `WithDeadlockRetry` ([#90]).
- [x] `quark.tenant.raw_under_native_rls` warning when `RawQuery`/`Exec`
      run with a tenant in context under `RowLevelSecurityNative` ([#91]).
- [x] Phase 6 formally opened — ADR-0014 + F6-1..F6-9 ([#93]).

[#88]: https://github.com/jcsvwinston/quark/pull/88
[#89]: https://github.com/jcsvwinston/quark/pull/89
[#90]: https://github.com/jcsvwinston/quark/pull/90
[#91]: https://github.com/jcsvwinston/quark/pull/91
[#93]: https://github.com/jcsvwinston/quark/pull/93

## Phase 6 — codegen + HA (v1.0)

Formally opened 2026-05-22 (full scope). Anchor decision:
[ADR-0014](adr/0014-codegen-coexistence-typed-registry.md) (codegen ↔
reflect coexistence). Decomposed into F6-1..F6-9 in
[`TASKS.md`](../TASKS.md) § "Fase 6".

- [x] **Codegen** (F6-1 skeleton, F6-2 typed scanners, F6-3a INSERT
      binder, F6-4 typed query field accessors) — opt-in, no reflect.
      Delivered v0.11.0–v0.12.0. F6-3b (UPDATE/partial/batch binder)
      deferred (ADR-0017: ~1% payoff; reopen only for type-safety).
- [x] **HA**: read replicas / pool routing (F6-5) + primary failover
      (F6-6) — delivered v0.13.0 (ADR-0015).
- [x] **Sharding** pluggable (F6-7, `ShardRouter`) — merged to `main`
      (ADR-0016); follow-ups (scatter-gather, shard-key-from-entity,
      runnable PG example) pending.
- [x] **Benchmarks** vs `database/sql` / GORM (F6-8a, v0.11.0) +
      stress/load testing (F6-9, v0.13.0). F6-8b (ent + sqlc
      codegen-tier) deferred — informational, not a gate (ADR-0017).

The v1.0 honest checklist is in [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) §3 (gaps).
The ADR-0002 ≥3× p99 codegen gate has been **retired** by
[ADR-0017](adr/0017-codegen-type-safety-not-perf-gate.md): F6-8a + profiling
(`benchmarks/PROFILING.md`) showed reflect is not the bottleneck (engine +
`database/sql` + architectural allocs dominate), so the gate is unreachable by
scan/bind codegen. Codegen is justified by **type-safety** (F6-4), not speed.
v1.0 now ships against the honest checklist above, not a speedup target.
