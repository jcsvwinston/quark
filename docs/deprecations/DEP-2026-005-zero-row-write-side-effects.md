# Behaviour-Change Notice: a write that touches no row reports nothing in 2.0

- ID: `DEP-2026-005`
- Status: `active`
- Announced in: the first release after `v1.17.0` that carries this file — a
  documentation-only change cuts no release of its own
- Earliest change: `v2.0.0`, no earlier than `2027-01-17`
- Scope: `behaviour`
- Affected lifecycle tag: `stable`
- Owner: `@jcsvwinston`

## Summary

`Update`, `UpdateFields`, `Delete` and `HardDelete` write the row the
entity's key names. When the query carries no conditions and no row has that
key, the statement matches nothing, the call returns `(0, nil)` — and still
runs the `AfterUpdate` or `AfterDelete` hook, writes the audit row and
publishes the `updated` or `deleted` event, as if it had written (QK-46,
seen in A11 `Q11`).

Under conditions the same calls already report nothing: QK-40 (quark#435)
made a write whose conditions exclude the row return `(0, nil)` with no
hook, audit row or event, because an audit row saying "deleted" for a row
that was not deleted is a false record. The unconditioned case was left as
it was, because changing it changes every call site that carries no
conditions. The owner decided on 2026-10-06 (QK-46) that in 2.0 a write that
touched no row reports nothing, conditions or not. QADR-0010 keeps that
kind of change for the major.

## Affected surfaces

On a query with no conditions, for a key no row has:

- `(*quark.Query[T]).Update` and `(*quark.Query[T]).UpdateFields`, on a
  model without a version column;
- `(*quark.Query[T]).Delete` (hard and soft) and
  `(*quark.Query[T]).HardDelete`.

Not affected: the same calls under conditions, which report nothing since
QK-40; `Update` and `UpdateFields` on a model with a version column, which
return `ErrStaleEntity` for a missing key and run nothing either way;
`Create`, which has no missing key; `UpdateMap`, `DeleteBy` and
`DeleteBatch`, which carry no entity and run no entity hook; the `Before*`
hooks, which run before the statement and keep running.

## What changes at 2.0

A write by entity that affected zero rows runs no `After*` hook, writes no
audit row and publishes no event, whether or not the query carries
conditions. The return value does not change: `(0, nil)`.

For the 2.0 PR: MySQL and MariaDB count changed rows, not matched ones, so
an `Update` that matched the row and changed nothing also answers zero
there. Under conditions the `keyPassesWhere` probe already tells the two
apart; the unconditioned path needs the same probe (or `clientFoundRows` on
the connection) so that a write that matched its row keeps reporting. This
notice is about writes that matched no row.

## Migration path

- **Now (v1):** nothing to keep compiling. A program that wants the 2.0
  behaviour today can give the query a condition the row always passes —
  conditioned writes already report nothing when they touch no row:

  ```go
  // before: a missing key still runs AfterUpdate, audits and publishes
  n, err := quark.For[Order](ctx, client).Update(&o)

  // after: the 2.0 behaviour today, at the cost of one probe when n == 0
  n, err := quark.For[Order](ctx, client).Where("id", "=", o.ID).Update(&o)
  ```

  Most programs need nothing: `n == 0` already says nothing was written.
- **At 2.0:** the unconditioned write reports nothing on zero rows; the
  `Where` above keeps compiling and becomes redundant.
- **Behaviour differences, from the caller's side:** an `After*` hook that
  used to run for a missing key no longer runs; an audit row and an event
  that used to appear no longer appear.
- **Required app changes:** none to keep compiling. A program that relied on
  the side effects of a write that touched nothing — an audit row for a key
  that never existed, an event a consumer acts on — has to decide what it
  wants before the upgrade.

## Detection

- Source scan for writes by entity without conditions:

  ```sh
  grep -rnE '\.(Update|UpdateFields|Delete|HardDelete)\(' --include='*.go' .
  ```

  Only the hits whose query carries no `Where` (nor a tenant scope) are
  affected, and only when the key can be missing.
- Runtime signature in v1: an `updated` or `deleted` event, or a row in
  `quark_audit`, for a key the table does not hold and never held. Nothing
  is logged: the silence is what this notice changes.

## What breaks if this is ignored

At `v2.0.0` a write by entity on a key no row has stops running its
`After*` hook, stops writing its audit row and stops publishing its event.
A consumer that counted events against calls sees fewer; an audit table
stops gaining rows for keys that do not exist; an `AfterDelete` that did
cleanup for a key that was already gone no longer runs. The return value is
the same `(0, nil)` it is today, so a program that checks it sees no change.

## Change window

- **Why `v2.0.0`:** a hook, an audit row and an event that fire today must
  not stop firing within `v1.x` (QADR-0010). The change lands with the
  major.
- **Why `2027-01-17`:** the suite's support policy sets a minimum of 90
  calendar days of notice, counted from the set that publishes the notice.
  The owner decided on 2026-10-06; this notice is dated 2026-10-16; the first
  set in cadence that can pin it is the one of 2026-10-19, and 90 days later
  is 2027-01-17. If a later set is the first to pin it, the date moves with
  that set, and the suite's list of the major
  (`quantum/docs/planes/quantum-2.0.md` §3.0) carries the date that governs.
- The date opens the window; the version is where the change can land.
  Whichever comes second governs.

## Validation

- **The v1 behaviour is pinned.**
  `TestAWriteWithoutConditionsOnAMissingKeyStillReportsInV1`
  (`missing_key_side_effects_test.go`, SQLite):

  ```sh
  go test -count=1 -run '^TestAWriteWithoutConditionsOnAMissingKeyStillReportsInV1$' .
  ```

  One subtest per surface — `Update`, `UpdateFields`, `Delete`,
  `HardDelete` — asserting `(0, nil)` with one hook run, one audit row and
  one event; `VersionedUpdateIsAlreadyAnError` pins the versioned case that
  does not change. The 2.0 PR changes the four to assert what the
  conditioned test asserts, in the same commit as the behaviour.
- **The conditioned half is exercised** by
  `TestAWriteTheConditionsExcludeReportsNothing` (`where_guard_test.go`,
  SQLite) and by `TestSuite<Engine>/WhereGuards`
  (`internal/enginesuite/where_guard_test.go`, the six engines), which the
  2.0 PR extends with the unconditioned case per engine — MySQL and MariaDB
  for the changed-rows count.
- **Release note:** the release that carries this file announces it.
- **Rollback plan:** not applicable in v1 — nothing changes.

## Timeline

- Decided: `2026-10-06` (QK-46).
- Written: `2026-10-16` (A12 `M0-Q`).
- Review checkpoint: `2027-01-17`, when the window opens.
- Change: at `v2.0.0` planning (`M1`), in one PR that drops the side effects
  on zero rows and changes the pinned test.
