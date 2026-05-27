<div align="center">

<img src="https://raw.githubusercontent.com/jcsvwinston/quark/main/docs/assets/quark-logo.png" alt="Quark ORM" width="160" />

# Quark

**A type-safe, security-first ORM for Go — generics on the surface, six dialects underneath.**

[![Go Reference](https://pkg.go.dev/badge/github.com/jcsvwinston/quark.svg)](https://pkg.go.dev/github.com/jcsvwinston/quark)
[![CI](https://github.com/jcsvwinston/quark/actions/workflows/ci.yml/badge.svg)](https://github.com/jcsvwinston/quark/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/go-1.21%2B-00ADD8?logo=go)](https://go.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/jcsvwinston/quark)](https://github.com/jcsvwinston/quark/releases/latest)

[Docs](https://jcsvwinston.github.io/quark/) · [Quick Start](#-quick-start) · [Examples](examples/) · [CLI](#️-cli) · [Changelog](CHANGELOG.md)

</div>

---

## 📌 Status

Quark is **v1.0.0** — the first stable release under SemVer. Phases 0–6 are complete: the core query builder, CRUD, schema-as-code migrations across all six dialects (Oracle now in blocking CI), multi-tenancy, caché, hooks/events/audit log, observability, opt-in code generation, read replicas with failover, and pluggable sharding. `v1.x` keeps API compatibility; breaking changes go to `v2.x` with a `docs/MIGRATION_v2.0.0.md`. v1.0 was gated on the qualitative checklist in [`docs/V1_GATE.md`](docs/V1_GATE.md) (cross-engine coverage, structural gaps closed or consciously waived), not on a performance target — [ADR-0017](docs/adr/0017-codegen-type-safety-not-perf-gate.md) retired the ≥3× codegen performance gate, so code generation is a type-safety feature, not a speedup. Known limitations consciously deferred to v1.1+ are listed in [`docs/RELEASE_NOTES_v1.0.0.md`](docs/RELEASE_NOTES_v1.0.0.md).

**v0.13** (Phase 6 HA cut): opt-in **read replicas** (`WithReplicas`) with read/write split, read-your-writes (`Sticky`), and automatic **failover** to the primary on transient replica errors (with a configurable replica cooldown, ADR-0015); a copy-on-write query-builder clone that drops a "fat base" derive to 1 alloc/op; and a runnable stress/load harness in `benchmarks/stress`. No breaking changes.

**v0.12** (Phase 6 sliver): opt-in **compile-time column type-safety** on top of the code generator — `quark gen` emits per-model `<Model>Columns` accessors and the query builder gains `WhereP`, so typos and wrong-typed values fail at build time. Pure compile-time sugar (ADR-0014): the string `Where(...)` API stays valid and interchangeable. Performance fix: audit row diff is now built only when an audit sink is configured (~9% allocation drop on `InsertOne`). No breaking changes.

**v0.11** (Phase 6 cut): opt-in **code generation** — `quark gen` parses your model package with `go/packages`/`go/types` and emits a `quark_gen.go` per package that registers typed scanners (read path) and a typed INSERT binder (single-integer-PK models) into the runtime registry. The reflection path stays the permanent default (ADR-0002). Honest profiling finding in `benchmarks/PROFILING.md`: codegen ~2–5% scan, ~1% INSERT — Quark's per-op CPU is dominated by `database/sql` and the engine, not reflection. **Generate for correctness and forward compatibility, not for speed.** Reproducible benchmark harness in `benchmarks/` (database/sql baseline + GORM + Quark). No breaking changes.

**v0.10** (correctness): `JSON[T]` / `Array[T]` round-trip on SQL Server (they were corrupted by a `[]byte`→VARBINARY→NVARCHAR conversion); savepoint rollback discards `After*`/`OnCommit`/`OnRollback` hooks queued in that scope so undone work no longer fires its side-effects on the outer commit; a real cross-engine deadlock-retry integration test (PG/MySQL/MariaDB) backs `WithDeadlockRetry`; raw SQL under `RowLevelSecurityNative` emits a `quark.tenant.raw_under_native_rls` warning. No breaking changes.

**v0.9** (Phase 5 cut — multi-tenancy + transactional hooks + events + audit): PostgreSQL `RowLevelSecurityNative` delegates tenant isolation to the database via `set_config('app.tenant_id', …)` + `CREATE POLICY` (the `quarktenant install-rls-policies` CLI generates the DDL); `After*` hooks now fire **post-commit** under `Client.Tx` and there are new `BeforeFind`/`AfterFind` hooks; `Tx.OnCommit`/`Tx.OnRollback` + `quark.TxFromContext` expose commit/rollback side-effects; a real `EventBus` (`Client.UseEventBus`) publishes `created`/`updated`/`deleted` events; `Client.EnableAuditLog` records an immutable change trail in `quark_audit`, written atomically with each write. Two **breaking-minor** changes — see [`docs/MIGRATION_v0.9.0.md`](docs/MIGRATION_v0.9.0.md).

**v0.8** (Phase 4 cut — observability + caché de producción): OTel **metrics** (counter + duration/rows histograms) join the existing tracing spans; `WithSpanRedaction` keeps bind values off spans by default; `WithSlowQueryThreshold` emits structured slow-query WARNs; the cache backing is wrapped with `stampedeStore` — singleflight + ±jitter + XFetch (ADR-0011); cache invalidation is per-row (`<table>:<pk>`) on top of the table tag, with the Redis tag-TTL fix (NX + GT MAX); `WithDeadlockRetry` re-runs the closure on PG 40P01 / MySQL 1213 / MSSQL 1205 / Oracle ORA-00060 with exponential backoff + jitter.

**v0.7**: per-column timezones (`quark:"tz=Europe/Madrid"` or Client-wide `WithDefaultTZ`), UTC-always wire contract.

**v0.6** (Phase 3 cut — schema-as-code migrations): neutral schema introspection across the four CI dialects + SQLite, pure-Go schema diff, `Client.PlanMigration` / `ApplyPlan` round-trip, transactional + resumable execution, `quarkmigrate` plan/verify/apply CLI, orchestrated `Backfill`, per-Client model registry, distributed migration lock, `Array[T]`.

**v0.5** (Phase 0 cleanup): the cross-engine integration matrix (PostgreSQL, MySQL, MariaDB, MSSQL via testcontainers; **Oracle excluded pending an image issue**) is **blocking** in CI.

**v0.4** (Phase 2 — composable query builder): typed expression AST, subqueries, CTEs, window functions, set operators, pessimistic locking, structured Join builder, nested-Preload dotted paths, IN(...) chunking, `HavingAggregate`.

**v0.3** (Phase 1 — rich types + dirty tracking): `Nullable[T]`, `JSON[T]`, `RegisterTypeMapper`, optimistic locking, soft-delete scopes.

Breaking changes are documented in `docs/MIGRATION_vX.Y.Z.md` per version ([`MIGRATION_v0.9.0.md`](docs/MIGRATION_v0.9.0.md) covers the two breaking-minor changes in v0.9; none for v1.0, v0.13, v0.12, v0.11, v0.10, v0.8, v0.7, v0.6 or v0.5; [`MIGRATION_v0.4.0.md`](docs/MIGRATION_v0.4.0.md) covers the Join builder rename from v0.3.x). Release notes per version live under [`docs/RELEASE_NOTES_*.md`](docs/).

---

## 🏗️ Why I built this

After running production services on GORM, three patterns kept causing incidents: every `db.Find(&result)` forced an `interface{}` cast the compiler couldn't verify; column names in `WHERE` clauses were plain strings with no guard against typos or injection in dynamic queries; N+1 queries appeared silently whenever a `Preload` was forgotten, only surfacing in slow-query logs hours later; and multi-tenant isolation meant copy-pasting `WHERE tenant_id = ?` everywhere, relying on discipline instead of enforcement. Quark is the ORM I wished existed: generics end the casts, `SQLGuard` validates every identifier at the API boundary, eager loading is explicit, and multi-tenancy is first-class — not an afterthought.

---

## 🚀 Quick Start

```bash
go get github.com/jcsvwinston/quark
```

```go
package main

import (
    "context"
    "log"

    "github.com/jcsvwinston/quark"
    _ "modernc.org/sqlite"
)

type User struct {
    ID    int64  `db:"id"    pk:"true"`
    Name  string `db:"name"  quark:"not_null"`
    Email string `db:"email" quark:"unique"`
    Age   int    `db:"age"`
}

func main() {
    client, err := quark.New("sqlite", "file:app.db?cache=shared")
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    ctx := context.Background()

    // Create the table
    client.Migrate(ctx, &User{})

    // Insert
    u := User{Name: "Alice", Email: "alice@example.com", Age: 30}
    quark.For[User](ctx, client).Create(&u)
    // u.ID is now set

    // Query
    users, _ := quark.For[User](ctx, client).
        Where("age", ">=", 18).
        OrderBy("name", "ASC").
        Limit(20).
        List()

    // Update (partial — only non-zero fields) → returns (rowsAffected int64, err error)
    u.Name = "Alice Smith"
    rows, err := quark.For[User](ctx, client).Update(&u)
    _, _ = rows, err

    // Delete
    _, _ = quark.For[User](ctx, client).HardDelete(&u)

    _ = users
}
```

**Switch to PostgreSQL** — change one line, zero query code changes:

```go
client, _ = quark.New("postgres", "postgres://user:pass@localhost/db")
```

See the per-dialect runnable examples under [`examples/`](examples/) (one folder per supported engine).

---

## 🎬 Demo

> **Recording coming soon.** To preview Quark locally right now:
>
> ```bash
> git clone https://github.com/jcsvwinston/quark
> go run ./examples/sqlite
> ```

---

## Why Quark?

Most Go ORMs make you choose between safety and ergonomics. Quark doesn't.

| | Quark | GORM | sqlx | ent |
|---|:---:|:---:|:---:|:---:|
| Native Generics (no `interface{}`) | ✅ | partial¹ | ❌ | ✅ |
| SQL Injection Guard | identifier + value | value only² | manual | value only² |
| 6 Dialects, zero config switch | ✅ | ✅ | ❌ | partial |
| Native Multi-Tenant (DB/Schema/RLS) | ✅ | manual/plugin | manual | manual/interceptor |
| Immutable Query Builder | ✅ | mutable³ | N/A | ✅ |
| Integrated L2 Cache | ✅ | plugin | ❌ | ❌ |
| `stdlib` `*sql.DB` — no magic pool | ✅ | ✅ | ✅ | ❌ |
| OpenTelemetry built-in | ✅ | plugin | ❌ | plugin |
| Batch Ops (Delete/Upsert/Update) | ✅ | partial⁴ | ❌ | partial |

> ¹ GORM v2 core API uses `interface{}`; generic wrappers exist but are not part of the primary API.  
> ² GORM and ent use parameterized queries that protect *values* against injection. Quark additionally validates *identifiers* (column/table names) at the API layer. See [docs/comparison.md](docs/comparison.md) for a detailed breakdown with code examples.  
> ³ GORM queries can mutate shared state when chained; `Session(&gorm.Session{NewDB: true})` mitigates this but is opt-in.  
> ⁴ GORM supports `CreateInBatches`; batch DELETE and batch UPDATE require custom loops.

For a cell-by-cell justification with code examples, see **[docs/comparison.md](docs/comparison.md)**.

---

## ✨ Features

- **100% Type-Safe** — Go Generics end `interface{}` casts and silent runtime errors forever
- **SQLGuard** — Every identifier (column, table, operator) is validated before touching the wire
- **Immutable Builder** — Clone-on-write query builder, safe for concurrent goroutines
- **6 Dialects** — PostgreSQL · MySQL · MariaDB · SQLite · MSSQL · Oracle, all with idiomatic SQL generation
- **Native Multi-Tenancy** — Database-per-tenant, schema-per-tenant, and Row-Level Security out of the box
- **L2 Cache** — Pluggable cache backend (in-memory, Redis) wired directly into the query lifecycle
- **OpenTelemetry** — Distributed tracing and metrics without changing your query code
- **Batch Operations** — Chunked `DeleteBatch`, dialect-optimal `UpsertBatch`, atomic `UpdateBatch`
- **Eager Loading** — Single-query `Preload()` eliminates N+1 queries
- **Auto-Migrations & Sync** — `Migrate()` creates tables; `Sync()` evolves them, including column renames
- **Hooks & Middleware** — Full lifecycle hooks (`BeforeCreate`, `AfterDelete`…) and stackable middleware
- **Versioned Migrations** — Code-first migration files with Up/Down and dry-run support
- **Composite PKs** — First-class support for multi-column primary keys across all dialects
- **Streaming** — `Iter()`, `Cursor()`, and `Paginate()` prevent OOM on large datasets
- **CLI** — `quark model generate`, `quark migrate up`, `quark inspect schema` and more

---

## 🔒 SQLGuard — Security by Default

Quark refuses to build a query with an unknown column, operator, or identifier:

```go
// Compile-time + runtime guard: "drop_table" is not a valid operator
quark.For[User](ctx, client).Where("name", "drop_table", "x").List()
// → ErrInvalidQuery: operator "drop_table" not allowed

// Raw subqueries require explicit opt-in
quark.For[User](ctx, client).WhereSubquery("id", "IN", rawSQL).List()
// → ErrInvalidQuery: WhereSubquery requires AllowRawQueries to be enabled
```

Enable raw queries only where you deliberately need them:

```go
lims := quark.DefaultLimits()
lims.AllowRawQueries = true
client, _ = quark.New("postgres", "postgres://user:pass@localhost/db", quark.WithLimits(lims))
```

---

## 📖 Core Operations

### CRUD

```go
// Create
err := quark.For[User](ctx, client).Create(&user)

// Find by PK
user, err := quark.For[User](ctx, client).Find(1)

// Update (partial — zero-value fields are skipped)
user.Name = "Bob"
rows, err := quark.For[User](ctx, client).Update(&user)

// UpdateMap (force any value, including zero)
rows, err := quark.For[User](ctx, client).
    Where("id", "=", user.ID).
    UpdateMap(map[string]any{"active": false, "score": 0})

// Upsert (INSERT … ON CONFLICT) — all 6 dialects
err = quark.For[User](ctx, client).Upsert(&user, []string{"email"}, []string{"name", "age"})

// Soft delete (sets deleted_at) or hard delete
rows, err = quark.For[User](ctx, client).Delete(&user)
rows, err = quark.For[User](ctx, client).HardDelete(&user)
rows, err = quark.For[User](ctx, client).Where("active", "=", false).DeleteBy()
```

### Batch Operations

```go
// DeleteBatch — chunked IN clauses, respects dialect limits
affected, err := quark.For[User](ctx, client).DeleteBatch([]int64{1, 2, 3, 100})

// UpsertBatch — dialect-optimal (multi-row ON CONFLICT / bulk MERGE / individual MERGE)
err = quark.For[User](ctx, client).UpsertBatch(users, []string{"email"}, []string{"name", "age"})

// UpdateBatch — N partial updates in a single transaction
affected, err = quark.For[User](ctx, client).UpdateBatch(users)
```

### Query Builder

```go
users, err := quark.For[User](ctx, client).
    Select("id", "name", "email").
    Where("active", "=", true).
    Where("age", ">", 18).
    WhereIn("role", []any{"admin", "editor"}).
    WhereBetween("created_at", start, end).
    WhereNot("banned", "=", true).
    Or(func(q *quark.Query[User]) *quark.Query[User] {
        return q.Where("tier", "=", "vip")
    }).
    OrderBy("created_at", "DESC").
    Limit(50).Offset(100).
    List()

count, err := quark.For[User](ctx, client).Where("active", "=", true).Count()
total, err := quark.For[Order](ctx, client).Sum("amount")
```

### Transactions & Savepoints

```go
err := client.Tx(ctx, func(tx *quark.Tx) error {
    if err := quark.ForTx[User](ctx, tx).Create(&u); err != nil {
        return err // triggers ROLLBACK
    }
    tx.Savepoint("checkpoint")
    // nested savepoint — partial rollback possible
    return nil // triggers COMMIT
})
```

---

## 🏢 Multi-Tenancy

```go
cfg := quark.DefaultTenantConfig()
cfg.Strategy  = quark.DatabasePerTenant  // or SchemaPerTenant, RowLevelSecurityClient
cfg.BaseClient = adminClient

router := quark.NewTenantRouter(cfg,
    func(ctx context.Context) string {
        return ctx.Value("tenant_id").(string)
    }, nil)

// Queries are automatically routed & isolated — no code changes
users, _ := quark.For[User](tenantCtx, router).List()
```

---

## 📦 Caching (L2)

```go
import "github.com/jcsvwinston/quark/cache/memory"

store := memory.New()
client, _ := quark.New("postgres", "postgres://user:pass@localhost/db",
    quark.WithCacheStore(store),
)

// Cache for 5 minutes, tagged "users"
users, _ := quark.For[User](ctx, client).
    Cache(5*time.Minute, "users").
    List()

// Invalidate the tag (e.g. after a write)
store.InvalidateTags(ctx, "users")
```

Redis backend: `github.com/jcsvwinston/quark/cache/redis`

---

## 🔭 OpenTelemetry

```go
import quarkotel "github.com/jcsvwinston/quark/otel"

client, _ := quark.New("postgres", "postgres://user:pass@localhost/db",
    quark.WithMiddleware(quarkotel.New()),
)
// Every query now emits spans and metrics to your configured OTEL exporter
```

---

## 🗄 Migrations

### Auto-Migrate & Sync

```go
// Create table if not exists
client.Migrate(ctx, &User{}, &Order{})

// Evolve schema: add columns, rename with quark:"rename:old_col", drop with safe=false
client.Sync(ctx, &User{})
```

### Versioned Migrations

```go
// migrations/20240101_create_users.go
migrate.Register(&migrate.Migration{
    ID: "20240101_create_users",
    Up: func(ctx context.Context, client *quark.Client) error {
        return client.Exec(ctx, `CREATE TABLE users (...)`)
    },
    Down: func(ctx context.Context, client *quark.Client) error {
        return client.Exec(ctx, `DROP TABLE users`)
    },
})
```

```bash
quark migrate up
quark migrate down --steps 1
quark migrate status
```

---

## 🛠️ CLI

Install:
```bash
go install github.com/jcsvwinston/quark/cmd/quark@latest
```

| Command | Description |
|---------|-------------|
| `quark init` | Scaffold a new project with `.quark.yml` config |
| `quark model generate --from-table users` | Generate Go structs from live database tables |
| `quark migrate create add_index` | Create a new versioned migration file |
| `quark migrate up` | Apply pending migrations |
| `quark migrate down --steps 1` | Revert the last migration |
| `quark migrate status` | Show applied / pending migrations |
| `quark inspect schema` | Print full database schema |
| `quark inspect table users` | Inspect a specific table |
| `quark validate --table users` | Validate column ↔ struct mapping |
| `quark seed run` | Execute registered seeders |
| `quark tenant provision acme` | Provision a new tenant |
| `quark tenant migrate-all` | Run migrations across all tenants |

---

## 📐 Project Structure

```
github.com/jcsvwinston/quark
├── *.go                  Core ORM (client, query builder, CRUD, dialect)
├── cache/
│   ├── memory/           In-memory L2 cache
│   └── redis/            Redis L2 cache
├── migrate/              Versioned migration engine
├── otel/                 OpenTelemetry middleware
├── internal/             Private implementation (guard, schema, introspection)
├── cmd/
│   └── quark/            CLI tool
├── examples/             Runnable examples per dialect
└── docs/                 Architecture, API reference, multi-tenancy guide
```

---

## ⚙️ Configuration Reference

```go
client, err := quark.New("postgres", "postgres://user:pass@localhost/db",
    quark.WithLimits(quark.Limits{
        MaxResults:         10_000,          // hard cap on List() results
        MaxWhereConditions: 20,              // prevent runaway WHERE chains
        MaxJoins:           5,
        QueryTimeout:       30 * time.Second,
        AllowRawQueries:    false,           // explicit opt-in for raw SQL
        SafeMigrations:     true,            // block DROP COLUMN by default
    }),
    quark.WithCacheStore(store),             // L2 cache backend
    quark.WithMiddleware(myMiddleware),      // stackable middleware
    quark.WithQueryObserver(myObserver),     // query logging / metrics
)
```

---

## 🤝 Contributing

Pull requests are welcome. For major changes, please open an issue first.

```bash
git clone https://github.com/jcsvwinston/quark
cd quark
go test ./...           # all unit + integration tests (SQLite runs offline)
```

External engine tests require env vars:
```
QUARK_TEST_POSTGRES_DSN=postgres://...
QUARK_TEST_MYSQL_DSN=user:pass@tcp(...)/db?parseTime=true
QUARK_TEST_MSSQL_DSN=sqlserver://...
QUARK_TEST_ORACLE_DSN=oracle://...
```

---

## 📄 License

Apache 2.0 — see [LICENSE](LICENSE)
