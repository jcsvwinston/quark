---
id: 0028
title: CreateBatch without RETURNING leaves the per-row form only where the engine proves which key each row got
status: accepted
date: 2026-10-06
implemented: v1.17 (arc A12, session Q3 — batch_backfill.go)
deciders: jcsvwinston
related: [0027]
supersedes: null
tags: [performance, correctness, mysql, sqlserver, batch]
---

# 0028 — CreateBatch keys on engines without RETURNING

## Context

`CreateBatch` hands every entity the key the database generated for its row.
PostgreSQL, SQLite and MariaDB read the keys back with `RETURNING`, one
statement per chunk. MySQL and SQL Server have no `RETURNING`; since Finding G
they inserted one row per statement to read each key, a round trip per row
(QK-36). Measured in this session on an Apple M4 Pro, through Docker
Desktop's published ports: 1000 rows took 301 ms on mysql:8.4 row by row and
3.7 ms as one statement; on SQL Server 2022 (the amd64 image, emulated) 636 ms
row by row and 81 ms as one statement.

A key handed to the wrong entity is not a slow path, it is data corruption:
the caller's next `Update` writes another row. So the question was never
"how do we read the keys of a multi-row INSERT", it was "where can the
engine prove which key each row got".

## Decision

### SQL Server: an insert-only MERGE that returns each key with its row's position

`INSERT … OUTPUT INSERTED.id VALUES (…), (…)` returns every key, but SQL
Server does not guarantee that OUTPUT returns them in the order of the VALUES
list, so they cannot be matched to the entities. An insert-only `MERGE … ON
1 = 0` can name a column of its source in OUTPUT: each key comes back next to
the position of its row, and the matching is exact. The keys go into a table
variable (`OUTPUT … INTO`, which tables with triggers accept) and are read
back in the same batch. One round trip per chunk.

It runs only on a user table whose key column is an `IDENTITY` and that has no
`INSTEAD OF` trigger — the conditions under which the key OUTPUT returns is
the `SCOPE_IDENTITY()` the per-row form read — checked once per client and
table. A chunk where a column's values are not all of one Go type goes row
by row: the VALUES list types each column once for all rows, and only
same-typed values convert exactly as single-row INSERTs do.

### MySQL: one multi-row INSERT, and the keys computed — only where MySQL documents them consecutive

`LAST_INSERT_ID()` after a multi-row INSERT is the key of its first row. The
others follow only if InnoDB allocated them as one consecutive run, and the
MySQL 8.0 manual ("AUTO_INCREMENT Handling in InnoDB") says when it does:

- under `innodb_autoinc_lock_mode` 0 and 1, "auto-increment numbers assigned
  by any given statement are consecutive";
- under 2 — **the MySQL 8 default** — "the values generated for the rows
  inserted by any given statement may not be consecutive", with the
  exception "if the only statements executing are 'simple inserts'", which
  no client can check.

So the multi-row form runs only when, checked once per client and table, the
server's lock mode is 0 or 1, the table is InnoDB, the key column is its
`AUTO_INCREMENT` column, no trigger runs on INSERT (a `BEFORE INSERT` trigger
can set the key itself), and the server is not TiDB or Vitess. The step
between keys is `auto_increment_increment`, which has a session value, so it
is read on the INSERT's own connection after every chunk. A chunk of fewer
than five rows goes row by row, where that is fewer round trips. On a
default MySQL 8 server nothing changes: one INSERT per row, as before.

### Both: a failure leaves what the per-row form left

A single statement inserts its whole chunk or nothing; the per-row form left
the rows before a failing one, with their keys set. To keep the outcome the
caller sees:

- SQL Server: the MERGE runs in `TRY`; its `CATCH` answers a marker row,
  which says the server caught the error and undid the statement, and the
  chunk then goes row by row. If the error took the caller's transaction with
  it (`XACT_STATE() = -1`, or `@@TRANCOUNT` changed — a deadlock), the error
  is raised instead: running the rows again would run them outside the
  transaction.
- MySQL: the INSERT runs inside a transaction of its own, or inside a
  savepoint of the caller's. If it fails it is rolled back and the chunk goes
  row by row; if the savepoint is gone with the caller's transaction, the
  error is returned instead. Only a failed COMMIT or RELEASE — an unknown
  outcome — is returned as it is.

`TestCreateBatchKeysAllEngines` pins all of it on the six engines: every key
reads back its own row while three other connections insert into the same
table; a batch whose sixth row is rejected leaves the five before it with
their keys on MySQL and SQL Server, and nothing on the engines with
RETURNING; and MySQL runs twice, on the default lock mode (one INSERT per row)
and on a server started with `innodb_autoinc_lock_mode=1` (one per chunk).

## Consequences

- SQL Server: one round trip per chunk wherever the probe passes, which is
  every `IDENTITY` key Quark's migrator creates.
- MySQL: one per chunk only on servers configured with lock mode 0 or 1;
  MySQL 8 installations on the default keep the per-row form. The guides say
  so, and how to change the setting (it is read-only at runtime).
- Two catalog probes per client and table, cached; a probe that fails is not
  cached, and the batch takes the per-row form.

## Alternatives considered

- **Trust `LAST_INSERT_ID()` plus consecutive keys everywhere**, as some ORMs
  do. Correct on the default MySQL 8 setting most of the time, and wrong under
  concurrent bulk inserts; a wrong key is silent. Rejected.
- **`INSERT … OUTPUT INSERTED.id` and sort the keys.** Relies on the order of
  identity values following the VALUES order, which SQL Server guarantees
  only for `INSERT … SELECT … ORDER BY`, and on a positive increment.
  Rejected for the MERGE, whose mapping does not depend on either.
- **One batch of single-row INSERTs with `SCOPE_IDENTITY()` after each.**
  Equivalent by construction, and measured: on SQL Server 2022 (the amd64
  image under emulation on an Apple M4 Pro) 500 rows took 160 ms warm and
  980 ms on first compile, against 40 ms and 170 ms for the MERGE and 320 ms
  row by row. A batch size seen for the first time costs more than the
  per-row form it replaces. Rejected.

## When to reopen

- If MySQL documents consecutive keys for simple inserts under lock mode 2,
  or adds `RETURNING` (MariaDB has it since 10.5, and uses it).
- If SQL Server documents the order of OUTPUT rows.
