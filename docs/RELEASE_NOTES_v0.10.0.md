# Quark v0.10.0 — Release Notes

> **Date:** 2026-05-22
> **Status:** late-alpha. Not yet v1.0 production-ready.
> See [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) for the honest gap analysis between the current state and a planned v1.0.

Maintenance release: correctness fixes for transactions and SQL Server,
a real cross-engine deadlock test, and a developer-experience warning
for raw SQL under Native RLS — plus the formal opening of Phase 6 (the
path to v1.0). **No breaking changes.** The only new public-facing
behaviour is the Native-RLS warning, which is opt-in by configuration.

## Fixes

### `JSON[T]` / `Array[T]` now round-trip on SQL Server ([#89])

`JSON[T].Value()` and `Array[T].Value()` returned `[]byte`, which
go-mssqldb binds as `VARBINARY`. Stored into the `NVARCHAR(MAX)` JSON
column, SQL Server performed an implicit VARBINARY→NVARCHAR conversion
that reinterpreted the UTF-8 bytes as UTF-16 and corrupted the payload,
so `Scan` failed with `invalid character 'â'`. Both methods now return a
`string`, which binds as `NVARCHAR` on SQL Server and as the equivalent
text type on every other driver. This also repairs the optional audit
log's `diff` read-back on SQL Server. The MSSQL skips were removed from
the JSON / Array / audit test suites.

### Savepoint rollback unwinds queued hooks ([#88])

Rolling back to a savepoint (`Tx.RollbackTo`, or the `Tx.Tx`
nested-transaction helper) now discards the model `After*` hooks and
`OnCommit` / `OnRollback` callbacks queued by CRUD run since that
savepoint. Previously they survived the rollback and fired on the outer
commit, so a rolled-back nested scope could trigger the side-effects —
published events, audit entries, cache invalidations — of work that
never committed (ADR-0013 Regla 2, extended to savepoints).
`ReleaseSavepoint` keeps the queued hooks, as released work merges into
the surrounding transaction.

## Added

### Raw-under-Native-RLS warning ([#91])

Under `RowLevelSecurityNative`, `Client.RawQuery` and `Client.Exec` now
emit a structured `quark.tenant.raw_under_native_rls` warning when the
call's context resolves a tenant — a cue that the raw call sidesteps the
tenant-scoped query builder. The PostgreSQL policy still enforces
isolation server-side, so this is a developer-experience signal, **not a
security boundary**. Zero cost for every other configuration.

## Tests

### Real cross-engine deadlock-retry integration test ([#90])

`WithDeadlockRetry` was unit-tested with a fabricated SQLSTATE; this
release adds an integration test that provokes a genuine engine deadlock
(two transactions taking the same two row locks in opposite order behind
a barrier) and asserts the victim retries and commits. Runs on
PostgreSQL, MySQL and MariaDB. SQLite is excluded (single-writer);
MSSQL / Oracle stay covered by the error-code classifier unit test.

## Phase 6 opened ([#93])

Phase 6 — codegen + HA + sharding + benchmarks, the path to v1.0 — is
formally open. Anchor decision:
[ADR-0014](adr/0014-codegen-coexistence-typed-registry.md) (the
codegen↔reflect coexistence mechanism that ADR-0002 left open).
Decomposed into F6-1..F6-9 in [`TASKS.md`](../TASKS.md) and tracked in
the Phase 6 planning issue. v1.0 ships only when the F6-8 benchmark
suite proves the performance gate from ADR-0002.

[#88]: https://github.com/jcsvwinston/quark/pull/88
[#89]: https://github.com/jcsvwinston/quark/pull/89
[#90]: https://github.com/jcsvwinston/quark/pull/90
[#91]: https://github.com/jcsvwinston/quark/pull/91
[#93]: https://github.com/jcsvwinston/quark/pull/93
