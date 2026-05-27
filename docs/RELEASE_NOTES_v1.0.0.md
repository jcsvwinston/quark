# Quark v1.0.0 — Release Notes (DRAFT)

> **⚠ DRAFT — v1.0.0 is NOT tagged yet.** The [v1.0 gate](V1_GATE.md) §A is
> now **fully closed (5/5)** as of PR #127 (Oracle in blocking CI, Item 1).
> v1.0.0 is unblocked: tag it with **`/release v1.0.0`** — that command does
> the version bump, Docusaurus versioning, and tag. This banner is removed by
> the release PR, not here.
>
> The **Known limitations** section below is authoritative: each waiver landed
> here in the same PR that closed its gate item (V1_GATE.md §D).

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

_(Authored by the `/release v1.0.0` PR — one paragraph per phase, Fases 0–6,
written alongside the version bump and Docusaurus versioning per
`.claude/commands/release.md` step 8. The gate is closed; the narrative is
release prose, not a gate blocker.)_

## Known limitations

Each item below is a feature consciously deferred past v1.0, documented here so
adopters see the boundary before they build on it.

- **Inbound PostgreSQL `LISTEN/NOTIFY` is not implemented.** The event bus is
  **outbound only** (`Client.UseEventBus` publishes `created`/`updated`/`deleted`
  post-commit; the `Notify` helper sends `pg_notify`). The inbound listener
  `ListenerFactory.CreateListener` returns `ErrDialectNotSupported` on every
  dialect. Consuming `LISTEN/NOTIFY` (a dedicated connection outside the pool,
  with reconnect/backpressure semantics) is planned post-v1.0. See
  [Event Bus → Not the same as LISTEN/NOTIFY](https://jcsvwinston.github.io/quark-docs/docs/advanced/events). *(V1_GATE §A Item 3.)*
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
- **Oracle is in blocking CI** (no longer a limitation). As of PR #127 the
  Oracle SharedSuite (216/0) runs on every PR in the `integration` matrix
  alongside PostgreSQL/MySQL/MariaDB/MSSQL — the *"ORM with real Oracle"*
  positioning is now backed by CI, not manual validation (V1_GATE §A Item 1,
  *Salida A*). The job boots `gvenzl/oracle-free` via `docker run` rather than
  testcontainers (whose lifecycle crashes on hosted runners).

## Migration from v0.x

No breaking changes since v0.9.0. See [`docs/MIGRATION_v0.9.0.md`](MIGRATION_v0.9.0.md)
for the last breaking-minor changes; v0.10 through the v1.0 cut introduced none.
