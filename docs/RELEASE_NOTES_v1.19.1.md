# Release notes — v1.19.1

A patch for the CLI: the DDL `migrate create --from-models` renders is
the DDL each engine accepts, idempotent the way the runtime makes it. The
library's behaviour does not change; a project that renders its migrations
from models on SQL Server, Oracle or MySQL gets a pair that applies as
written.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>

## Fixed

**`migrate create --from-models` renders each engine's own idempotent DDL
instead of a literal `IF NOT EXISTS` (QK-101;
[#492](https://github.com/jcsvwinston/quark/pull/492)).** The command
wrote `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX IF NOT EXISTS` for
every dialect, without the runtime's `IdempotentDDL`: SQL Server does not
accept the clause, Oracle only from 23ai, and MySQL not on indexes, so the
pair rendered for those engines failed when applied. The CLI now resolves
the dialect by name and renders through the same `IdempotentDDL` the
runtime uses (a `sys.tables`/`sys.indexes` guard on SQL Server, a bare
`CREATE` on Oracle, a bare `CREATE INDEX` on MySQL), quotes tables, columns
and indexes as the runtime does — Oracle in upper case, so the CLI's DDL
and `client.Migrate`'s match —, writes `DROP TABLE` without `IF EXISTS` on
Oracle, and keeps MySQL's backticks out of the Go file `--emit go` writes.
The `schema` 1 contract of `--emit json` does not change.

**Module floors.** `cmd/quark` (v1.3.1, which also carries the
`--from-models` fix) and the five `drivers/*` modules (v0.3.3) require
`github.com/jcsvwinston/quark v1.19.0`
([#494](https://github.com/jcsvwinston/quark/pull/494)); no driver code
changed.
