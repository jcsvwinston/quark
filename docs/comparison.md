# Quark vs Other Go ORMs — Detailed Comparison

This page justifies every cell in the README comparison table with code examples and precise reasoning. The goal is not to disparage other projects but to articulate clearly where the trade-offs lie.

---

## 1. Native Generics (no `interface{}`)

**Claim:** Quark avoids `interface{}` in its core public API; GORM v2 does not.

### Quark

```go
// The return type is []User — no assertion needed.
users, err := quark.For[User](ctx, client).Where("active", "=", true).List()
// users is []User at compile time.
```

### GORM v2

```go
var users []User
// db.Find takes an interface{} destination — the compiler cannot verify the type.
db.Where("active = ?", true).Find(&users)
```

GORM v2's `Find`, `First`, `Create`, and `Save` all accept `interface{}`. Generic wrappers exist in community packages but are not part of the core API.

**Verdict:** Quark `✅` — GORM `partial` (core API uses interface{}, generics are add-on).

---

## 2. SQL Injection Guard

**Claim:** Quark validates identifiers (column names, table names, operators) at the API layer before query construction. GORM and ent protect *values* via prepared statements but do not validate identifiers.

### Quark — identifier guard

```go
// "drop_table" is not a recognized operator — blocked before any SQL is built.
_, err := quark.For[User](ctx, client).Where("name", "drop_table", "x").List()
// err: ErrInvalidQuery: operator "drop_table" not allowed

// Injecting a column name from user input:
col := r.URL.Query().Get("sort_by") // attacker sends "1; DROP TABLE users--"
_, err = quark.For[User](ctx, client).Where(col, "=", "value").List()
// err: ErrInvalidQuery: column "1; DROP TABLE users--" not in allowlist
```

### GORM — value protection only

```go
// Values are parameterized (safe):
db.Where("name = ?", userInput).Find(&users) // safe

// But identifier injection is not guarded:
col := r.URL.Query().Get("sort_by") // attacker sends "1=1 OR 1"
db.Where(col+" = ?", "value").Find(&users) // potentially unsafe — col is interpolated
db.Order(col).Find(&users)                 // unsafe if col is user-controlled
```

GORM and ent protect query *values* through parameterized queries, which is the standard defense against classic SQL injection. Quark additionally guards *identifiers* — a less common but real attack surface when column/table names originate from user-controlled input.

**Verdict:** Both approaches prevent the most common injection vectors. Quark adds a second layer specific to identifier injection. Neither approach replaces application-level input validation.

---

## 3. 6 Dialects, Zero Config Switch

**Claim:** Switching databases in Quark requires changing one line; no query code changes.

### Quark

```go
// SQLite
client, _ := quark.New(db, quark.WithDialect(quark.SQLite()))

// PostgreSQL — one line changes, all queries identical
client, _ = quark.New(db, quark.WithDialect(quark.PostgreSQL()))
```

Quark supports: SQLite, PostgreSQL, MySQL, MariaDB, MSSQL, Oracle.

### GORM

GORM also supports multiple dialects via separate driver packages, and the query API is mostly portable. `partial` in the table refers to the fact that some GORM features (e.g., `Upsert`, JSON queries) require dialect-specific code.

### sqlx

sqlx is a thin wrapper around `database/sql` with no dialect abstraction. SQL strings must be written per-dialect manually.

---

## 4. Native Multi-Tenancy

**Claim:** Quark has a built-in `TenantRouter` supporting three isolation strategies. Other ORMs require manual implementation.

### Quark

```go
router := quark.NewTenantRouter(cfg, func(ctx context.Context) string {
    return ctx.Value("tenant_id").(string)
}, nil)

// All queries are automatically isolated — no per-query code:
users, _ := quark.For[User](tenantCtx, router).List()
```

### GORM equivalent (manual)

```go
// Schema-per-tenant: must set search_path on every connection/session manually.
db.Exec("SET search_path TO " + tenantID) // must be done on every acquired connection

// RLS: must inject WHERE on every query manually.
db.Where("tenant_id = ?", tenantID).Find(&users)
// Forgetting this line leaks cross-tenant data — no enforcement.
```

**Verdict:** Quark enforces isolation at the routing layer; leaking data across tenants requires explicitly bypassing the router. GORM/sqlx/ent require discipline at the call site.

---

## 5. Immutable Query Builder

**Claim:** Every Quark builder method returns a clone; the original is never mutated.

### Quark

```go
base := quark.For[User](ctx, client).Where("active", "=", true)

// Safe to use base concurrently — each branch is independent:
admins, _ := base.Where("role", "=", "admin").List()
editors, _ := base.Where("role", "=", "editor").List()
// base is unchanged.
```

### GORM (mutable)

```go
base := db.Where("active = ?", true)

// base is mutated by chained calls:
admins := base.Where("role = ?", "admin") // mutates base
// editors would now also have the admin condition — shared state bug.
```

GORM's `Session(&gorm.Session{NewDB: true})` can mitigate this, but it is opt-in, not the default.

---

## 6. Integrated L2 Cache

**Claim:** Quark has a built-in cache abstraction with tag-based invalidation wired into the query lifecycle.

### Quark

```go
store := memory.New()
client, _ := quark.New(db, quark.WithDialect(quark.PostgreSQL()), quark.WithCacheStore(store))

users, _ := quark.For[User](ctx, client).Cache(5*time.Minute, "users").List()
store.InvalidateTags(ctx, "users") // invalidate after a write
```

### GORM

GORM has no built-in cache. Third-party packages (e.g., `go-gorm/cache`) exist but require separate wiring.

---

## 7. OpenTelemetry Built-In

**Claim:** Quark ships an OTel middleware that requires no query code changes.

### Quark

```go
import quarkotel "github.com/jcsvwinston/quark/otel"

client, _ := quark.New(db,
    quark.WithDialect(quark.PostgreSQL()),
    quark.WithMiddleware(quarkotel.New()),
)
// Every query now emits spans — no further changes needed.
```

### GORM

`otelgorm` is a community plugin. Setup requires registering the plugin on the DB instance and is maintained separately from GORM core.

---

## 8. Batch Operations

**Claim:** Quark provides chunked `DeleteBatch`, dialect-optimal `UpsertBatch`, and atomic `UpdateBatch`.

### Quark

```go
// Chunked DELETE with IN clauses respecting dialect limits (e.g., Oracle 1000-item limit):
affected, err := quark.For[User](ctx, client).DeleteBatch([]int64{1, 2, 3, 1000})

// Multi-row INSERT ... ON CONFLICT for PostgreSQL/MySQL; MERGE for MSSQL/Oracle:
err = quark.For[User](ctx, client).UpsertBatch(users, []string{"email"}, []string{"name"})

// N partial updates in a single transaction:
affected, err = quark.For[User](ctx, client).UpdateBatch(users)
```

### GORM

`CreateInBatches` exists for inserts. Batch DELETE and batch UPDATE require custom loops with manual transaction management.

### ent

`CreateBulk` supports batch inserts. Batch delete/update require manual loops or raw queries.
