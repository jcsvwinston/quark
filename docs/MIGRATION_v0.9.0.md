# Migration guide — v0.8.0 → v0.9.0

This is the migration guide for Phase 5 changes. The v0.9.0 release
ships when **F5-4..F5-7** land; the items below are accumulated as
each PR merges so the doc is final when the tag is cut.

## Summary of breaking changes

| Item | Surface | Severity | Action |
| --- | --- | --- | --- |
| Tenant strategy renamed | `quark.RowLevelSecurity` → `quark.RowLevelSecurityClient` | **None** for callers — deprecated alias keeps value | Optional: replace at your own pace; the alias is removed in v1.0. |
| Hook timing change | `AfterCreate` / `AfterUpdate` / `AfterDelete` under `Client.Tx` | **Breaking minor** | Audit callers below. |
| EventBus placeholder renamed | `quark.EventBus` struct → `ListenerFactory`; `quark.NewEventBus` → `NewListenerFactory` | **Breaking, but the type was non-functional** | Rename call sites if you referenced the placeholder. See below. |

Other Phase 5 deliveries (`RowLevelSecurityNative`, `quarktenant`
CLI, `BeforeFindHook` / `AfterFindHook`, `Tx.OnCommit` /
`Tx.OnRollback`, the `EventBus` interface, audit log) are **purely
additive** — no migration is required to keep existing code working.

## EventBus placeholder rename (F5-6)

### What changed

v0.8.0 shipped a struct named `EventBus` (constructed via
`NewEventBus`) whose only method, `CreateListener`, always returned
`ErrDialectNotSupported` — a placeholder for a never-implemented
PostgreSQL LISTEN/NOTIFY listener. F5-6 introduces a real
**`EventBus` interface** for outbound CRUD lifecycle events, so the
placeholder struct was renamed to **`ListenerFactory`** and its
constructor to **`NewListenerFactory`**.

### Why this is (technically) breaking but practically safe

The renamed struct never did anything but return an error — there is
no working code that depends on its behaviour. If you happened to
reference `quark.EventBus` / `quark.NewEventBus` (e.g. in a type
assertion or a `_ = quark.NewEventBus(c)` smoke line), update the
identifiers:

```go
// Before (v0.8.0)
f := quark.NewEventBus(client)
_, err := f.CreateListener() // always ErrDialectNotSupported

// After (v0.9.0)
f := quark.NewListenerFactory(client)
_, err := f.CreateListener() // still ErrDialectNotSupported (out of scope, ADR-0013)
```

The `EventBus` name now refers to the outbound CRUD-event interface
(`Publish(ctx, Event) error`). See the
[Event Bus guide](../website/docs/advanced/events.mdx).

## Hook semantics change (F5-4)

### What changed

Under an **explicit transaction** opened via `Client.Tx` or
`Client.BeginTx`, the `After*` hooks (`AfterCreateHook`,
`AfterUpdateHook`, `AfterDeleteHook`) now fire **after the
transaction commits**, in the order the CRUD operations were issued.
Before v0.9.0, the hooks ran inline, immediately after the SQL
statement — at a point where the surrounding transaction had not yet
committed and could still roll back.

The non-transactional path (CRUD via `For[T]` without an explicit
`Tx`) is unchanged: hooks still run inline, immediately after the
statement.

### Why this is breaking

If your `After*` hook performed application work that assumed the
write was already persisted — emitting an event to a message broker,
updating an in-memory cache, sending an email — there was a race in
v0.8.0 and earlier where the hook could fire **before** the
transaction committed. If the commit then failed (constraint
deferred to commit time, optimistic-lock conflict, deadlock without
retry), the hook had already run; the side effect would not be
rolled back.

v0.9.0 closes that race for `Client.Tx` callers. The hook now fires
**after** the commit succeeds, exactly when the side effect can
honestly assume the write is durable.

### What to audit

Search your code for `After*` hook implementations on tenant-scoped
models that:

1. Run inside `Client.Tx` (or via `ForTx[T]`).
2. Read DB state that depends on the CRUD that triggered the hook.

Before v0.9.0, that read saw the uncommitted state inside the
transaction. After v0.9.0, the hook fires **outside** the
transaction (post-commit), so the same read sees committed state.
For most use cases this is what you actually wanted; if you relied
on the old "still-inside-tx" semantics for, say, a chained `Update`
that built on the same row, move that follow-up Update into the
`fn` of `Client.Tx` rather than into the After hook.

### What stays the same

- `Before*` hooks still run inline before the SQL statement, both in
  the transactional and the non-transactional path. Their error
  return still aborts the operation (and, in the transactional case,
  causes the surrounding transaction to roll back when the caller
  propagates the error).
- Non-transactional `For[T].Create/Update/Delete` keeps inline
  After-hook semantics — `For[T]` was already a "fire and forget"
  shape; wrapping every single-statement CRUD in an implicit
  transaction would have added two round-trips per call for no
  safety gain. The hook order remains: BeforeX → SQL → AfterX.
- If `Tx.Rollback` is invoked (explicitly or because the
  `Client.Tx` callback returned an error), the queued After hooks
  are **discarded** entirely. The DB never committed; the side
  effects never fire. This matches the contract you'd expect from
  "After hooks observe committed state".

### How the new semantics are implemented

For the curious: each `*quark.Tx` now owns a FIFO queue
(`afterHooks []func() error`). When the CRUD path calls an After
hook against a Query that was bound to a transaction
(`ForTx[T](ctx, tx)`), the hook closure is appended to the queue
instead of being invoked inline. `Tx.Commit()` drains the queue
after the underlying `*sql.Tx.Commit` succeeds. A hook returning an
error post-commit is logged via the Client's slog logger (event
`quark.hook.after_post_commit_error`) but does not block the rest
of the cascade — once the commit is durable, no application code
can undo it (ADR-0013 Regla 2).

This same queue is the foundation that F5-5 exposes to application
code as the public `Tx.OnCommit(fn)` / `Tx.OnRollback(fn)` API
(shipped — see below).

## New optional features (no migration needed)

### `BeforeFindHook` and `AfterFindHook` (F5-4)

Two new interfaces in `hooks.go`:

```go
type BeforeFindHook interface { BeforeFind(ctx context.Context) error }
type AfterFindHook  interface { AfterFind(ctx context.Context) error  }
```

Implement on `*Model` to hook around read operations: `List`,
`First`, `Find`, `Iter`, `Cursor`. `BeforeFind` fires before any SQL
is built; `AfterFind` fires exactly once after the result is
hydrated (including relations from `Preload`). Iter and Cursor wire
AfterFind only on successful completion.

### `RowLevelSecurityNative` and `TenantRouter.Tx` (F5-2)

See [`row-level-native.mdx`](../website/docs/advanced/row-level-native.mdx).
Engine-enforced PostgreSQL RLS; opt-in.

### `quarktenant` CLI (F5-3)

Library-style CLI for installing the policies F5-2 needs. See
[`row-level-native.mdx` — Option A](../website/docs/advanced/row-level-native.mdx#1-install-the-policy-on-each-tenant-scoped-table).
Opt-in.

### `Tx.OnCommit` / `Tx.OnRollback` / `TxFromContext` (F5-5)

Register side-effect callbacks that fire when a transaction reaches
its terminal state. `OnCommit` runs FIFO after the model `After*`
hooks once the commit succeeds; `OnRollback` runs FIFO after a
rollback. Callback errors are logged, never propagated.
`quark.TxFromContext(ctx)` resolves the active `*Tx` from a context
so lifecycle hooks can register their own commit/rollback effects.
See [`transactions.mdx` — Side-effects on commit/rollback](../website/docs/guides/transactions.mdx).
Net-new API; opt-in.

### `EventBus` + `Client.UseEventBus` (F5-6)

Outbound CRUD lifecycle events. `Client.UseEventBus(bus)` makes every
`Create`/`Update`/`Delete` publish a `created`/`updated`/`deleted`
event after the write commits. In-tree `LoggerEventBus` /
`OTelEventBus`; implement `EventBus` for an external broker. Emit
failures surface as `ErrEventEmitFailed` (non-tx) or a logged
`quark.event.emit_failure` (tx). See the
[Event Bus guide](../website/docs/advanced/events.mdx). Net-new API;
opt-in. (The `EventBus` *name* rename is the breaking part — covered
above.)

### Audit log — `Client.EnableAuditLog` (F5-7)

Opt-in per-row change trail in `quark_audit`. `EnableAuditLog`
migrates the table (portable DDL across all six dialects) and records
`created`/`updated`/`deleted` rows with a JSON `diff`, written
atomically on the CRUD transaction. `AuditConfig` carries
`UserFromContext` / `TenantFromContext` / `IncludeTables` /
`ExcludeTables`. See the
[Audit Log guide](../website/docs/advanced/audit-log.mdx). Net-new
API; opt-in.

## Phase 5 complete

All seven Phase 5 items (F5-1 … F5-7) have landed. The only breaking
changes in v0.9.0 are the two **breaking-minor** items in the table
above (hook timing under `Client.Tx`; the `EventBus`→`ListenerFactory`
placeholder rename). Everything else is additive and opt-in.
