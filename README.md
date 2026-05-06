<div align="center">

<img src="https://raw.githubusercontent.com/jcsvwinston/quark/main/docs/assets/quark-logo.png" alt="Quark ORM" width="160" />

# Quark

**The type-safe, security-first ORM for Go — built on generics, built to production standards.**

[![Go Reference](https://pkg.go.dev/badge/github.com/jcsvwinston/quark.svg)](https://pkg.go.dev/github.com/jcsvwinston/quark)
[![CI](https://github.com/jcsvwinston/quark/actions/workflows/ci.yml/badge.svg)](https://github.com/jcsvwinston/quark/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/go-1.21%2B-00ADD8?logo=go)](https://go.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Coverage](https://img.shields.io/badge/coverage-87%25-brightgreen)](docs/benchmarks.md)
[![Release](https://img.shields.io/github/v/release/jcsvwinston/quark)](https://github.com/jcsvwinston/quark/releases/latest)

[Docs](docs/ENGLISH_DOCS.md) · [Quick Start](#-quick-start) · [Examples](examples/) · [CLI](#️-cli) · [Changelog](docs/RELEASE_NOTES_V1.md)

</div>

---

## 📌 Status

Quark is **v0.x** — production-grade design with an API that may evolve before v1.0. The core query builder, CRUD operations, and migration engine are considered stable. Breaking changes will be documented in the [changelog](docs/RELEASE_NOTES_V1.md) with a migration path.

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

See the [blog-api example](examples/blog-api/) for a full end-to-end REST API with migrations, tests, and curl examples.

---

## 🎬 Demo

> **Recording coming soon.** To preview Quark locally right now:
>
> ```bash
> git clone https://github.com/jcsvwinston/quark
> go run ./examples/blog-api
> curl -s -X POST http://localhost:8080/authors \
>   -H "Content-Type: application/json" \
>   -d '{"name":"Alice","email":"alice@example.com"}' | jq .
> curl -s "http://localhost:8080/posts" | jq .
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
cfg.Strategy  = quark.DatabasePerTenant  // or SchemaPerTenant, RowLevelSecurity
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
