# Profiling: where Quark's per-operation cost lives

This is the follow-up to the F6-8a baseline (Quark runs ~1.5–2.1× the
hand-written `database/sql` floor) and the F6-2/F6-3a codegen findings (scan
codegen ~2–5%, insert-binder codegen ~1%). Those raised the ADR-0002 question
directly: **is reflection actually Quark's bottleneck?** This profiles the
read and write paths to answer it.

## Method

In-memory SQLite (the same harness as `README.md`), Go 1.26, Apple M4 Pro.

```bash
cd benchmarks
go test -run=^$ -bench='BenchmarkQuark_ListWhere$' -benchtime=3s -cpuprofile=cpu.prof -memprofile=mem.prof .
go tool pprof -top cpu.prof
go tool pprof -top -sample_index=alloc_space mem.prof
```

`ListWhere` (50 rows) for the read path, `InsertOne` for the write path.
The numbers below are one representative run; reproduce locally.

## CPU — engine and driver bound, reflection invisible

For `ListWhere`, the CPU profile is dominated by the SQLite engine and the
`database/sql` plumbing:

| Bucket | ~% of CPU |
| --- | --- |
| `syscall.Syscall` / `rawsyscalln` (modernc SQLite engine, mmap) | ~67% |
| `database/sql.(*Rows).Next` / `.Close` (cum) | ~52% |
| `database/sql.(*DB).QueryContext` (cum) | ~16% |
| Quark `scanRow` / reflection | **not in the top 25 nodes** |

Quark's reflection-based scan does not register as a CPU cost — the time is
spent inside the database engine and the standard library's row machinery.
Removing reflection cannot move CPU it does not consume.

## Allocations — architectural, not reflection

CPU is not where Quark differs from raw; **allocations** are (ListWhere: Quark
~474 allocs/op vs raw ~365; InsertOne: Quark ~68). The alloc profile shows
where they come from.

**Read path (`ListWhere`, `alloc_space`):**

| Source | flat % | note |
| --- | --- | --- |
| `Query.List.func1` (result collection loop) | ~36% | growing the result slice + per-row work |
| `Query.scanRow` | ~14% | the `[]any` scan-target slice + per-field boxing |
| `Query.clone` | ~7% | the immutable builder — each `Where`/`OrderBy`/`Limit` clones `BaseQuery` |
| `SQLGuard.ValidateOperator`, `For[T]`, `Where`, `OrderBy`, `buildSelect` | ~10% combined | query construction |

**Write path (`InsertOne`, `alloc_space`):**

| Source | flat % | note |
| --- | --- | --- |
| `For[T]` | ~19% | query construction |
| `BaseQuery.saveAny` | ~19% | write orchestration (association scan, sub-query setup) |
| `BaseQuery.buildInsert` | ~12% | column/placeholder/SQL string building |
| `rowToMap` | ~9% | **audit/event diff computed unconditionally — even with no audit log or event bus configured** |
| `SQLiteDialect.Quote`, `Returning`, `executeQueryRow` | ~7% combined | dialect + exec |

## What this means for codegen (ADR-0002 gate)

The ADR-0002 gate asks codegen to show **≥3× p99** to justify itself. The
profile says that is **not reachable by removing reflection from scan/bind**:

1. **CPU isn't in reflection** — it's in the engine and `database/sql`.
   Codegen has nothing to recover there.
2. **The allocations codegen can target are a minority and aren't eliminated.**
   `scanRow` is ~14% of read allocs; the generated scanner (F6-2) removed the
   reflect *lookup* but still allocates the `[]any` and boxes each field. The
   insert binder (F6-3a) likewise builds the same column/arg slices.
3. **The dominant allocators are architectural, not reflective**: result
   collection, the immutable-clone builder, query-string building, and
   `saveAny` orchestration. None of these are reflection that codegen removes.

This matches the measured ~1–5% codegen gains and confirms ADR-0002's own
*reopen* condition ("if benchmarks show reflect is no longer the bottleneck,
re-evaluate Phase 6").

## Recommendation

- **Do not pursue codegen for speed.** The mechanism (F6-1/F6-2/F6-3a) is
  correct and a fine foundation, but scan/bind codegen will not approach the
  ≥3× gate. Either revise the gate or stop treating speed as codegen's
  justification.
- **Reframe codegen's value as type-safety** (F6-4: compile-time column
  accessors). That value is real and independent of the perf gate.
- **If per-operation cost is a goal, the levers are allocation reduction, not
  codegen** — and they are independent of it:
  - `rowToMap` is computed on every `Create` even when audit/events are off;
    make it lazy (only when a sink is configured). Easy, ~9% of write allocs.
  - The immutable-clone builder allocates per builder call; consider a lazy
    or pooled clone for hot query construction.
  - `scanRow`/`buildInsert` allocate a fresh `[]any` per call; a reused buffer
    would help bulk reads/writes.
  Note that even these are bounded: against a networked database the driver
  round-trip dwarfs all of it, and even in-memory the engine dominates CPU.
