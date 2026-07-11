# Release notes — v1.2.0

**Scaling follow-ups.** v1.2.0 closes the two scaling deferrals documented at
v1.1 — cross-instance cache-stampede coordination and sharding ergonomics —
and carries a dependency security pin. No breaking changes.

Docs (1.2.0 is the current version): <https://jcsvwinston.github.io/quark/docs/>
(older versions under `/docs/1.1.5/` etc.)

## Added

- **Cache — cross-instance stampede coordination (ADR-0020):** the existing
  stampede protection (singleflight + TTL jitter + XFetch, ADR-0011) is
  in-process, so N processes missing the same hot key still meant N
  recomputes. Opt-in `WithCacheCrossInstance()` uses an optional `CacheLocker`
  capability (type-asserted on the store; `memory` and `redis` via
  `SET NX PX` + token-checked release implement it): the lock winner
  recomputes and writes, losers wait-and-reread until the value appears. A
  lock-backend error degrades to the uncoordinated ADR-0011 behavior — it
  never fails the request.
- **Sharding — shard key from the entity (ADR-0021):** entities can implement
  `ShardKeyer` and be routed with `WithShardKeyOf(ctx, entity)` instead of
  hand-threading the key through every call site. Caller-side helper, not a
  router hook — routing stays explicit and inspectable.
- **Sharding — scatter-gather cross-shard reads (ADR-0022):**
  `ScatterGather` / `ScatterCount` fan a read out to every shard and merge
  caller-side via an explicit `ScatterMerge`. COUNT is the only aggregate
  merged for you; other cross-shard aggregates stay deliberately manual.

## Fixed

- **Security — dependency pins:** toolchain pinned to go1.26.5 and pgx to
  v5.9.2, picking up the upstream security fixes accumulated against the
  versions pinned at v1.1.5. `govulncheck` against the Go vulnerability
  database is the source of truth for advisory status (see `SECURITY.md`).
- **Query — Update zero-value warning:** the "skipped zero-value" warning now
  fires only for scalar zero values, not for nil pointers (which are the
  idiomatic way to skip a column in a partial update).

## Performance

- **Read path:** the per-row scan plan is memoized and the result slice
  pre-sized (AUD-2) — less reflection and fewer allocations on `List`-shaped
  reads.

No breaking changes. `v1.x` keeps API compatibility; breaking changes go to
`v2.x` with a migration guide.
