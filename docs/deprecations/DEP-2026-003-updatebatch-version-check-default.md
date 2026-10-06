# Behaviour-Change Notice: `UpdateBatch` checks versions by default in 2.0

- ID: `DEP-2026-003`
- Status: `active`
- Announced in: the release that ships `Query.CheckVersions` — the next minor
  after `v1.16.0`
- Earliest change: `v2.0.0`, no earlier than `2027-01-04`
- Scope: `behaviour`
- Affected lifecycle tag: `stable`
- Owner: `@jcsvwinston`

## Summary

On a model with a `quark:"version"` field, `Update`, `UpdateFields` and
`Tracked.Save` hold the row to the version the entity was loaded with and
return `ErrStaleEntity` when it moved. `UpdateBatch` sends the same version
predicate for every row and then reads nothing back:

- a row whose version moved is not written, the rest of the batch is, and the
  call returns `nil`;
- a key that does not exist is not written and returns `nil` (`Update` returns
  `ErrStaleEntity` for it);
- the rows it did write move to the next version in the database but not in
  memory, so the next `Update` of the same struct returns `ErrStaleEntity` for
  a conflict nobody caused.

The owner decided on 2026-10-06 (QK-45) that `UpdateBatch` respects optimistic
locking: as an explicit opt-in now, and as the default in 2.0. The opt-in is
`Query.CheckVersions()`. Under it the batch is all or nothing over the
versions — every stale row is collected, the whole batch is rolled back, and
the error joins one `ErrStaleEntity` per stale row with its key; a row the
query's conditions exclude is skipped, as `Update` skips it; the written
rows' in-memory versions are bumped once the batch is through.

Nothing is deprecated, so there is no `// Deprecated:` marker for the suite
guard to read. What changes at the major is what an existing call does: a
batch that returns `nil` today returns an error in 2.0. QADR-0010 keeps that
kind of change for the major, and this notice is how it is announced.

## Affected surfaces

- `(*quark.Query[T]).UpdateBatch` on a model that carries `quark:"version"`.

Not affected: `UpdateBatch` on a model without a version column, which has
nothing to check; `Update`, `UpdateFields` and `Tracked.Save`, which already
check.

Out of this notice: the writes that send no version predicate at all —
`UpdateMap`, the update branch of `Upsert`/`UpsertBatch`, `Delete` and
`HardDelete` of an entity, the soft delete and `Restore` (QK-58). If the
decision is extended to them, the extension gets its own entry here.

## Migration path

- **Now (v1):** chain `CheckVersions()` wherever `UpdateBatch` writes a
  versioned model, and handle `ErrStaleEntity`:

  ```go
  // before
  err := quark.For[Account](ctx, client).UpdateBatch(accounts)

  // after
  err := quark.For[Account](ctx, client).CheckVersions().UpdateBatch(accounts)
  if errors.Is(err, quark.ErrStaleEntity) {
      // nothing was written: reload the accounts, replay, retry
  }
  ```

  A call written this way behaves the same before and after 2.0.
- **At 2.0:** `UpdateBatch` behaves as `CheckVersions().UpdateBatch` does in
  v1. `CheckVersions()` keeps compiling and becomes redundant for
  `UpdateBatch`.
- **Behaviour differences, from the caller's side:**
  - a stale row, or a missing key on a query without conditions, fails the
    whole batch with `ErrStaleEntity` instead of being skipped;
  - the written entities' in-memory versions are bumped, so they can be
    passed to `Update` again without a reload.
- **Required app changes:** none to keep compiling. A caller that relied on
  stale rows being skipped in silence has to decide what a conflict means for
  it, before or at the upgrade.

## Detection

- Source scan for batches that do not opt in yet:

  ```sh
  grep -rn '\.UpdateBatch(' --include='*.go' . | grep -v 'CheckVersions()'
  ```

  Only the hits whose model carries `quark:"version"` are affected.
- There is no runtime or log signature in v1: an unchecked batch that skips a
  stale row says nothing — that silence is what this notice changes.

## What breaks if this is ignored

At `v2.0.0` a batch that contains a stale row stops returning `nil`: it is
rolled back whole and returns `ErrStaleEntity`. A program that ignored the
error writes nothing where it used to write the fresh rows; a program that
checks the error gets one it did not get before. Nothing changes for a batch
whose rows are all at their loaded version, except that their in-memory
versions now move.

## Change window

- **Why `v2.0.0`:** a call that returns `nil` today must not start returning an
  error within `v1.x` (QADR-0010). The change lands with the major.
- **Why `2027-01-04`:** the suite's support policy sets a minimum of 90
  calendar days of notice. This notice is dated 2026-10-06; 90 days later is
  2027-01-04.
- The date opens the window; the version is where the change can land.
  Whichever comes second governs.

## Validation

- **The v1 default is pinned.** `TestUpdateBatchWithoutCheckVersionsKeepsTheV1Behaviour`
  (`update_batch_versions_test.go`, SQLite) and
  `UpdateBatchVersions/WithoutCheckVersionsKeepsTheV1Behaviour`
  (`internal/enginesuite/update_batch_versions_test.go`, the six engines)
  assert today's unchecked batch, false conflict on the next `Update`
  included. The 2.0 PR changes them, in the same commit as the default.
- **The opt-in is exercised** by the rest of both files: a mixed batch rolled
  back with every stale key named, a fresh batch committed with its versions
  bumped, a row excluded by the conditions skipped, and the batch inside the
  caller's transaction under a savepoint.
- **The enterprise bench measures both.** `OPS-04`
  (`internal/enterprisebench`) records the unchecked default as the open half
  of its `partial`, and the checked batch as measured; flipping the default
  moves that probe, which then has to be re-recorded.
- **Release note:** the release that ships `CheckVersions` announces it.
- **Rollback plan:** not applicable in v1 — the default does not change.

## Timeline

- Decided: `2026-10-06` (QK-45), with the opt-in.
- Review checkpoint: `2027-01-04`, when the window opens.
- Change: at `v2.0.0` planning, in one PR that flips the default, the pinned
  tests and `OPS-04`.
