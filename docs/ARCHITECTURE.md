# Quark ORM Architecture

Quark is an enterprise-grade, generic-based Object-Relational Mapper for Go. It is designed around safety, immutability, and modularity.

## Core Design Principles

1. **Type Safety via Generics**: Quark leverages Go 1.18+ generics to provide a completely type-safe API. `For[T]` returns a `*Query[T]` specific to a struct model, eliminating the need for generic `interface{}` returns and assertions.
2. **Immutable Query Building**: Every method on `Query[T]` that modifies the query state (e.g., `.Where()`, `.Limit()`, `.Preload()`) returns a cloned instance of the builder. This prevents state contamination and ensures thread-safe query building.
3. **Database Independence**: The `Dialect` interface hides database-specific logic (like parameter placeholders or identifier quoting). Quark natively supports SQLite, PostgreSQL, MySQL, Microsoft SQL Server, and Oracle. Custom dialects can be registered via `RegisterDialect()`.
4. **Modularity and Injection**: Quark allows dependency injection of custom loggers, `QueryObserver` functions (for telemetry/metrics), and a modular Middleware chain for Hooks (`BeforeCreate`, `AfterDelete`, etc.) and Validation.

## Request Lifecycle

The standard lifecycle of a Quark database operation follows this path:

1. **Initialization (`quark.For[T]`)**: Parses struct metadata (cached via `sync.Map`), sets up the model details, and initializes an immutable `Query[T]` builder.
2. **State Construction**: The developer chains builder methods. Each method creates a clone of the `Query` state.
3. **Execution Endpoint**: A method like `.List()` or `.Create()` is invoked.
4. **Validation (Writes)**: Write endpoints intercept the model to validate using struct tags (`validate:"required"`) or a custom `Validatable` interface.
5. **Middleware & Hooks**: Operations pass through a middleware pipeline. Lifecycle hooks (`BeforeCreate`, `AfterUpdate`, etc.) are triggered before and after the core database execution.
6. **SQL Generation**: The query state is translated to dialect-specific SQL safely using `SQLGuard` to prevent SQL Injection on identifiers.
7. **Execution & Mapping**: The SQL is executed via `database/sql`, and results are reflected back into the generic struct models. Preloads are resolved efficiently using secondary `IN` queries.

## Multi-Tenant Architecture

Quark supports building multi-tenant applications efficiently without leaking connections or data, via the `TenantRouter`.

Quark abstracts three isolation strategies:
- **Database-per-Tenant**: Highest isolation. The router maintains an LRU cache of independent `*sql.DB` connection pools for each tenant to prevent connection exhaustion. Oldest connections are safely evicted via `db.Close()`.
- **Schema-per-Tenant**: Logical isolation on a single database. The router dynamically injects the `tenant_id` as a schema prefix in SQL generation (e.g., `SELECT * FROM tenant_acme.users`), heavily reducing infrastructure overhead.
- **Row-Level-Security (RLS)**: Simplest isolation. The router transparently injects a `WHERE tenant_id = ?` clause into every query via the `Query[T]` builder initialization.

## Native Execution (Procedures, Functions, and Events)

While `Query[T]` handles relational operations, Quark isolates the execution of raw database functions, stored procedures, and event streams.

1. **`Routine[T]`**: A builder similar to `Query[T]` designed to call Table-Valued Functions or scalar SQL functions. The `Dialect` implementation ensures proper translation (`SELECT * FROM func()` in Postgres vs `CALL proc()` in MySQL).
2. **`Call`**: For pure logic stored procedures (that mutate data or use `sql.Out` parameters), `quark.Call()` issues direct execution statements.
3. **`EventBus`**: An abstraction for database-native pub/sub events (e.g., PostgreSQL `LISTEN`/`NOTIFY`).

## Advanced Relations

Quark supports complex relation types beyond standard has_one, has_many, and belongs_to.

### Many-to-Many (M2M)

Many-to-many relations are defined using a join table to link two models:

```go
type User struct {
    ID    int64   `db:"id" pk:"true"`
    Roles []Role  `rel:"m2m" m2m:"user_roles:user_id:role_id"`
}

type Role struct {
    ID   int64  `db:"id" pk:"true"`
    Name string `db:"name"`
}
```

The `m2m` tag specifies the join table name and optionally the foreign key columns. If omitted, Quark auto-generates them based on model names.

### Polymorphic Relations

Polymorphic associations allow a model to belong to multiple other models through a single association:

```go
type Comment struct {
    ID       int64  `db:"id" pk:"true"`
    Content  string `db:"content"`
    EntityID int64  `db:"entity_id"`  // The parent ID
    EntityType string `db:"entity_type"` // The parent type identifier
}

type User struct {
    ID       int64     `db:"id" pk:"true"`
    Comments []Comment `rel:"polymorphic" polymorphic:"entity_type:users"`
}
```

The `polymorphic` tag specifies the type identifier stored in the type column. Quark uses this to filter related records during eager loading.

## Custom Dialect Registration

Quark supports custom database dialects via the `RegisterDialect` API, enabling integration with proprietary or non-standard databases:

```go
// Define custom dialect
myDialect := &MyCustomDialect{}

// Register it
quark.RegisterDialect("customdb", myDialect)

// Use it
client, err := quark.New("customdb", "customdb://user:pass@localhost/db")
```

The `Dialect` interface includes methods for SQL generation, identifier quoting, placeholder formatting, and DDL operations (ALTER TABLE).

## Recursive Association Persistence

Quark v1.0 introduces the ability to save complex object graphs in a single operation. When calling `.Create()` or `.Update()`, Quark orchestrates the persistence order:

1. **Belongs-To Dependencies**: Persisted *before* the main entity to obtain their primary keys.
2. **Main Entity**: Persisted next, with foreign keys from dependencies injected.
3. **Has-One / Has-Many**: Persisted *after* the main entity, with its primary key injected as the foreign key.
4. **Many-to-Many**: Join table entries are created or updated to maintain linkage.

This orchestration ensures data integrity and reduces boilerplate code for manual linkage.

## Observability & Telemetry

Quark v1.0 features a centralized observability pipeline built directly into the low-level execution path (`BaseQuery`).

1. **Unified Event Streams**: Every database interaction (CRUD, Raw SQL, Migrations, Preloads) triggers a `QueryEvent` sent to registered `QueryObserver` instances.
2. **Detailed Metrics**: Events include raw SQL, positional arguments, execution duration, rows affected, and errors.
3. **Audit-Grade Logging**: Pre-built `SQLQueryLogger` provides structured logging (via `slog`) suitable for production audit trails.

## Tenant Context Propagation

A critical feature of Quark's multi-tenancy is the automatic propagation of tenant context through the entire operation lifecycle:

- **Recursive Saving**: When saving a nested graph (e.g., Author with Profiles and Posts), the tenant identifier is inherited by all related models during the recursive persistence process.
- **Preload Isolation**: Eager loading queries (`Preload`) automatically inject `WHERE tenant_id = ?` clauses for related models that support multi-tenancy, preventing data leakage.
- **Centralized Enforcement**: Injection happens at the lowest possible layer before SQL construction, ensuring that even raw SQL executions through the ORM benefit from isolation checks.

## Smart Caching (L2)

Quark v1.0 introduces a semantic caching layer designed for high-concurrency environments:

- **Pluggable Backends**: Support for `InMemory` (with auto-cleanup) and `Redis` (for clusters).
- **Tag-based Invalidation**: Automated "Smart Purge" that invalidates related cache entries whenever a table is mutated (INSERT/UPDATE/DELETE).
- **Tenant-Aware Hashing**: Cache keys include the Tenant ID, preventing cross-tenant data leakage in memory.

## Native JSON Support

Cross-database JSON querying is now a first-class citizen:

- **Dialect Abstraction**: Native translation for JSON paths across Postgres, MySQL, SQLite, MSSQL, and Oracle.
- **Queryable JSON**: Use `.WhereJSON("metadata", "theme", "=", "dark")` to filter by nested JSON properties.

## Evolutionary Migrations

Quark's auto-migration system supports table evolution through ALTER TABLE operations:

- **Add Columns**: `dialect.AlterTableAddColumn(table, column, dataType)`
- **Drop Columns**: `dialect.AlterTableDropColumn(table, column)`
- **Modify Columns**: `dialect.AlterTableAlterColumn(table, column, newDataType)`
- **Rename Columns**: `dialect.RenameColumn(table, oldName, newName)`
- **Rename Tables**: `dialect.RenameTable(oldName, newName)`

Each dialect implements these with database-specific SQL. The `SupportsTransactionalDDL()` method indicates whether DDL operations can be rolled back within a transaction.

## Next Steps

See `ROADMAP.md` for future architectural extensions.
