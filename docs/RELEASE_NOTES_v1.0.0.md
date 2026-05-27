# Quark v1.0.0 — Release Notes

The first stable release. v1.0 closes the qualitative [v1.0 gate](V1_GATE.md)
§A (5/5) — most notably Oracle joining the blocking CI matrix (216/0), which
completes cross-engine coverage across all six dialects. The **Known
limitations** section below is authoritative: each waiver landed in the same
PR that closed its gate item (V1_GATE.md §D).

## What v1.0 means

v1.0 is the **honest** first stable release — the point where the public API
is committed to under SemVer:

- `v1.x` releases keep API compatibility; breaking changes go to `v2.x` with a
  `docs/MIGRATION_v2.0.0.md`.
- v1.0 is gated on the **qualitative checklist** in [`docs/V1_GATE.md`](V1_GATE.md)
  (cross-engine coverage, structural gaps closed or consciously waived), **not**
  on a performance target — the ADR-0002 ≥3× p99 codegen gate was retired by
  [ADR-0017](adr/0017-codegen-type-safety-not-perf-gate.md). Codegen is a
  type-safety feature, not a speedup.

## Phases delivered

v1.0 is the sum of seven development phases (0–6). Each shipped incrementally
across the v0.x line; the per-version detail is in [`CHANGELOG.md`](../CHANGELOG.md)
and [`docs/ROADMAP.md`](ROADMAP.md).

- **Phase 0 — security & correctness** (v0.3.0, cleanup in v0.5.0). Closed the
  five P0 bugs found in the first audit: tenant-isolation leak in `Or()`,
  `WhereJSON` path injection (now validated + bound), swallowed `linkM2M`
  errors, the `Update` zero-value trap, and raw `JOIN ... ON` concatenation.
  The v0.5.0 cleanup added the infrastructure the rest of the line relies on:
  a testcontainers per-engine CI matrix (blocking on PG/MySQL/MariaDB/MSSQL),
  release-please automation, a docs linter, and the Docusaurus deploy pipeline.

- **Phase 1 — rich types + dirty tracking** (v0.3.0; closed out in v0.6.0 and
  v0.7.0). Dirty tracking (`Track().Find().Save()`), the `JSON[T]` and
  `Nullable[T]` typed wrappers, `RegisterTypeMapper` with `size`/`precision`/
  `scale` tag sizing, soft-delete scopes, and optimistic locking
  (`quark:"version"`). The deferred type work closed later: `Array[T]` in
  v0.6.0 and per-column timezones (`quark:"tz=..."`, UTC-on-the-wire) in v0.7.0.

- **Phase 2 — advanced query builder** (v0.4.0). A composable expression AST,
  typed subqueries (`Exists`/`InSub`/…), CTEs (`With`/`WithRecursive`), window
  functions, set operations (`Union`/`Intersect`/`Except`), pessimistic
  locking (`ForUpdate`/`SkipLocked`/…), dotted-path nested preload, and
  IN-list chunking. The structured `Join(table).On(...)` builder replaced the
  v0.3.x string form (breaking — see [`MIGRATION_v0.4.0.md`](MIGRATION_v0.4.0.md)).

- **Phase 3 — schema-as-code migrations** (v0.6.0; Oracle completed at the v1.0
  cut). Distributed migration lock, neutral schema introspection, a pure-Go
  schema diff with a `models → Plan → Apply` pipeline, transactional or
  resumable apply per dialect, the `quarkmigrate` plan/verify/apply library,
  orchestrated `Backfill`, and a per-Client model registry. Oracle
  introspection (F3-2) and its `DBMS_LOCK`-based migration lock landed in the
  v1.0 cut, completing the pipeline on all six engines.

- **Phase 4 — observability + resilient cache** (v0.8.0). OpenTelemetry metrics
  and spans with argument redaction, a slow-query log, deterministic
  (collision-free) cache keys, in-process stampede protection (singleflight +
  TTL jitter + XFetch), per-row cache invalidation, and opt-in deadlock retry
  on `Client.Tx` across all engines.

- **Phase 5 — engine-enforced multi-tenancy, hooks, events, audit** (v0.9.0).
  PostgreSQL-native row-level security (`RowLevelSecurityNative` via
  `set_config` + `CREATE POLICY`) with the `quarktenant` policy-installer CLI;
  transactional `After*` hooks that fire post-commit plus `Before/AfterFind`;
  public `Tx.OnCommit`/`Tx.OnRollback`; a real `EventBus`; and an audit log
  written atomically with each write. Two breaking-minors — see
  [`MIGRATION_v0.9.0.md`](MIGRATION_v0.9.0.md).

- **Phase 6 — code generation, HA, sharding, benchmarks** (v0.10.0 → v1.0.0).
  Opt-in code generation (`quark gen`: typed scanners and a single-integer-PK
  INSERT binder) that auto-registers and falls back to reflection; typed query
  field accessors; read replicas with pool routing and replica failover; a
  pluggable `ShardRouter`; a stress/load harness; and a reproducible benchmark
  harness comparing Quark against raw `database/sql`, GORM, and the
  code-generation tier (ent, sqlc). The benchmarks ([ADR-0017](adr/0017-codegen-type-safety-not-perf-gate.md))
  showed reflection is not the bottleneck — sqlc (no runtime) sits on the raw
  floor while ent (codegen + a runtime) stays in the reflect class — so the
  ADR-0002 ≥3× p99 performance gate was retired and codegen reframed as a
  type-safety feature, not a speedup.

## Known limitations

Each item below is a feature consciously deferred past v1.0, documented here so
adopters see the boundary before they build on it.

- **Inbound PostgreSQL `LISTEN/NOTIFY` is not implemented.** The event bus is
  **outbound only** (`Client.UseEventBus` publishes `created`/`updated`/`deleted`
  post-commit; the `Notify` helper sends `pg_notify`). The inbound listener
  `ListenerFactory.CreateListener` returns `ErrDialectNotSupported` on every
  dialect. Consuming `LISTEN/NOTIFY` (a dedicated connection outside the pool,
  with reconnect/backpressure semantics) is planned post-v1.0. See
  [Event Bus → Not the same as LISTEN/NOTIFY](https://jcsvwinston.github.io/quark/docs/advanced/events). *(V1_GATE §A Item 3.)*
- **Cache stampede protection is in-process only.** Singleflight, TTL jitter,
  and XFetch coordinate within a single process. In a multi-replica deployment,
  N replicas can each compute the same hot key once (much less severe than an
  in-process stampede, but real). A cross-instance distributed-lock hook is
  planned post-v1.0 (ADR-0011 §"Cuándo reabrir"). *(V1_GATE §A Item 4.)*
- **Code generation covers the read path and single-integer-PK INSERT.** The
  generated fast paths are typed scanners (F6-2) and the INSERT binder for
  single-integer-PK models (F6-3a). `UPDATE`/partial/batch still use the
  reflection path (F6-3b deferred; ~1% measured payoff per ADR-0017). The
  reflection path is the permanent default and is fully supported; codegen is
  opt-in for compile-time type-safety. *(V1_GATE §B Item 7.)*
- **The versioned migration registry is global.** The model registry is
  per-Client since v0.6 (F3-7), but the versioned migration registry in
  `migrate/migrate.go` is still process-global. Two Clients in one process share
  it. *(V1_GATE §B Item 8.)*
- **Read-replica failover recovery is passive.** A downed replica is taken out
  of rotation for a cooldown and rejoins on the first retry after it; there is
  no active health-check goroutine. Active health checks are a post-v1.0 item.
- **Sharding routes per shard key; advanced features are post-v1.0.**
  `ShardRouter` routes each query to the owning shard by a shard key supplied
  per operation via context (see the runnable `examples/sharding/`). **Cross-shard
  scatter-gather** (read fan-out with merge) and **shard-key-from-entity**
  (deriving the key from the model on writes) are deferred to v1.1; there are no
  cross-shard joins or transactions. *(V1_GATE §A Item 2.)*

## Migration from v0.x

No breaking changes since v0.9.0. See [`docs/MIGRATION_v0.9.0.md`](MIGRATION_v0.9.0.md)
for the last breaking-minor changes; v0.10 through the v1.0 cut introduced none.
