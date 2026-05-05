# Quark ORM — Performance Benchmarks

Benchmarks for Quark across all supported database engines. Results were collected against local Docker instances (except SQLite, which uses in-memory mode).

---

## Environment

| | |
|---|---|
| **Hardware** | Apple M-series (ARM64), 16 GB RAM |
| **OS** | macOS 15 |
| **Go** | 1.25.7 |
| **Quark** | v0.1.0 |
| **Docker Desktop** | 4.x (PostgreSQL 16, MySQL 8, MSSQL 2022, Oracle 21c) |
| **SQLite** | modernc.org/sqlite v1.23 (pure Go, in-memory) |

---

## How to Reproduce

### Full engine benchmark (observer-based, 10 000 inserts / 5 000 updates / 5 000 deletes)

```bash
# SQLite only (no external dependencies):
go test -run TestBenchmarkEngines -v -timeout 5m

# All engines (set DSN env vars first):
export QUARK_TEST_POSTGRES_DSN="postgres://quark:quark@localhost:5432/quark_test?sslmode=disable"
export QUARK_TEST_MYSQL_DSN="quark:quark@tcp(localhost:3306)/quark_test?parseTime=true"
export QUARK_TEST_MSSQL_DSN="sqlserver://quark:Quark1234!@localhost:1433?database=quark_test"
export QUARK_TEST_ORACLE_DSN="oracle://quark:quark@localhost:1521/ORCLPDB1"

go test -run TestBenchmarkEngines -v -timeout 15m
```

The test prints per-operation timing and a summary at the end.

---

## Results — Quark Internal (avg µs/op over 10 000+ operations)

| Engine | Operation | Avg (µs/op) | Total (N ops) |
|:---|:---|---:|---:|
| **SQLite (in-memory)** | INSERT | 6.12 | 10 000 |
| | UPDATE | 3.24 | 5 000 |
| | DELETE | 1.92 | 5 000 |
| **PostgreSQL** | INSERT | 198.55 | 10 000 |
| | UPDATE | 129.76 | 5 000 |
| | DELETE | 128.53 | 5 000 |
| **MySQL** | INSERT | 979.43 | 10 000 |
| | UPDATE | 275.57 | 5 000 |
| | DELETE | 266.18 | 5 000 |
| **MSSQL** | INSERT | 651.50 | 10 000 |
| | UPDATE | 266.88 | 5 000 |
| | DELETE | 265.41 | 5 000 |
| **Oracle** | INSERT | 431.73 | 10 000 |
| | UPDATE | 271.94 | 5 000 |
| | DELETE | 269.34 | 5 000 |

> Times include round-trip to a local Docker container for all engines except SQLite. Network latency to Docker on the same machine accounts for the majority of the per-op cost for PostgreSQL and above.

---

## Cross-ORM Comparison

> **Honest note:** The numbers below are *not* from a shared controlled benchmark harness. A fair apples-to-apples comparison requires the same hardware, the same database instance, the same schema, and the same query patterns measured in the same `go test -bench` run. That benchmark is planned for v0.2 (tracked in the roadmap). The table below is based on published benchmarks from each project's documentation and community reports.

| | Quark | GORM v2 | ent | sqlx |
|:---|:---:|:---:|:---:|:---:|
| **Reflection approach** | cached, one-time | per-call (heavy) | code-gen (none) | manual scan |
| **ORM overhead (SQLite insert, est.)** | ~6 µs | ~40–80 µs | ~10–20 µs | ~3–8 µs |
| **Allocations per insert (est.)** | low | high | very low | very low |
| **Memory stability (10 k ops)** | stable | stable | stable | stable |

**Analysis:**
- **SQLite performance** is close to raw `database/sql` thanks to Quark's one-time reflection cache. After the first query for a given model, struct metadata is never re-computed.
- **PostgreSQL** at ~198 µs/op is dominated by Docker round-trip latency (~1 ms on loopback). Relative ORM overhead is a small fraction of that.
- **GORM overhead** is higher primarily because it re-inspects struct tags on every query chain. Quark caches this work in a `sync.Map` on first use.
- **ent and sqlx** are faster in raw throughput because ent avoids reflection entirely (code generation) and sqlx shifts the mapping work to the caller. If maximum throughput is the only priority and you are willing to accept code generation or manual scanning, those tools are strong choices. Quark optimizes for the balance between safety, ergonomics, and performance.

---

## Planned: `go test -bench` Reproducible Suite

A `benchmarks/comparison/` package with proper `func BenchmarkXxx(b *testing.B)` functions comparing Quark, GORM, ent, sqlx, and bun on:

- Single-row insert
- Batch insert (1 000 rows)
- Simple SELECT with WHERE
- SELECT with JOIN
- Eager loading (Preload equivalent)

Will be published in v0.2. Contributions welcome — see [CONTRIBUTING.md](../CONTRIBUTING.md).

---

*Last updated: 2026-05-05 · Environment: Mac ARM64, Docker Desktop, Go 1.25.7*
