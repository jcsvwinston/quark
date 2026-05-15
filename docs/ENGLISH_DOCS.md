# Quark ORM — English Documentation

Quark is a modern, lightweight, fully type-safe ORM for Go built on Generics.  
It provides a fluent, immutable query builder, multi-dialect support, and a rich feature set without the overhead of continuous reflection.

---

## Quick Start

```go
import (
    _ "modernc.org/sqlite"
    "github.com/jcsvwinston/quark"
)

client, _ := quark.New("sqlite", ":memory:")
defer client.Close()
```

---

## Model Definition

```go
type User struct {
    ID        int64     `db:"id"         pk:"true"`
    Name      string    `db:"name"       quark:"not_null"`
    Email     string    `db:"email"      quark:"unique"`
    Active    bool      `db:"active"     default:"1"`
    CreatedAt time.Time `db:"created_at"`
    DeletedAt *time.Time `db:"deleted_at"` // enables soft-delete
}
```

### Field Tags

| Tag                  | Description                                         |
|----------------------|-----------------------------------------------------|
| `db:"col"`           | Maps the field to a SQL column named `col`          |
| `pk:"true"`          | Marks the field as (part of) the primary key        |
| `quark:"not_null"`   | Emits `NOT NULL` in `CREATE TABLE` DDL              |
| `nullable:"false"`   | Alias for `not_null`                                |
| `default:"val"`      | Emits `DEFAULT val` in `CREATE TABLE` DDL           |
| `quark:"unique"`     | Emits `UNIQUE` in `CREATE TABLE` DDL                |
| `quark:"rename:old"` | Renames column `old` to the current `db` name on `Sync` |
| `validate:"rule"`    | go-validator v10 rules applied before Create/Update |

---

## CRUD Operations

### Create
```go
user := User{Name: "Alice", Email: "alice@example.com"}
err := quark.For[User](ctx, client).Create(&user)
// user.ID is populated after insert
```

### Find / First / List
```go
user, err  := quark.For[User](ctx, client).Find(1)
first, err := quark.For[User](ctx, client).Where("active", "=", true).First()
users, err := quark.For[User](ctx, client).Limit(20).List()
```

### Update (Partial)

> `Update()` is a **partial update** — it only writes fields whose value is non-zero.  
> Use `UpdateMap` to explicitly set fields to zero values.

```go
user.Name = "Bob"
rows, err := quark.For[User](ctx, client).Update(&user)
// SQL: UPDATE users SET name = 'Bob' WHERE id = 1
```

```go
rows, err := quark.For[User](ctx, client).
    Where("id", "=", 1).
    UpdateMap(map[string]any{"active": false})
```

### Delete / Soft Delete
```go
// Hard delete (always removes the row)
rows, err := quark.For[User](ctx, client).HardDelete(&user)

// Soft delete (sets deleted_at = NOW() when the column is present)
rows, err := quark.For[User](ctx, client).Delete(&user)

// Restore soft-deleted records
rows, err := quark.For[User](ctx, client).Unscoped().Where("id", "=", 1).Restore()
```

### Upsert (INSERT … ON CONFLICT)
```go
u := User{Email: "alice@example.com", Name: "Alice Updated"}
err := quark.For[User](ctx, client).Upsert(&u,
    []string{"email"},          // conflict columns
    []string{"name", "active"}, // columns to update on conflict
)
```

Dialect mapping:

| Dialect  | Generated SQL fragment                                  |
|----------|---------------------------------------------------------|
| Postgres | `ON CONFLICT (col) DO UPDATE SET …`                     |
| MySQL    | `ON DUPLICATE KEY UPDATE col = VALUES(col)`             |
| MariaDB  | `ON DUPLICATE KEY UPDATE col = VALUES(col)`             |
| SQLite   | `ON CONFLICT (col) DO UPDATE SET col = excluded.col`    |
| MSSQL    | Full `MERGE INTO … USING … ON … WHEN MATCHED …` statement |
| Oracle   | Full `MERGE INTO … USING … ON … WHEN MATCHED …` statement |

### CreateBatch (Bulk Insert)
```go
users := []*User{
    {Name: "Alice", Email: "a@example.com"},
    {Name: "Bob",   Email: "b@example.com"},
}
err := quark.For[User](ctx, client).CreateBatch(users)
// Single INSERT INTO users (...) VALUES (...), (...)
// PKs are populated when the dialect supports RETURNING
```

### DeleteBatch (Bulk Delete by Primary Key)

Deletes multiple records in one or more `DELETE … WHERE pk IN (…)` statements.
Automatically chunks to 1000 IDs per statement to respect Oracle's IN-list limit.

```go
affected, err := quark.For[User](ctx, client).DeleteBatch([]any{1, 2, 3, 4})
// DELETE FROM users WHERE id IN (?, ?, ?, ?)
```

### UpsertBatch (Bulk Upsert)

Inserts or updates multiple records in a single batch operation.

```go
users := []*User{
    {Name: "Alice", Email: "alice@example.com", Score: 99},
    {Name: "Bob",   Email: "bob@example.com",   Score: 88},
}
err := quark.For[User](ctx, client).UpsertBatch(
    users,
    []string{"email"},          // conflict detection column(s)
    []string{"name", "score"},  // columns to update on conflict (empty = all)
)
```

Dialect strategies:

| Dialect  | Strategy |
|----------|----------|
| Postgres / SQLite | Multi-row `INSERT … ON CONFLICT (col) DO UPDATE SET …` |
| MySQL / MariaDB   | Multi-row `INSERT … ON DUPLICATE KEY UPDATE …` |
| MSSQL             | Single `MERGE … USING (VALUES …) AS src(…)` |
| Oracle            | N individual `MERGE` statements (IDENTITY column restriction) |

### UpdateBatch (Bulk Update in Transaction)

Updates multiple records by primary key within a single atomic transaction.
Each entity is partially updated — zero-value fields are skipped (same semantics as `Update`).

```go
for _, u := range users {
    u.Score += 100
}
err := quark.For[User](ctx, client).UpdateBatch(users)
// Wraps N individual UPDATE statements in a transaction; rolls back all on error
```

---

## Query Builder

The query builder is **immutable** — every method returns a new `*Query[T]` clone.

### Where / WhereIn / WhereBetween / Or
```go
q := quark.For[User](ctx, client).
    Where("active", "=", true).
    Where("age", ">", 18).
    WhereIn("role", []any{"admin", "editor"}).
    WhereBetween("created_at", start, end)

users, _ := q.Or(func(oq *quark.Query[User]) *quark.Query[User] {
    return oq.Where("name", "=", "John")
}).List()
```

### WhereNot
```go
// WHERE NOT (active = true)
users, _ := quark.For[User](ctx, client).WhereNot("active", "=", true).List()
```

### WhereSubquery
```go
// WHERE id IN (SELECT user_id FROM orders WHERE total > 100)
users, _ := quark.For[User](ctx, client).
    WhereSubquery("id", "IN", "SELECT user_id FROM orders WHERE total > 100").
    List()
```

### Distinct
```go
users, _ := quark.For[User](ctx, client).Select("name").Distinct().List()
```

### GroupBy / Having
```go
results, _ := quark.For[Employee](ctx, client).
    Select("department").
    GroupBy("department").
    Having("salary", ">", 50000).
    List()
```

### Aggregate Functions
```go
total, _ := quark.For[Order](ctx, client).Sum("amount")
avg,   _ := quark.For[Order](ctx, client).Avg("amount")
min,   _ := quark.For[Order](ctx, client).Min("amount")
max,   _ := quark.For[Order](ctx, client).Max("amount")

// With WHERE filter
userTotal, _ := quark.For[Order](ctx, client).
    Where("user_id", "=", 42).Sum("amount")
```

### Scopes (Reusable Query Fragments)
```go
// Define once
activeOnly := quark.Scope[User](func(q *quark.Query[User]) *quark.Query[User] {
    return q.Where("active", "=", true)
})
adults := quark.Scope[User](func(q *quark.Query[User]) *quark.Query[User] {
    return q.Where("age", ">=", 18)
})

// Compose at call site
users, _ := quark.For[User](ctx, client).Apply(activeOnly, adults).List()
```

### Count / Paginate
```go
count, _ := quark.For[User](ctx, client).Where("active", "=", true).Count()

page, _ := quark.For[User](ctx, client).OrderBy("id", "ASC").Paginate(20, 0)
// page.Items []User, page.Total int64, page.TotalPages int
```

---

## Streaming Large Datasets

```go
// Iter — constant memory footprint
err := quark.For[User](ctx, client).Iter(func(u User) error {
    process(u)
    return nil
})

// Cursor — manual row-by-row
cursor, _ := quark.For[User](ctx, client).Cursor()
defer cursor.Close()
for cursor.Next() {
    var u User
    cursor.Scan(&u)
}
```

---

## Relations & Eager Loading

Quark resolves associations in a single extra DB round-trip (no N+1).

```go
type User struct {
    ID    int64   `db:"id" pk:"true"`
    Posts []Post  `rel:"has_many"  join:"user_id"`
    Addr  Address `rel:"has_one"   join:"user_id"`
    Team  Team    `rel:"belongs_to" join:"team_id"`
    Tags  []Tag   `rel:"many_to_many" m2m:"user_tags:user_id:tag_id"`
}

users, _ := quark.For[User](ctx, client).Preload("Posts", "Addr", "Tags").List()
```

### Polymorphic Relations
```go
type Comment struct {
    ID         int64  `db:"id"          pk:"true"`
    Body       string `db:"body"`
    PolyableID int64  `db:"polyable_id"`
    PolyType   string `db:"poly_type"`
}

type Post struct {
    ID       int64     `db:"id"    pk:"true"`
    Title    string    `db:"title"`
    Comments []Comment `rel:"polymorphic" polymorphic:"poly_type:post" join:"polyable_id"`
}

posts, _ := quark.For[Post](ctx, client).Preload("Comments").List()
```

---

## Migrations

### Auto-Migrate (Create Tables)
```go
err := client.Migrate(ctx, &User{}, &Order{})
```

### Auto-Sync (Schema Evolution)
```go
// Adds new columns; renames with quark:"rename:old_col"
err := client.Sync(ctx, &User{})
```

### CreateIndex
```go
// CREATE [UNIQUE] INDEX IF NOT EXISTS name ON table (cols)
err := client.CreateIndex(ctx, "users", "idx_users_email", []string{"email"}, true)
```

### AddForeignKey
```go
// ALTER TABLE child ADD CONSTRAINT name FOREIGN KEY (col) REFERENCES parent (col) ON DELETE action
err := client.AddForeignKey(ctx, "orders", "fk_orders_user",
    []string{"user_id"}, "users", []string{"id"}, "CASCADE", "")
```

---

## Transactions

```go
// Callback style (recommended — auto commit/rollback)
err := client.Tx(ctx, func(tx *quark.Tx) error {
    u := User{Name: "Charlie"}
    return quark.ForTx[User](ctx, tx).Create(&u)
})

// Manual style
tx, _ := client.BeginTx(ctx, nil)
defer tx.Rollback()
quark.ForTx[User](ctx, tx).Create(&u)
tx.Commit()
```

---

## Middleware, Hooks & Observers

### Lifecycle Hooks
```go
func (u *User) BeforeCreate(ctx context.Context) error {
    u.CreatedAt = time.Now()
    return nil
}
```

Available: `BeforeCreate`, `AfterCreate`, `BeforeUpdate`, `AfterUpdate`, `BeforeDelete`, `AfterDelete`.

### Middleware
```go
type LogMiddleware struct{ quark.BaseMiddleware }

func (m *LogMiddleware) WrapExec(next quark.ExecFunc) quark.ExecFunc {
    return func(ctx context.Context, exec quark.Executor, sql string, args []any) (sql.Result, error) {
        log.Printf("exec: %s", sql)
        return next(ctx, exec, sql, args)
    }
}

client, _ := quark.New("sqlite", ":memory:", quark.WithMiddleware(&LogMiddleware{}))
```

### Query Observers
```go
type MetricsObserver struct{}
func (o *MetricsObserver) ObserveQuery(e quark.QueryEvent) {
    metrics.Record(e.SQL, e.Duration)
}
client, _ := quark.New("sqlite", ":memory:", quark.WithQueryObserver(&MetricsObserver{}))
```

---

## Caching

```go
import "github.com/jcsvwinston/quark/cache/memory"

store := memory.New()
client, _ := quark.New("sqlite", ":memory:",
    quark.WithCacheStore(store),
)

// Cache result for 5 minutes, tagged "users"
users, _ := quark.For[User](ctx, client).
    Cache(5*time.Minute, "users").
    List()

// Invalidate by tag
store.InvalidateTags(ctx, "users")
```

Redis cache is available in `cache/redis`.

---

## Multi-Tenant

```go
cfg := quark.DefaultTenantConfig()
cfg.Strategy = quark.RowLevelSecurityClient
cfg.BaseClient = client

router := quark.NewTenantRouter(cfg, func(ctx context.Context) string {
    return ctx.Value("tenant_id").(string)
}, nil)

// Automatically injects WHERE tenant_id = 'acme' on all queries
users, _ := quark.For[User](ctx, router).List()
```

---

## OpenTelemetry

```go
import "github.com/jcsvwinston/quark/otel"

client, _ := quark.New("sqlite", ":memory:",
    quark.WithMiddleware(otel.New()),
)
```

---

## Supported Dialects

| Dialect  | Constructor       | Notes                          |
|----------|-------------------|--------------------------------|
| PostgreSQL | `quark.PostgreSQL()` | SERIAL PK, RETURNING, $N placeholders |
| MySQL    | `quark.MySQL()`   | AUTO_INCREMENT, last_insert_id |
| MariaDB  | `quark.MariaDB()` | ON DUPLICATE KEY UPDATE        |
| SQLite   | `quark.SQLite()`  | AUTOINCREMENT, RETURNING       |
| MSSQL    | `quark.MSSQL()`   | IDENTITY, MERGE for upsert     |
| Oracle   | `quark.Oracle()`  | GENERATED AS IDENTITY, MERGE   |
