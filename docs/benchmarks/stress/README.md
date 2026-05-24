# Stress / load testing (F6-9)

The micro-benchmarks in [`benchmarks/`](../../../benchmarks/) measure one
operation at a time, single-connection, to isolate per-call overhead. This
harness does the opposite: it drives **many concurrent workers** against a
Quark `Client` for a fixed duration and reports the behaviour that only shows
up under load — latency percentiles, throughput, connection-pool contention,
and lock/contention errors. The goal is to find the **first real bottleneck**
under concurrency so post-1.0 optimisation effort is spent where it matters.

## Running it

The harness is a runnable command in the benchmarks module:

```bash
cd benchmarks

# Default: in-memory SQLite, 16 workers, 8-connection pool, 20% writes, 5s.
go run ./stress

# Match the pool to the worker count:
go run ./stress -conns 16 -workers 16

# Point it at a real engine (any driver Quark supports):
go run ./stress -driver pgx -dsn "postgres://user:pass@localhost/db?sslmode=disable" \
  -conns 16 -workers 64 -duration 30s -write-pct 30
```

Flags: `-driver`, `-dsn`, `-conns` (pool size), `-workers`, `-duration`,
`-write-pct` (0–100), `-seed` (rows preloaded). The default SQLite DSN sets
`busy_timeout` so write contention surfaces as latency rather than immediate
`SQLITE_BUSY` errors.

## Documented run

Apple M4 Pro, Go 1.26, `modernc.org/sqlite` in-memory (shared cache), seed
1000 rows, 5s (3s for the variants). **Absolute numbers are machine- and
engine-specific; the ratios and the pool-wait signal are the takeaway, not the
microseconds.**

| Config (conns/workers, writes) | ops/sec | read p50 | read p99 | write p50 | write p99 | pool waitCount | avg wait/block |
|---|--:|--:|--:|--:|--:|--:|--:|
| 8 / 16, 20% (default)  | 32,478 | 281µs | 1.56ms | 696µs | 4.03ms | 162,393 (≈ every op) | 247µs |
| 16 / 16, 20%           | 31,381 | **64µs** | 1.09ms | 1.25ms | **9.84ms** | **0** | — |
| 8 / 16, 0% (read-only) | 15,046 | 829µs | 3.74ms | — | — | 45,146 (≈ every op) | 532µs |
| 8 / 16, 50%            | 43,425 | 232µs | 1.09ms | 356µs | 1.68ms | 130,275 (≈ every op) | 184µs |

## The first bottleneck: connection-pool sizing

When `MaxOpenConns < workers`, **nearly every operation blocks waiting for a
pool connection** (`waitCount` ≈ total ops), and that wait (250–530µs) is on
the same order as the read latency itself — it dominates. Matching the pool to
the offered concurrency (row 2) drops `waitCount` to **0** and read p50 from
281µs to **64µs** (~4.4×). This is a deployment/config property, not a Quark
code path: the fix is `quark.WithMaxOpenConns(n)` sized to expected concurrency.

## The second bottleneck: engine write serialization

Once the pool is large enough, the limiter becomes the **engine**. With a
matched 16-connection pool, reads stay fast (64µs p50) but write tail latency
balloons (p99 ~10ms, max ~20ms): SQLite has a single writer, so concurrent
`UPDATE`s serialize (the `busy_timeout` turns the contention into wait). This
is a property of SQLite, not of Quark's mapping — a server engine
(PostgreSQL/MySQL) with row-level locking will not serialize writes the same
way. Run the harness against one to characterise it there.

## Takeaway

Under load the cost is **pool acquisition and the engine**, not Quark's
reflect-based mapping — the same conclusion the per-operation profiling reached
([`benchmarks/PROFILING.md`](../../../benchmarks/PROFILING.md)) and the basis
for the ADR-0002 gate decision to stop chasing codegen-for-speed. The
actionable, post-1.0 priorities this surfaces:

1. **Document pool-sizing guidance** prominently (size `MaxOpenConns` to
   concurrency); consider a saner default than the driver's.
2. Optimisation of the per-op mapping path has bounded value until the pool and
   engine costs above are addressed — confirmed by data, not assumed.
