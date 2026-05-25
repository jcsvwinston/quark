# Quark v0.13.0 — Release Notes

> **Date:** 2026-05-25
> **Status:** late-alpha. Not yet v1.0 production-ready.
> See [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) for the honest gap analysis between the current state and a planned v1.0.

A Phase 6 HA + performance release: opt-in **read replicas** with automatic
failover, plus an allocation cut on the query builder. **No breaking changes** —
read replicas are opt-in (`WithReplicas`); without them every operation uses the
single primary connection exactly as before.

## Added

### Read replicas with read/write split and read-your-writes (F6-5)

`WithReplicas(dsns...)` opens read-only connection pools and routes multi-row
reads (`List` / `Iter` / eager-loading) across them round-robin, while writes
always go to the primary (ADR-0015):

```go
client, err := quark.New("pgx", primaryDSN,
    quark.WithReplicas(replica1DSN, replica2DSN),
    quark.WithMaxOpenConns(16),
)

// Read-your-writes: pin a read to the primary when it must see a recent write.
fresh, _ := quark.For[User](quark.Sticky(ctx), client).Where("id", "=", id).List()
```

Reads inside a `Client.Tx` and under `RowLevelSecurityNative` always use the
primary. See the [Read replicas guide](https://jcsvwinston.github.io/quark-docs/docs/0.13.0/advanced/read-replicas).

### Automatic replica failover (F6-6)

A read routed to a replica that fails with a transient connection error **fails
over to the primary** and the replica is taken out of rotation for a cooldown
(`WithReplicaDownCooldown`, default 5s), then retried — a downed replica degrades
performance, not correctness. There is no multi-primary promotion: the model has
a single primary (the fallback target).

## Performance

### Copy-on-write query-builder clone (F6-9 profiling lever)

`Query.clone()` no longer deep-copies all ten builder slices on every chained
method; it shares them and the builder appends through a capacity-bounded
`ownedAppend` that reallocates only the slice actually mutated. Immutability is
unchanged. A "fat base" derive drops to 1 alloc/op.

## Tooling

### Stress / load harness (F6-9)

`benchmarks/stress` is a runnable concurrency harness reporting latency
percentiles, throughput, and connection-pool contention. Its documented run
identifies the first bottleneck under load (pool sizing, then engine write
serialization — not Quark's mapping). See
[`docs/benchmarks/stress/`](https://github.com/jcsvwinston/quark/tree/main/docs/benchmarks/stress).

## Known limitations

- Read-replica routing covers multi-row reads; single-row `First`/`Find`/`Count`
  still use the primary (they share an execution path with `INSERT … RETURNING`).
- Replica failover recovery is passive (no active health-check goroutine): a
  recovered replica rejoins on the first retry after its cooldown.
- Still late-alpha — not production-ready until a v1.0 honest release.

[#107]: https://github.com/jcsvwinston/quark/pull/107
[#109]: https://github.com/jcsvwinston/quark/pull/109
[#110]: https://github.com/jcsvwinston/quark/pull/110
[#113]: https://github.com/jcsvwinston/quark/pull/113
