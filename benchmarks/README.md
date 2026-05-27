# Quark benchmark harness

A reproducible `go test -bench` harness that measures Quark's per-operation
overhead against a hand-written `database/sql` baseline and against GORM, on
the same model, schema, data, and operations.

This is the F6-8 deliverable: a real harness with documented methodology,
not estimated numbers. It exists to give an honest, pre-codegen baseline so
the generated-code work (F6-2/F6-3) could be measured against the
[ADR-0002](../docs/adr/0002-reflect-default-codegen-fase-6.md) gate. That
gate (≥3× p99 with codegen) has since been **retired** by
[ADR-0017](../docs/adr/0017-codegen-type-safety-not-perf-gate.md): the
baseline + profiling below showed reflect is not the bottleneck, so codegen
is justified by type-safety (F6-4), not speed.

## Why this is a separate module

The comparison dependencies (GORM, the pure-Go SQLite driver) must not leak
into the library's `go.mod`. So this is its own module
(`github.com/jcsvwinston/quark/benchmarks`) that pulls the library in
through a local `replace` directive. Nothing here ships to library
consumers.

## Why separate test binaries

Quark's core links `modernc.org/sqlite`, and GORM's pure-Go driver
(`glebarez/go-sqlite`, itself a fork of modernc) both register the
`database/sql` driver name `sqlite`. Importing both into one binary panics
with `Register called twice`. So each implementation that links a driver
lives in its own package — Quark/raw in the root, GORM in `./gorm`, ent in
`./ent`, sqlc in `./sqlc` — i.e. four test binaries. `go test ./...` builds
each independently, so no binary sees another's driver. ent and sqlc both
link `modernc.org/sqlite`, the same engine lineage as Quark/raw, so the
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

Each operation is implemented five ways against the same `bench_users`
table:

- **Raw** — hand-written `database/sql` with manual `Scan`/`Exec`. The
  performance floor; the target the generated path aims to approach.
- **Quark** — the public `quark.For[T]` API on the current reflect path.
- **GORM** — the reflect-ORM peer.
- **ent** (`./ent`) — a code-generation ORM: a typed client generated from a
  schema, with a rich runtime (builders, mutations, hooks).
- **sqlc** (`./sqlc`) — a code generator that turns annotated SQL into thin
  typed wrappers over `database/sql`, with no runtime of its own.

ent and sqlc are the code-generation tier (F6-8b) — the same tier Quark's own
generated scanners/binders (F6-2/F6-3, shipped v0.11.0) belong to.

A few deliberate per-API choices to be aware of when reading the
allocation numbers:

- **Update** uses `gorm.Save(&u)` (a PK-keyed full-row UPDATE, no SELECT)
  to match Quark's `Update(&u)` and the raw `UPDATE … WHERE id`. `Save`
  still runs GORM's update-callback machinery even with no hooks
  registered, so part of GORM's Update cost is that fixed overhead. ent's
  `UpdateOneID(...).Save` runs its full mutation pipeline, which is why its
  Update is the heaviest of the five.
- **InsertBatch** passes `[]*BenchUser` to Quark (its `CreateBatch`
  contract takes pointers) and `[]BenchUser` to GORM. The pointer slice
  adds heap escapes, so some of Quark's batch `allocs/op` is the slice
  shape, not reflection. **sqlc has no variadic multi-row `INSERT` for
  SQLite** (its `:copyfrom`/`:batch` helpers are pgx-only), so its
  `InsertBatch` is a transaction-wrapped loop of single-row inserts — a real
  API asymmetry vs the multi-row `VALUES` batch the other four use.

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

# Everything (all four binaries):
go test -run=^$ -bench=. -benchmem ./...

# Just one implementation:
go test -run=^$ -bench=Quark -benchmem .
go test -run=^$ -bench=GORM  -benchmem ./gorm
go test -run=^$ -bench=Ent   -benchmem ./ent
go test -run=^$ -bench=Sqlc  -benchmem ./sqlc

# Compare one operation across implementations:
go test -run=^$ -bench=InsertOne -benchmem ./...

# Stabilise for comparison (recommended before publishing numbers; the
# representative run below used -count=6):
go test -run=^$ -bench=. -benchmem -count=6 ./... | tee bench.txt
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
`gorm.io/gorm v1.31.0`, `entgo.io/ent v0.14.6`, `sqlc v1.31.1`. The benchmark
module's `go.mod` requires Go 1.25; this run used the go1.26.0 toolchain (the
`go.mod` line is a minimum, not the version used, so reproducing on a
different toolchain may shift the numbers). Medians of
`-bench=. -benchmem -count=6`, summarized with `benchstat`:

Time per operation (ns/op):

| Operation     |     Raw |   Quark |    GORM |     ent |    sqlc |
| ------------- | ------: | ------: | ------: | ------: | ------: |
| InsertOne     |   6,572 |  12,940 |  19,120 |  13,080 |   6,009 |
| InsertBatch   | 175,300 | 263,600 | 265,500 | 302,300 | 279,000 |
| FindByPK      |   7,864 |  14,140 |  10,400 |  11,750 |   7,544 |
| ListWhere(50) |  33,900 |  66,540 |  54,360 |  45,330 |  35,770 |
| Update        |   2,851 |   4,611 |   8,327 |  21,000 |   3,014 |

Allocations per operation (allocs/op):

| Operation     | Raw | Quark |  GORM |   ent | sqlc |
| ------------- | --: | ----: | ----: | ----: | ---: |
| InsertOne     |  20 |    61 |    78 |    77 |   21 |
| InsertBatch   | 622 | 1,277 | 1,287 | 3,278 | 2,307 |
| FindByPK      |  24 |    65 |    66 |   100 |   25 |
| ListWhere(50) | 365 |   468 |   705 |   756 |  374 |
| Update        |  15 |    55 |    84 |   143 |   18 |

Reading of this run:

- **Codegen alone does not put you at the floor — the absence of a runtime
  does.** sqlc sits right on the raw `database/sql` floor (~1.0–1.1×) because
  its generated code is thin wrappers with no runtime. ent is also
  code-generated but carries a rich runtime (builders, mutations); it lands in
  the reflect class on writes (its `Update` is the heaviest of the five, its
  `InsertBatch` allocates the most). So the spread tracks **runtime and
  allocation design, not reflect-vs-codegen.**
- Quark, GORM, and ent are in the same performance class; none dominates.
  Quark is faster than GORM on inserts and updates, GORM/ent are faster on the
  single-row read and the filtered list. Only sqlc is consistently faster, and
  it trades ergonomics for that (no batch helper on SQLite, hand-written SQL,
  no model lifecycle).
- This confirms why Quark's own generated path (F6-2/F6-3) was reframed: it
  recovers only ~1–5% over the reflect baseline (`PROFILING.md`) because the
  cost is architectural allocation + the driver round-trip, not reflection —
  the same reason ent (codegen + a runtime) stays in the reflect class. The
  ADR-0002 ≥3× p99 gate was retired (ADR-0017); codegen is justified by
  type-safety (F6-4), not speed. Per-op figures have run-to-run noise (a few
  ±10–25%); treat the relative ratios as the signal.

## The code-generation tier (ent, sqlc) — F6-8b

ent and sqlc are codegen tools, so each ships its generated code committed:

- **ent** (`./ent`) — schema in `./ent/schema`, generated client in `./ent`.
  Regenerate with `go generate ./ent/...` (needs the `entgo.io/ent/cmd/ent`
  tool, kept in `go.mod` via the `tool` directive).
- **sqlc** (`./sqlc`) — `schema.sql` + `query.sql` + `sqlc.yaml`, generated
  package in `./sqlc/sqlcdb`. Regenerate with `sqlc generate` from `./sqlc`
  (needs the `sqlc` binary: `go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest`).
  The generated code only imports `database/sql`, so it adds no module
  dependency of its own.

Each is its own subpackage importing `internal/model` but not the Quark core,
mirroring `./gorm`, under the same driver-isolation constraint. This is the
codegen-tier comparison against Quark's own generated path (F6-2/F6-3); the
≥3× gate it once fed has been retired (ADR-0017), so it is informational, not
a v1.0 blocker.
