# Behaviour-Change Notice: every write of a versioned model joins optimistic locking in 2.0

- ID: `DEP-2026-004`
- Status: `active`
- Announced in: the first release after `v1.17.0` that carries this file — a
  documentation-only change cuts no release of its own
- Earliest change: `v2.0.0`, no earlier than `2027-01-17`
- Scope: `behaviour`
- Affected lifecycle tag: `stable`
- Owner: `@jcsvwinston`

## Summary

On a model with a `quark:"version"` field, `Update`, `UpdateFields` and
`Tracked.Save` hold the row to the version the entity was loaded with, return
`ErrStaleEntity` when it moved, and bump the version they write. `UpdateBatch`
joins them under `CheckVersions`, and by default in 2.0
([`DEP-2026-003`](DEP-2026-003-updatebatch-version-check-default.md)). The
other writes of a versioned model do neither (QK-58, found while
investigating QK-45):

- `UpdateMap` sends no version predicate and leaves the version column where
  it was, so its write is invisible to anyone holding the row: their next
  `Update` sees no conflict and overwrites it;
- the update branch of `Upsert` and `UpsertBatch` writes a stale entity's
  columns over the winner's, and the version stays where the winner left it;
- `Delete` and `HardDelete` of an entity match on the key alone, so a stale
  entity removes the row another writer moved — what the enterprise bench
  records as the open half of `OPS-04`;
- the soft delete and `Restore` write `deleted_at` without a version
  predicate and without bumping the version.

The owner decided on 2026-10-06 (QK-58), following QK-45, that in 2.0 these
writes join optimistic locking. Nothing is deprecated and no symbol goes
away; what changes is what an existing call does with a stale entity, which
QADR-0010 keeps for the major.

## Affected surfaces

On a model that carries `quark:"version"`:

- `(*quark.Query[T]).UpdateMap`;
- `(*quark.Query[T]).Upsert` and `(*quark.Query[T]).UpsertBatch`, their
  update branch;
- `(*quark.Query[T]).Delete` and `(*quark.Query[T]).HardDelete` of an
  entity, the soft delete included;
- `(*quark.Query[T]).Restore`.

Not affected: a model without a version column, which has nothing to check;
`Update`, `UpdateFields`, `Tracked.Save` and `UpdateBatch` under
`CheckVersions`, which already check; `DeleteBy` and `DeleteBatch`, which
address rows by conditions or by a list of keys and hold no entity whose
version could be stale (they keep writing without a predicate; the owner's
decision names the writes that carry an entity).

## What changes at 2.0

- `Delete`, `HardDelete`, the soft delete and `Restore` of a versioned entity
  carry `AND version = <loaded version>`. A statement that matches no row
  returns `ErrStaleEntity` with the key, nothing removed or restored; under
  conditions the same probe `Update` uses today tells a row the conditions
  exclude — `(0, nil)` — from a conflict (QK-40). The soft delete and
  `Restore` also bump the version they write, in the row and in memory.
- The update branch of `Upsert` and `UpsertBatch` bumps the version it
  writes and, when the entity carries a version, holds the row to it: a
  conflicting row that moved is `ErrStaleEntity`, nothing written. An entity
  with a zero version is treated as new, as today. The statement shape per
  engine (`ON CONFLICT … DO UPDATE … WHERE`, `ON DUPLICATE KEY UPDATE` with
  a guarded assignment, `MERGE … WHEN MATCHED AND`) is for the 2.0 PR to
  settle; the contract is the one above.
- `UpdateMap` has no entity and no loaded version, so it checks nothing — but
  it bumps the version column it leaves alone today, so that a holder of the
  row sees the write on their next `Update` (`ErrStaleEntity`) instead of
  overwriting it. Proposed for the 2.0 PR: a map that names the version
  column is `ErrInvalidQuery`, as `UpdateFields` refuses the primary key.

## Migration path

- **Now (v1):** the exit exists for the writes by key, because a query's
  conditions are ANDed with the key (QK-40): hold the row to its version
  yourself and read zero rows as the conflict.

  ```go
  // before
  n, err := quark.For[Account](ctx, client).Delete(&acc)

  // after: the 2.0 behaviour, by hand
  n, err := quark.For[Account](ctx, client).Where("version", "=", acc.Version).Delete(&acc)
  if err == nil && n == 0 {
      // the row moved (or is gone): reload, decide, retry
  }
  ```

  The same `Where` works for `HardDelete`, the soft delete and `Restore`.
  For `UpdateMap`, name the version in both halves:
  `Where("version", "=", v).UpdateMap(map[string]any{"balance": b, "version": v + 1})`.
  For `Upsert` on a versioned row there is no v1 switch: use `Update` for a
  row you hold (it checks) and `Create` for a new one.
- **At 2.0:** the writes above check and bump on their own. A `Where` on the
  version keeps compiling and becomes redundant.
- **Behaviour differences, from the caller's side:** a write with a stale
  entity returns `ErrStaleEntity` instead of `(1, nil)` or `nil`; a write
  that goes through moves the version, in the row and in the entity.
- **Required app changes:** none to keep compiling. A caller whose delete or
  upsert "always wins" today has to decide what a conflict means for it.

## Detection

- Source scan: the versioned models, then the writes on them.

  ```sh
  grep -rln 'quark:"version"' --include='*.go' .
  grep -rnE '\.(UpdateMap|Upsert|UpsertBatch|Delete|HardDelete|Restore)\(' --include='*.go' .
  ```

  Only the hits whose model carries `quark:"version"` are affected, and
  among those only the calls that can run with a stale entity.
- There is no runtime or log signature in v1: a stale delete that removes
  the winner's row says nothing. The enterprise bench (`OPS-04`,
  `internal/enterprisebench`) records the `Delete` half as `partial`.

## What breaks if this is ignored

At `v2.0.0` a `Delete`, `HardDelete`, soft delete or `Restore` with a stale
entity stops returning `(1, nil)`: it removes or restores nothing and
returns `ErrStaleEntity`. An `Upsert` with a stale entity stops overwriting
the winner's row and returns `ErrStaleEntity`. After an `UpdateMap`, a holder
of the row gets `ErrStaleEntity` on their next `Update` where they used to
overwrite the map's write. A program that ignores the error deletes, restores
or writes nothing where it used to; a program that checks it gets one it did
not get before. Nothing changes for a write whose entity is at the row's
version, except that the version now moves.

## Change window

- **Why `v2.0.0`:** a call that returns `(1, nil)` today must not start
  returning an error within `v1.x` (QADR-0010). The change lands with the
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
  `TestWritesOutsideOptimisticLockingKeepTheV1Behaviour`
  (`optimistic_locking_scope_test.go`, SQLite):

  ```sh
  go test -count=1 -run '^TestWritesOutsideOptimisticLockingKeepTheV1Behaviour$' .
  ```

  One subtest per surface — `UpdateMap`, `UpsertUpdateBranch`,
  `UpsertBatchUpdateBranch`, `DeleteOfAStaleEntity`,
  `HardDeleteOfAStaleEntity`, `SoftDeleteAndRestoreOfAStaleEntity` — each
  asserting today's write with a stale entity, version left where it was.
  The 2.0 PR changes them in the same commit as the predicates, and adds the
  per-engine statement shapes to `internal/enginesuite`.
- **The checked writes are exercised** by `where_guard_test.go`
  (`TestVersionedWritesTellAConflictFromAnExclusion`) and
  `update_batch_versions_test.go`, which the 2.0 PR extends to the surfaces
  above.
- **The enterprise bench measures the gap.** `OPS-04` records the `Delete`
  half as the open half of its `partial`; closing it moves that probe, which
  then has to be re-recorded.
- **Release note:** the release that carries this file announces it.
- **Rollback plan:** not applicable in v1 — nothing changes.

## Timeline

- Decided: `2026-10-06` (QK-58), with QK-45.
- Written: `2026-10-16` (A12 `M0-Q`).
- Review checkpoint: `2027-01-17`, when the window opens.
- Change: at `v2.0.0` planning (`M1`), in one PR that adds the predicates,
  changes the pinned tests and re-records `OPS-04`.
