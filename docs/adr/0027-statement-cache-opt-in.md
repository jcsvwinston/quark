---
id: 0027
title: Prepared statements are reused only on request — one LRU per pool, statements bound per transaction, never on PostgreSQL or Oracle
status: accepted
date: 2026-10-06
implemented: v1.17 (arc A12, session Q3 — stmt_cache.go; the seam in observe.go; Client.BeginTx/Commit/Rollback)
deciders: jcsvwinston
related: [0015, 0023]
supersedes: null
tags: [performance, drivers, mysql, transactions]
---

# 0027 — Prepared statements are reused only on request

## Context

The A12 engine bench (`benchmarks/engines`, control `MY-01`) measured
MySQL's FindByPK at 1.4 times a hand-written program that sends the query
per call, and 2.7 times one that prepares the statement once and reuses it.
The reason is the protocol, not Quark's CPU: given arguments and no
`interpolateParams`, go-sql-driver/mysql sends a query as three commands —
prepare, execute, close — and two round trips, every time. Quark keeps no
statement, so every Find pays all three (QK-37).

PostgreSQL does not have the problem: pgx keeps a statement cache per
connection, and S0 measured nothing to gain there.

Two shapes were on the table: reuse statements, or document
`interpolateParams=true`, which makes the driver write the arguments into the
SQL text and send one query with no server-side statement at all.

## Decision

### 1. An option, off by default: `WithStatementCache(size)`

The cache changes how statements travel, and an existing application must
not find that out from a deploy: it is opt-in, as QADR-0010 requires of new
behaviour. `interpolateParams` is documented next to it, measured, because it
is the other answer and the right one for some deployments (behind a proxy
that does not track prepared statements, or where `max_prepared_stmt_count`
is tight).

### 2. What is cached is what the driver already prepared

Only the query builder's statements (`StatementQuery`, `StatementExec`) with
at least one argument, no `sql.NamedArg`, and at most 4 KiB of SQL. On MySQL
those are exactly the statements the driver prepares today; the cache skips
the second and later prepares and the closes, and changes neither the SQL
the server runs nor the binary protocol it answers in. A statement without
arguments travels as plain text today and is left alone, so no statement
moves from the text protocol to the prepared one. Schema work, savepoints,
raw SQL and named arguments (Oracle's `RETURNING … INTO`) are never cached.
The 4 KiB bound keeps multi-row batches out: their text changes with the row
count and would only push the statements that repeat out of the cache.

### 3. One LRU per pool, with a reference count

The client's pool and each replica pool get an LRU of their own, keyed by
SQL text. An entry is counted while a call holds it; eviction marks it, and
the statement is closed when the last holder hands it back — so a call never
finds its statement closed between taking it and running it. Rows still
being read keep the driver's statement alive past the close: database/sql
defers the final close until they are closed. The prepare on a miss runs
outside the lock.

### 4. Inside a transaction: prepared on the transaction, once

A statement is prepared with `(*sql.Tx).PrepareContext` the first time a
transaction sends it, and reused until the transaction ends, when
database/sql closes it. The transaction's set is bounded by the same size.
Two shortcuts were rejected:

- `(*sql.Tx).StmtContext(poolStmt)` would reuse the pool statement's
  preparation on the transaction's connection, but it hides a failed
  preparation inside the returned statement until its first use. Through
  `QueryRow` that use is after the lease ended, so a preparation that timed
  out would fail every later call of that statement in the transaction.
- Preparing a missing statement on the pool needs a pool connection, which
  the transaction may be holding: with `MaxOpenConns(1)` the wait never ends.
  `TestStatementCacheInTransaction` runs exactly that configuration.

Statements sent on a `*sql.Conn` (the migration lock, the per-statement
transaction of `RowLevelSecurityNative`) and on any executor a middleware
substitutes go to the driver as before. The cache sits in the innermost link
of the seam, so the middleware chain and the observers see the same
statements and the same events either way.

### 5. Not on PostgreSQL, not on Oracle

On PostgreSQL the option is ignored, and `New` logs it (event
`quark.stmtcache.ignored`): pgx already caches per connection and handles a
plan that a schema change invalidated; a database/sql statement on top would
prepare twice and handle neither.

On Oracle it is ignored too, for a defect measured in this session, not a
guess. With go-ora v2.9.0 — the driver `drivers/oracle` ships, and the
latest release — a prepared `SELECT COUNT(*) … WHERE id = :1 AND owner = :2`,
re-executed after a `DELETE` without arguments and an `INSERT` had run on the
same connection, answered 0 for a row that existed; the same query unprepared
answered 1. It reproduces with go-ora alone, on one connection, without Quark
in between. Quark tells a stale entity from an excluded one by that count, so
under the cache `UpdateFields` on a versioned model accepted a stale write
silently (`WhereGuards/Versioned` in the SharedSuite). The engine test
`TestStatementCacheAllEngines` replays the sequence; with the option forced on
for Oracle it fails in its second round.

## Consequences

- MySQL and MariaDB get the reuse the S0 measurement priced: on the engine
  bench (control `MY-02`, mysql:8.4), FindByPK with the cache took 46–48 %
  less time than without it, in the same rounds, in five runs on the CI
  runner (145 µs against 270 µs on an AMD EPYC 7763), and 41–42 % less on an
  Apple M4 Pro laptop. It is then 1.65 times a hand-written program that
  reuses its statement: what remains is Quark's CPU share of the query, not
  the protocol.
- SQL Server and SQLite get it too. go-mssqldb prepares on the client side
  (each execution is an `sp_executesql`), so the gain there is CPU, not round
  trips, and it is not measured.
- Each connection that ran a cached statement keeps it prepared on the server
  until eviction or until the connection closes: up to `size × MaxOpenConns`
  per pool. On MySQL `max_prepared_stmt_count` (16382 by default) caps that
  across every client of the server. The option's documentation says so;
  `TestStatementCacheAllEngines` checks the bound with the server's own
  `Prepared_stmt_count`, and that it returns to where it was after `Close`.
- The whole SharedSuite runs a second time with the option on, on MySQL,
  MariaDB, SQL Server and SQLite (`TestSuite*StatementCache`).

## Alternatives considered

- **Documenting `interpolateParams` only.** Simpler, and it is documented. But
  it is a DSN setting that changes every statement of the pool, including raw
  SQL, and it removes server-side type checking of the arguments; the cache
  keeps the protocol the driver chose and only stops repeating it.
- **On by default.** Rejected by QADR-0010: a protocol change an application
  did not ask for, with a server-wide resource (`max_prepared_stmt_count`)
  behind it.
- **A global cache keyed by `*sql.DB`.** Two clients on one pool
  (`NewWithDB`, `WithOptions`) would share statements whose lifetime neither
  owns. One cache per client is simpler to close and to bound.

## When to reopen

- When go-ora releases a fix for the stale re-execution: re-run
  `TestStatementCacheAllEngines` with Oracle enabled and drop it from
  `stmtCacheRefusal` if it passes.
- If a deployment needs the cache behind a connection proxy that multiplexes
  sessions (ProxySQL, Vitess): prepared statements are per session there, and
  the option should stay off — the reopening would be about detecting it.
