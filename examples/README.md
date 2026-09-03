# Quark ORM Examples

This directory contains real-world examples of Quark ORM usage with different database engines.

Each engine example is its **own Go module** (`examples/<name>/go.mod`), because
it imports the driver the way an application does — through the Quark driver
module (`_ "github.com/jcsvwinston/quark/drivers/sqlite"`, `…/drivers/postgres`,
…), which registers the `database/sql` driver and teaches Quark to classify its
errors. Those modules import Quark, so the library module cannot require them;
local `replace` directives point each example at this checkout instead. Run an
example from inside its directory:

```bash
cd examples/sqlite && go run .
```

To copy an example into your own project, drop the two `replace` lines and
`go get` the two modules it requires.

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
cd examples/sqlite && go run .
```

### 2. PostgreSQL (Multi-Tenant RLS)
Demonstrates Row Level Security (RLS) isolation and automatic tenant ID injection.

```bash
cd examples/postgres && go run .
```

### 3. MySQL (Transactions & Streaming)
Demonstrates transactional operations and memory-efficient result streaming using `Iter()`.

```bash
cd examples/mysql && go run .
```

### 4. MSSQL (Pagination & Builders)
Demonstrates pagination using the OFFSET/FETCH syntax required by SQL Server.

```bash
cd examples/mssql && go run .
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
cd examples/sharding && go run .
```

## Cleaning Up

```bash
docker rm -f quark-pg quark-mysql
```
