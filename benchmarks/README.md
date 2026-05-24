# Quark benchmark harness

A reproducible `go test -bench` harness that measures Quark's per-operation
overhead against a hand-written `database/sql` baseline and against GORM, on
the same model, schema, data, and operations.

This is the F6-8 deliverable: a real harness with documented methodology,
not estimated numbers. It exists to give an honest, pre-codegen baseline so
the generated-code work (F6-2/F6-3) can be measured against the
[ADR-0002](../docs/adr/0002-reflect-default-codegen-fase-6.md) gate.

## Why this is a separate module

The comparison dependencies (GORM, the pure-Go SQLite driver) must not leak
into the library's `go.mod`. So this is its own module
(`github.com/jcsvwinston/quark/benchmarks`) that pulls the library in
through a local `replace` directive. Nothing here ships to library
consumers.

## Why two test binaries

Quark's core links `modernc.org/sqlite`, and GORM's pure-Go driver
(`glebarez/go-sqlite`, itself a fork of modernc) both register the
`database/sql` driver name `sqlite`. Importing both into one binary panics
with `Register called twice`. So the Quark/raw benchmarks live in the root
package and the GORM benchmarks live in `./gorm` — two packages, two test
binaries. `go test ./...` builds each independently, so neither sees the
other's driver. Both run on the same modernc engine lineage, so the
comparison stays fair.

The shared model and fixtures live in `internal/model`, which imports no ORM
and no driver, so both binaries can reuse it.

## What is measured

Five operations, chosen because they exercise the exact reflect hot paths
(`scanRow`, `buildInsert`, `buildUpdate`) that codegen will replace:

| Benchmark     | Operation                                            |
| ------------- | ---------------------------------------------------- |
| `InsertOne`   | Insert a single row                                  |
| `InsertBatch` | Insert 100 rows in one batch                         |
| `FindByPK`    | Select one row by primary key                        |
| `ListWhere`   | Select up to 50 rows with a `WHERE age >= ?` filter  |
| `Update`      | Update one row (all non-PK columns) by primary key   |

Each operation is implemented three ways against the same `bench_users`
table:

- **Raw** — hand-written `database/sql` with manual `Scan`/`Exec`. The
  performance floor; the target the generated path aims to approach.
- **Quark** — the public `quark.For[T]` API on the current reflect path.
- **GORM** — the reflect-ORM peer.

A few deliberate per-API choices to be aware of when reading the
allocation numbers:

- **Update** uses `gorm.Save(&u)` (a PK-keyed full-row UPDATE, no SELECT)
  to match Quark's `Update(&u)` and the raw `UPDATE … WHERE id`. `Save`
  still runs GORM's update-callback machinery even with no hooks
  registered, so part of GORM's Update cost is that fixed overhead.
- **InsertBatch** passes `[]*BenchUser` to Quark (its `CreateBatch`
  contract takes pointers) and `[]BenchUser` to GORM. The pointer slice
  adds heap escapes, so some of Quark's batch `allocs/op` is the slice
  shape, not reflection.

## Methodology and its limits

- **In-memory SQLite** (`mode=memory&cache=shared`) is used so the
  measurement isolates ORM/driver CPU and allocation overhead rather than
  disk or network I/O. This is the right lens for the codegen question:
  codegen removes reflection cost, not I/O. **Against a networked database,
  ORM overhead is a small fraction of round-trip latency** — do not read
  these microseconds as production request times.
- Each implementation uses the same five-column schema and the same
  deterministic row data. Each ORM migrates the table its own idiomatic way;
  the resulting schema is functionally identical.
- Connections are capped at one (`SetMaxOpenConns(1)`) so the shared-cache
  in-memory database stays alive and the benchmarks are single-threaded and
  deterministic. Concurrency/contention is out of scope here — that is F6-9
  (stress/load).
- Numbers are **machine- and run-specific**. Treat the relative ratios as
  the signal, not the absolute nanoseconds. Reproduce locally before
  drawing conclusions.

## How to run

```bash
cd benchmarks

# Everything (both binaries):
go test -run=^$ -bench=. -benchmem ./...

# Just one implementation:
go test -run=^$ -bench=Quark -benchmem .
go test -run=^$ -bench=GORM  -benchmem ./gorm

# Compare one operation across implementations:
go test -run=^$ -bench=InsertOne -benchmem ./...

# Stabilise for comparison (recommended before publishing numbers):
go test -run=^$ -bench=. -benchmem -count=10 ./... | tee bench.txt
go run golang.org/x/perf/cmd/benchstat@latest bench.txt
```

## Concurrency / stress (F6-9)

The micro-benchmarks above are single-connection, one operation at a time.
For behaviour under concurrent load — latency percentiles, connection-pool
contention, write serialization — use the stress harness:

```bash
go run ./stress                  # in-memory SQLite, 16 workers, 8-conn pool
go run ./stress -conns 16 -workers 16 -write-pct 30 -duration 30s
```

Methodology and a documented run (including the first bottleneck it surfaces —
pool sizing, then engine write serialization) live in
[`docs/benchmarks/stress/README.md`](../docs/benchmarks/stress/README.md).

## A representative run

Apple M4 Pro, macOS, in-memory SQLite, `modernc.org/sqlite v1.23.1`,
`gorm.io/gorm v1.31.0`. The benchmark module's `go.mod` requires Go 1.25;
this run used the go1.26.0 toolchain (the `go.mod` line is a minimum, not
the version used, so reproducing on a different toolchain may shift the
numbers). One `-bench=. -benchmem` run:

| Operation     | Raw ns/op | Quark ns/op | GORM ns/op | Raw allocs | Quark allocs | GORM allocs |
| ------------- | --------: | ----------: | ---------: | ---------: | -----------: | ----------: |
| InsertOne     |     6,038 |      12,293 |     21,212 |         20 |           62 |          78 |
| InsertBatch   |   170,776 |     258,973 |    270,601 |        622 |        1,277 |       1,287 |
| FindByPK      |     7,311 |      15,541 |     11,002 |         24 |           65 |          66 |
| ListWhere(50) |    38,604 |      71,172 |     50,134 |        365 |          474 |         705 |
| Update        |     3,059 |       4,963 |      9,132 |         15 |           62 |          84 |

Reading of this run:

- Quark's reflect path runs ~1.5–2.1× the hand-written `database/sql` floor.
  That gap is the headroom the generated path (F6-2/F6-3) can recover — it
  also bounds it, since generated code cannot beat hand-written SQL. This is
  a direct input to the ADR-0002 v1.0 gate (which asks codegen to justify
  itself with a ≥3× p99 improvement): on these single-row in-memory
  operations the measured headroom to the floor is closer to 2×, so the gate
  is most likely to be met (if at all) on heavier paths — wide rows, large
  result sets where per-row reflection dominates, or under the concurrency
  that F6-9 will exercise.
- Quark and GORM are in the same performance class; neither dominates. Quark
  is faster on inserts and updates here, GORM is faster on the single-row
  read and the filtered list.

## Adding a competitor (ent, sqlc, …) — F6-8b

ent and sqlc are codegen tools: each needs its generated code committed
(an ent schema + `ent generate`, or a `sqlc.yaml` + `sqlc generate`). They
are the codegen-tier comparison that matters once Quark itself has a
generated path to compare (the v1.0 gate), and they carry the same
driver-isolation constraint as GORM. Add each as its own subpackage
(`./ent`, `./sqlc`) that imports `internal/model` but not the Quark core,
mirroring `./gorm`. Tracked as F6-8b in `../TASKS.md`.
