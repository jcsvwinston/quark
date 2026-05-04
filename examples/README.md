# Quark ORM Examples

This directory contains real-world examples of Quark ORM usage with different database engines.

## Prerequisites

To run the PostgreSQL and MySQL examples, you need to have the test databases running via Docker:

```bash
# From the project root
docker compose -f docker-compose.test.yml up -d
```

## Running Examples

### 1. SQLite Example
The SQLite example is self-contained and creates a local `example.db` file.

```bash
go run pkg/quark/examples/sqlite/main.go
```

### 2. PostgreSQL (Multi-Tenant RLS)
Demonstrates Row Level Security (RLS) isolation and automatic tenant ID injection.

```bash
go run pkg/quark/examples/postgres/main.go
```

### 3. MySQL (Transactions & Streaming)
Demonstrates transactional operations and memory-efficient result streaming using `Iter()`.

```bash
go run pkg/quark/examples/mysql/main.go
```

### 4. MSSQL (Pagination & Builders)
Demonstrates pagination using the OFFSET/FETCH syntax required by SQL Server.

```bash
go run pkg/quark/examples/mssql/main.go
```

### 5. Oracle (Godror Support)
Demonstrates Godror setup. Note that the Godror driver requires CGO enabled for Oracle compilation.

```bash
go run pkg/quark/examples/oracle/main.go
```

## Cleaning Up

```bash
docker compose -f docker-compose.test.yml down
```
