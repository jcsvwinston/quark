# Quark v0.8.0 — Release Notes

> **Date:** 2026-05-15
> **Status:** late-alpha. Not yet v1.0 production-ready.
> See [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) for the honest gap analysis between the current state and a planned v1.0.

Phase 4 release. Closes F4-1 through F4-7: observability (OTel metrics, span argument redaction, structured slow-query log), stampede-protected caché (deterministic cache key, singleflight + ±jitter + XFetch wrapper, per-row invalidation and Redis tag-TTL fix), and resilience (deadlock retry with exponential backoff on `Client.Tx`). Every new feature is **opt-in** — the v0.7 surface keeps working unchanged. No breaking changes; no migration guide.

## What's in this release

### Observability

- **F4-1 OTel metrics.** `quark/otel` now emits three instruments on the `github.com/jcsvwinston/quark` meter alongside the existing tracing spans:
  - `quark.queries.total` (Int64 counter) — every Quark operation increments.
  - `quark.queries.duration` (Float64 histogram, ms) — wall-clock duration of the wrapped op.
  - `quark.queries.rows` (Int64 histogram) — `RowsAffected` for Exec only (counting SELECT rows would require wrapping `*sql.Rows`; documented gap).

  Etiquetados por `db.operation` y — cuando se setea con `otel.WithDBSystem(name)` — `db.system`. El meter se resuelve lazy del `MeterProvider` global, mismo patrón que el tracer. ([#70](https://github.com/jcsvwinston/quark/pull/70))

- **F4-2 Span argument redaction.** New `otel.WithSpanRedaction(mode)`. Default `RedactArgs` keeps bind values out of every span — only the parameterised SQL reaches `db.statement`. Opt-in `IncludeArgs` attaches `db.statement.args` for local debug. A tracing backend MUST NOT see user values it has no authority to retain — the redaction is on by default. ([#70](https://github.com/jcsvwinston/quark/pull/70))

- **F4-3 Slow query log.** New `quark.WithSlowQueryThreshold(d)` `Option`. When set, every operation whose duration exceeds `d` emits a structured WARN through `Client.logger` with `duration_ms`, `threshold_ms`, `operation`, `table`, `rows` and `sql` (parameterised). **Bind arguments are NOT included** — same redaction principle as F4-2. Single comparison on the centralised observer dispatcher, so a Client with the feature off pays nothing. ([#71](https://github.com/jcsvwinston/quark/pull/71))

### Caché de producción (ADR-0011)

- **F4-4 Deterministic cache key.** `generateCacheKey` dropped `fmt.Sprintf("%v", arg)` for a type-tagged, length-prefixed encoding. Closes three collision classes a parameterised cached SELECT could hit: type (`int64(1)` vs `string("1")`, also `uint64`/`float64`/`bool`/`nil`), boundary (no separators meant tenant `"my"`+schema `"sql"` hashed the same stream as `"mysql"`+`""`, and args `"ab"`+`""` the same as `"a"`+`"b"`), and `nil` vs `""`. `time.Time` keyed by `UnixNano()` so the same instant in different zones hits the same key. Reflection-free (ADR-0002). Prerequisite of F4-5/F4-6 — they couldn't ship on a frail key. ([#69](https://github.com/jcsvwinston/quark/pull/69))

- **F4-5 Cache stampede protection.** New internal `stampedeStore` wrapper (`cache_stampede.go`) auto-installed by `WithCacheStore` — `memory.Store`, `redis.Store`, and any third-party `CacheStore` keep working unchanged inside it, and the public `CacheStore` interface doesn't break. Three protections, all "todo o nada":

  - **Singleflight** (`golang.org/x/sync/singleflight`): `N` concurrent callers for a single key collapse to one compute. A miss never produces a database stampede on a hot key.
  - **TTL jitter**: every `Set` randomises the TTL by `±jitterPct` (default `±10%`, tune with `WithCacheJitter(pct)`), so batch-warmed entries don't expire in lockstep.
  - **XFetch / probabilistic early refresh**: each entry embeds metadata (compute delta + timestamps) as a length-prefixed `xfetchEntry`. `Get` evaluates `timeLeft ≤ delta · β · (-ln(rand()))` (Vattani et al.) and signals early refresh near expiry. `WithCacheXFetchBeta(β)` tunes; `β = 0` disables XFetch only.

  The query path detects `*stampedeStore` via type assertion and uses the richer `getOrCompute` API; third-party stores fall back to the historical cache-aside dance. **Known limitation**: singleflight is in-process only — cross-instance stampede is not covered (ADR-0011 §Consecuencias). ([#72](https://github.com/jcsvwinston/quark/pull/72), gofmt follow-up [#73](https://github.com/jcsvwinston/quark/pull/73))

- **F4-6 Per-row invalidation + Redis tag-TTL fix.** `executeExec` accepts an `extraTags ...string` variadic; mutations that know their PK (`Update`, `UpdateFields`, `Tracked.Save`, soft/hard delete by PK, `Create` post-PK-populate, `UpdateBatch`) now pass `<table>:<pk>`. The single `InvalidateTags` call carries both the table tag (historical default — listings stay consistent) and the row tag. Callers can cache by-PK queries with the per-row tag:

  ```go
  user, _ := quark.For[User](ctx, client).
      Where("id", "=", 1).
      Cache(5*time.Minute, "users", "users:1").
      First()
  ```

  An `UpdateFields(&u, "name")` on `id=1` invalidates `users` AND `users:1` — listings refresh, this Find drops. An update on `id=2` invalidates `users:2` — `users:1` survives.

  The Redis store's tag-set TTL is no longer shrinkable: `pipe.Expire(...)` (which over-wrote with the latest key's TTL, leaving cached keys with no surviving tag entry) was replaced with `pipe.ExpireNX(...)` + `pipe.ExpireGT(...)`. NX initialises when SADD just created the set; GT extends only when new > current. Requires Redis 7.0+ for the NX/GT flags — older servers fall back to the historical (broken) behaviour, documented gap. Composite-PK models fall back to the table tag; `Upsert` / `UpsertBatch` / `DeleteBatch` with complex WHERE / raw `Exec` keep table-only invalidation. ([#74](https://github.com/jcsvwinston/quark/pull/74))

### Resilience

- **F4-7 Deadlock retry on `Client.Tx`.** New `quark.WithDeadlockRetry(maxAttempts)` `Option`. When the transaction closure returns an error that `isDeadlock` recognises from the active driver, the runner sleeps with exponential backoff + ±50% jitter (10ms doubling, capped at 1s) and re-executes the closure against a fresh transaction:

  | Driver | Code |
  |---|---|
  | PostgreSQL | `40P01` (deadlock_detected) |
  | MySQL / MariaDB | `1213` (ER_LOCK_DEADLOCK) |
  | MSSQL | `1205` (chosen as deadlock victim) |
  | Oracle | `ORA-00060` |

  The retry wraps the **entire** closure, not individual queries — a deadlock aborts the whole tx, so re-running a single statement inside a half-committed state would race. SQLite is single-writer and never raises a true deadlock; the option is a no-op there. Non-deadlock errors propagate on the first attempt; a cancelled context aborts the backoff and surfaces `ctx.Err()` — callers stay in control of the budget. Disabled by default. ([#75](https://github.com/jcsvwinston/quark/pull/75))

### Phase 4 opening + infra

- [#67](https://github.com/jcsvwinston/quark/pull/67) — Phase 4 formally opened: ADR-0011 (cache stampede wrapper) + decomposition into F4-1..F4-7 in `TASKS.md`.
- [#68](https://github.com/jcsvwinston/quark/pull/68) — release-please workflow opted into Node.js 24 ahead of the 2026-06-02 GitHub cutoff.

## Known limitations

- Quark is **late-alpha** (~v0.8). Not v1.0 production-ready. Phase 5 (real RLS engine via `CREATE POLICY` + `SET LOCAL`, transactional hooks, real `EventBus`, audit log) and Phase 6 (codegen, read replicas, sharding, real benchmarks) are not yet in scope.
- **Cache stampede is in-process only.** A multi-replica deployment can still see N processes each compute the same hot key on miss. Much less severe than the in-process case, but real — ADR successor will add a distributed-lock hook if real demand surfaces.
- **F4-6 per-row invalidation skips composite PKs.** A stable encoding of composite primary keys (length-prefixed, dialect-neutral) would let them in; follow-up if demand surfaces.
- **F4-7 deadlock retry has no real cross-engine integration test.** Provoking a deterministic deadlock requires running parallel transactions with inverted lock order against a multi-writer engine — tracked as a F4-7 follow-up. The classifier (`isDeadlock`) and the retry loop have full unit coverage.
- **Redis tag-TTL fix requires Redis 7.0+.** Older Redis servers no-op the NX/GT flags and the historical (broken) behaviour returns. Documented in `docs/playbooks/cache.md`.
- Carry-overs from earlier releases: Oracle has no CI coverage (gvenzl/oracle-free image issue); MSSQL `JSON[T]` round-trip skip pending the NVARCHAR(MAX) encoding fix; `OpAlterColumn` covers type changes only; SQLite `DropForeignKey` / `DropCheck` return `ErrUnsupportedFeature`.

## Upgrading

```bash
go get github.com/jcsvwinston/quark@v0.8.0
```

No source-code changes required — v0.8 is fully additive. Every new option (`WithCacheJitter`, `WithCacheXFetchBeta`, `WithSlowQueryThreshold`, `WithDeadlockRetry`, `otel.WithSpanRedaction`, `otel.WithDBSystem`) is opt-in; existing client construction keeps working unchanged.

If you adopt the new features:

1. **Caching** — once you call `quark.WithCacheStore(...)`, the stampede wrapper is automatic. Read [`caching-observability.mdx § Stampede protection`](https://jcsvwinston.github.io/quark/docs/0.8.0/advanced/caching-observability#stampede-protection) for the cross-instance gap before relying on it for multi-replica deploys.
2. **Per-row invalidation** — pass `"<table>:<pk>"` as a tag on `Find`-style cached queries to scope invalidation to that row. See [`caching.mdx § Per-row invalidation`](https://jcsvwinston.github.io/quark/docs/0.8.0/reference/api/caching#per-row-invalidation).
3. **Slow-query log** — `WithSlowQueryThreshold(100*time.Millisecond)` is a reasonable starting point. The log line ends up wherever your `*slog.Logger` writes (default `slog.Default()`).
4. **Deadlock retry** — `WithDeadlockRetry(3)` is enough for typical workloads. Don't enable it on a transaction that has external side effects (sending emails, charging cards) — those will be re-run on retry.

## Versioned docs

The page-versioned site for v0.8.0 is at `https://jcsvwinston.github.io/quark/docs/0.8.0/`.
