# Quark ORM Examples

This directory contains real-world examples of Quark ORM usage with different database engines.

## Prerequisites

To run the PostgreSQL and MySQL examples, you need a database reachable at
the DSN each example reads from its environment. A throwaway container per
engine is enough:

```bash
docker run -d --name quark-pg -p 5432:5432 -e POSTGRES_USER=quark -e POSTGRES_PASSWORD=quark -e POSTGRES_DB=quark_test postgres:16-alpine
docker run -d --name quark-mysql -p 3306:3306 -e MYSQL_ROOT_PASSWORD=quark -e MYSQL_DATABASE=quark_test mysql:8
```

The SQLite and sharding examples need no infrastructure at all.

## Running Examples

### 1. SQLite Example
The SQLite example is self-contained and creates a local `example.db` file.

```bash
go run ./examples/sqlite/main.go
```

### 2. PostgreSQL (Multi-Tenant RLS)
Demonstrates Row Level Security (RLS) isolation and automatic tenant ID injection.

```bash
go run ./examples/postgres/main.go
```

### 3. MySQL (Transactions & Streaming)
Demonstrates transactional operations and memory-efficient result streaming using `Iter()`.

```bash
go run ./examples/mysql/main.go
```

### 4. MSSQL (Pagination & Builders)
Demonstrates pagination using the OFFSET/FETCH syntax required by SQL Server.

```bash
go run ./examples/mssql/main.go
```

### 5. Oracle
Demonstrates Oracle setup with the `sijms/go-ora/v2` driver — pure Go, no
CGO or Oracle client libraries required.

```bash
go run ./examples/oracle/main.go
```

### 6. Sharding (ShardRouter)
Self-contained (no Docker): partitions data across two SQLite shards by shard
key via `ShardRouter`, proving per-shard disjoint storage and the keyless-query
rejection. See the [Sharding guide](https://jcsvwinston.github.io/quark/docs/advanced/sharding).

```bash
go run ./examples/sharding/main.go
```

## Cleaning Up

```bash
docker rm -f quark-pg quark-mysql
```
