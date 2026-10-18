# Behaviour-Change Notice: `Upsert` does the same on the six engines in 2.0

- ID: `DEP-2026-006`
- Status: `completed`
- Announced in: the first release after `v1.17.0` that carries this file — a
  documentation-only change cuts no release of its own
- Earliest change: `v2.0.0`, no earlier than `2027-01-17`
- Scope: `behaviour`
- Affected lifecycle tag: `stable`
- Owner: `@jcsvwinston`

## Summary

`Upsert` and `UpsertBatch` lower to the engine's own upsert — `ON CONFLICT`
on PostgreSQL and SQLite, `ON DUPLICATE KEY UPDATE` on MySQL and MariaDB,
`MERGE` on SQL Server and Oracle — and two of their edges mean different
things per engine. Both were measured on the six and pinned per engine in
v1 (quark#459, quark#460), and the godoc and the CRUD reference say what
each engine does:

**An empty `updateCols` on a conflict** (QK-59, seen as QK-48):

| Engine | The conflicting row |
|---|---|
| PostgreSQL, SQLite, MySQL, MariaDB | left as it was (insert-or-ignore) |
| SQL Server, Oracle | every column the insert writes is overwritten, except the conflict columns, the primary key and `created_at` |

**A duplicate of a unique key other than the conflict columns** (QK-65,
seen as QK-61): the entity carries an existing row's primary key and a
conflict-column value no row has.

| Engine | What happens |
|---|---|
| PostgreSQL, SQLite | `ErrConstraintViolation`, nothing written — `ON CONFLICT` names its arbiter |
| MySQL, MariaDB | `nil`; with `updateCols` the row that holds the duplicate is updated, whichever key it is — `ON DUPLICATE KEY UPDATE` has no conflict target; without them it is left as it was |
| SQL Server, Oracle | `nil`; a new row is inserted with a key the engine assigns — the `MERGE` matches on the conflict columns only, and the identity ignores the key the entity carried |

The owner decided on 2026-10-06 (QK-59, QK-65) that in 2.0 `Upsert` does
the same on the six engines, for both edges. One decision, one notice. The
two PRs that measured the edges left *which* behaviour to the major; this
notice proposes it, with the reason, and the 2.0 plan (`M1`) settles it.
Making the engines agree changes what callers observe on one side or the
other, which QADR-0010 keeps for the major.

## Affected surfaces

- `(*quark.Query[T]).Upsert` and `(*quark.Query[T]).UpsertBatch`, in the
  plain path and under `RowLevelSecurityClient` (`upsert_tenant.go`).

Not affected: an upsert with `updateCols` whose entity carries no key other
than the conflict columns' — the common case — which does the same on the
six engines today and keeps doing it.

## What changes at 2.0 — proposed for `M1`

1. **An empty `updateCols` is insert-or-ignore on every engine.** The
   conflicting row is left as it was, `Upsert` returns `nil`, and the
   entity's key is written back only where a row was inserted. On SQL
   Server and Oracle the `MERGE` loses its `WHEN MATCHED` branch.

   *Why this side:* it is what four of the six engines do, and it is the
   only reading that cannot overwrite data the caller did not name. A caller
   who wants every column updated names them — which is already the
   portable advice in the reference.

2. **A duplicate of a unique key other than the conflict columns is
   `ErrConstraintViolation` on every engine, nothing written.** The conflict
   columns name the key the caller asked `Upsert` to arbitrate; a duplicate
   elsewhere is the error a `Create` of the same row would be.

   *Why this side:* it is the only one of the three that writes nothing the
   caller did not ask for — MySQL and MariaDB rewrite a row addressed by a
   different key, SQL Server and Oracle insert a row and drop the key the
   entity carried — and the only one every engine can implement:
   PostgreSQL's `ON CONFLICT` names its arbiter and cannot take any-key
   semantics. The cost falls on the four engines that change: on MySQL and
   MariaDB every assignment is guarded by the conflict columns and the row
   is read back (row by row in `UpsertBatch`), the shape quark#460
   measured; on SQL Server and Oracle the `MERGE` has to see the key the
   entity carries. The statement shapes are the 2.0 PR's; the contract is
   the one above.

The tenant-guarded path keeps its own rule (QK-43): a conflicting row of
another tenant is an error wrapping `ErrConstraintViolation`, nothing
written.

## Migration path

- **Now (v1):** pass `updateCols` explicitly. An upsert that names its
  columns does the same on every engine today and does not change in 2.0.

  ```go
  // before: overwrites on SQL Server and Oracle, ignores on the other four
  err := quark.For[User](ctx, client).Upsert(&u, []string{"email"}, nil)

  // after: one behaviour, now and in 2.0
  err := quark.For[User](ctx, client).Upsert(&u, []string{"email"}, []string{"name", "updated_at"})
  ```

  For the second edge: do not carry another row's key into an upsert. A row
  you hold is written with `Update` (by its key); a new row carries a zero
  key, or `Create` it.
- **At 2.0:** an empty `updateCols` ignores the conflict everywhere; a
  duplicate on another key is `ErrConstraintViolation` everywhere.
- **Behaviour differences, from the caller's side:** on SQL Server and
  Oracle an empty `updateCols` stops overwriting; on MySQL and MariaDB a
  stale key stops updating the row it names; on SQL Server and Oracle a
  stale key stops inserting a second row. PostgreSQL and SQLite do not
  change.
- **Required app changes:** none to keep compiling. A caller on SQL Server
  or Oracle that relied on the `MERGE` overwriting names its columns before
  the upgrade; a caller on MySQL, MariaDB, SQL Server or Oracle that relied
  on a duplicate elsewhere being absorbed handles `ErrConstraintViolation`.

## Detection

- Source scan for upserts with no `updateCols`:

  ```sh
  grep -rnE '\.Upsert(Batch)?\([^)]*,\s*(nil|\[\]string\{\})\s*\)' --include='*.go' .
  ```

  Every hit changes meaning on SQL Server and Oracle. The second edge has
  no grep: it depends on what the entity carries at run time.
- Runtime signature in v1, through a `QueryObserver`: on SQL Server and
  Oracle an upsert with an empty `updateCols` emits a `MERGE` with a
  `WHEN MATCHED THEN UPDATE` branch — that branch is what disappears. On
  MySQL and MariaDB an `ON DUPLICATE KEY UPDATE` whose assignments are not
  guarded by the conflict columns.

## What breaks if this is ignored

At `v2.0.0`, on SQL Server and Oracle, an `Upsert` with an empty
`updateCols` that overwrote the conflicting row leaves it as it was: data
the caller expected written is not. On MySQL and MariaDB, an `Upsert` whose
entity carries another row's key and a fresh conflict value stops returning
`nil` and updating that row: it returns `ErrConstraintViolation` and writes
nothing. On SQL Server and Oracle the same call stops inserting a second
row, with the same error. A program that ignores the error writes nothing
where it used to; one that checks it gets one it did not get before.
Nothing changes on PostgreSQL and SQLite, nor on any engine for an upsert
that names its columns and carries no foreign key.

## Change window

- **Why `v2.0.0`:** an upsert that overwrites, updates or inserts today must
  not stop doing so within `v1.x`, and one that returns `nil` must not start
  returning an error (QADR-0010). The change lands with the major.
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

- **The v1 behaviour is pinned per engine**, in the engine suites that run
  on the six in CI (`-tags=integration`; SQLite in the same lane as the
  root module):
  `TestSuite<Engine>/UpsertEmptyUpdateCols`
  (`internal/enginesuite/upsert_empty_update_cols_test.go`) for the first
  edge, and `TestSuite<Engine>/UpsertEngineEdges/DuplicateOnAnotherKey`
  (`internal/enginesuite/upsert_engine_edges_test.go`) for the second, with
  `<Engine>` one of `Postgres`, `MySQL`, `MariaDB`, `MSSQL`, `Oracle`,
  `SQLite`:

  ```sh
  cd internal/enginesuite && go test -tags=integration -count=1 \
    -run '^TestSuitePostgres$/^(UpsertEmptyUpdateCols|UpsertEngineEdges)$' .
  ```

  Each test switches on the dialect — `overwrites` in the first,
  `duplicateKey` / `merge` in the second — and those switches are the diff
  of the 2.0 PR: every engine then takes the PostgreSQL branch.
- **In the root module** (SQLite):
  `TestUpsertWithoutUpdateColsOnAConflictIsNotAnError`
  (`upsert_empty_update_cols_test.go`),
  `TestUpsertDuplicateKeyNoOpAssignsTheColumnToItself` and
  `TestMergeInferredUpdateSetLeavesTheKeyAndCreatedAt`
  (`upsert_engine_edges_test.go`), which pin the statement shapes per
  dialect without an engine:

  ```sh
  go test -count=1 -run '^(TestUpsertWithoutUpdateColsOnAConflictIsNotAnError|TestUpsertDuplicateKeyNoOpAssignsTheColumnToItself|TestMergeInferredUpdateSetLeavesTheKeyAndCreatedAt)$' .
  ```

- **The documentation that changes with it:** the godoc of `Upsert` and
  `UpsertBatch`, the per-engine tables in
  `website/docs/reference/api/crud.mdx`, and the playbook
  (`docs/playbooks/query-builder.md`, "An empty `updateCols` is not one
  behaviour").
- **Release note:** the release that carries this file announces it.
- **Rollback plan:** not applicable in v1 — nothing changes.

## Timeline

- Decided: `2026-10-06` (QK-59, QK-65): one behaviour on the six.
- Written: `2026-10-16` (A12 `M0-Q`), with the semantics above as the
  proposal for `M1`.
- Review checkpoint: `2027-01-17`, when the window opens — and `M1`, which
  confirms or amends the proposed semantics before the PR.
- Change: at `v2.0.0` planning (`M1`), in one PR that rewrites the four
  engine-specific branches, the pinned tests and the per-engine tables.
- Applied: `dry-run/2.0`, 2026-10-18 — the semantics proposed above, as
  written: an empty `updateCols` is insert-or-ignore on the six, a duplicate
  on another unique key is `ErrConstraintViolation` on the six (MySQL and
  MariaDB guard every assignment by the conflict columns and read back a
  statement that changed nothing; SQL Server and Oracle refuse a carried
  foreign key before the `MERGE`); `UpsertBatch` goes row by row on
  MySQL/MariaDB and runs in a transaction wherever it is more than one
  statement. `Dialect.UpsertSQL` unchanged.
