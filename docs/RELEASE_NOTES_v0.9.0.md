# Quark v0.9.0 — Release Notes

> **Date:** 2026-05-21
> **Status:** late-alpha. Not yet v1.0 production-ready.
> See [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) for the honest gap analysis between the current state and a planned v1.0.

Phase 5 release. Closes F5-1 through F5-7: engine-enforced PostgreSQL
multi-tenancy, transactional hooks, a real event bus, and an optional
audit log. Most of the surface is **opt-in** and additive, but two
changes are **breaking-minor** — read
[`docs/MIGRATION_v0.9.0.md`](MIGRATION_v0.9.0.md) before upgrading:

1. `After*` hooks invoked through a `Query[T]` bound to an explicit
   `Client.Tx` now fire **after the transaction commits** (they used
   to fire inline after the SQL). The non-transactional path is
   unchanged.
2. The v0.8.0 placeholder struct `EventBus` (a LISTEN/NOTIFY factory
   that only ever returned `ErrDialectNotSupported`) was renamed to
   `ListenerFactory` (`NewEventBus` → `NewListenerFactory`) to free
   the `EventBus` name for the new CRUD-event interface. The struct
   was non-functional, so no working code path changes behaviour.

## What's in this release

### Engine-enforced multi-tenancy (F5-1, F5-2, F5-3)

- **F5-1 — `RowLevelSecurityClient` rename.** The `RowLevelSecurity`
  tenant strategy is renamed to `RowLevelSecurityClient` to make
  explicit that it is client-side WHERE injection, not engine-enforced
  RLS. The old name remains a `// Deprecated:` alias with the same
  value (removed in v1.0) — existing code keeps compiling. ([#78](https://github.com/jcsvwinston/quark/pull/78))

- **F5-2 — `RowLevelSecurityNative` (PostgreSQL).** A new tenant
  strategy that delegates row isolation to the database engine: each
  query runs in a transaction that first calls
  `set_config('app.tenant_id', <tenant>, true)`, and `CREATE POLICY`
  clauses filter rows server-side. Unlike the client strategy,
  `client.Raw()` is filtered too. `TenantRouter.Tx(ctx, fn)` is the
  recommended entry point; `For[T]` also works via an implicit
  per-query transaction. PostgreSQL-only — other dialects return
  `ErrUnsupportedFeature`. ([#80](https://github.com/jcsvwinston/quark/pull/80))

- **F5-3 — `quarktenant` policy CLI.** The
  `github.com/jcsvwinston/quark/quarktenant` library ships an
  `install-rls-policies` subcommand that generates the
  `ENABLE`/`FORCE ROW LEVEL SECURITY` + `CREATE POLICY` DDL for every
  registered model and applies it inside a single transaction under a
  distributed migration lock. `--dry-run` prints the DDL without
  applying. The `--cast` flag is validated against a type-token
  whitelist (no SQL injection through the cast). ([#81](https://github.com/jcsvwinston/quark/pull/81))

### Transactional hooks and side-effects (F5-4, F5-5)

- **F5-4 — Post-commit `After*` hooks + Find hooks.** `AfterCreate` /
  `AfterUpdate` / `AfterDelete` hooks invoked under `Client.Tx` now
  fire after the commit succeeds (queued on the `*Tx`, discarded on
  rollback) — closing the race where a hook could fire before a commit
  that then failed. New `BeforeFindHook` / `AfterFindHook` interfaces
  fire around `List` / `First` / `Find` / `Iter` / `Cursor`.
  **Breaking-minor** — see the migration guide. ([#82](https://github.com/jcsvwinston/quark/pull/82))

- **F5-5 — `Tx.OnCommit` / `Tx.OnRollback`.** Register arbitrary
  side-effects that fire (FIFO) when a transaction reaches its
  terminal state. Callback errors are logged, never propagated.
  `quark.TxFromContext(ctx)` resolves the active `*Tx` from a hook's
  context so a hook can register its own commit/rollback effect. ([#83](https://github.com/jcsvwinston/quark/pull/83))

### Events and audit (F5-6, F5-7)

- **F5-6 — `EventBus`.** `Client.UseEventBus(bus)` publishes a
  `created` / `updated` / `deleted` event after each write commits.
  In-tree `LoggerEventBus` and `OTelEventBus`; implement the
  `EventBus` interface for an external broker (NATS / Kafka / Redis
  Streams). Delivery is **synchronous, at-least-once, no outbox** — an
  emit failure surfaces as `ErrEventEmitFailed` (non-transactional) or
  a logged `quark.event.emit_failure` (transactional); the committed
  write is never rolled back. ([#84](https://github.com/jcsvwinston/quark/pull/84))

- **F5-7 — Audit log.** `Client.EnableAuditLog(ctx, AuditConfig)`
  records every `Create` / `Update` / `Delete` into a `quark_audit`
  table (portable DDL across all six dialects). The audit row is
  written **atomically with the CRUD transaction** — under `Client.Tx`
  it commits or rolls back together with the data, so there is never
  committed data without a trail nor a trail for rolled-back work. The
  diff is the full row for create/delete, new values for plain
  `Update`, and a `{old, new}` delta for `Tracked.Save`.
  `AuditConfig` carries `UserFromContext` / `TenantFromContext` /
  `IncludeTables` / `ExcludeTables`. ([#85](https://github.com/jcsvwinston/quark/pull/85))

## Known limitations

- **Still late-alpha.** v0.9.0 is not v1.0 production-ready. Phase 6
  (codegen, read replicas / failover, sharding, real benchmarks) is
  the remaining work toward an honest v1.0.
- **Native RLS is PostgreSQL-only.** Other dialects use the
  client-side `RowLevelSecurityClient` strategy.
- **Events are at-least-once, no transactional outbox.** An event can
  be lost if the process crashes between commit and emit. Use an
  idempotent subscriber, or wait for an outbox in a later release.
- **Bulk and WHERE-based methods are not hooked, evented, or audited**
  (`CreateBatch` / `UpdateBatch` / `DeleteBatch` / `DeleteBy` /
  `UpdateMap`) — they have no per-row entity to act on.
- **MSSQL `JSON[T]` round-trip bug** (F0-8 follow-up) blocks reading
  the audit `diff` column back on MSSQL; the audit *write* works
  there. The other engines are unaffected.
- **`LISTEN/NOTIFY` (inbound) remains out of scope** —
  `ListenerFactory.CreateListener` returns `ErrDialectNotSupported`.

## Documentation

Versioned docs for this release:
<https://jcsvwinston.github.io/quark/docs/0.9.0/>

New guides: [Event Bus](https://jcsvwinston.github.io/quark/docs/0.9.0/advanced/events),
[Audit Log](https://jcsvwinston.github.io/quark/docs/0.9.0/advanced/audit-log),
[PostgreSQL Native RLS](https://jcsvwinston.github.io/quark/docs/0.9.0/advanced/row-level-native),
[Lifecycle Hooks](https://jcsvwinston.github.io/quark/docs/0.9.0/guides/hooks).
