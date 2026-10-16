# Behaviour-Change Notice: `ON DELETE SET DEFAULT` means the same on the six engines in 2.0

- ID: `DEP-2026-007`
- Status: `active`
- Announced in: the first release after `v1.17.0` that carries this file — a
  documentation-only change cuts no release of its own
- Earliest change: `v2.0.0`, no earlier than `2027-01-17`
- Scope: `behaviour`
- Affected lifecycle tag: `stable`
- Owner: `@jcsvwinston`

## Summary

A foreign key declares its referential actions — `ForeignKey.OnDelete` and
`OnUpdate` in a `Schema`, the `onDelete`/`onUpdate` arguments of
`Client.AddForeignKey` — and the dialect writes them as its engine takes
them (`quarkdriver.ReferentialActioner`, QK-49). `SET DEFAULT` is where the
engines part (QK-60), measured on the six on 2026-10-16:

| Engine | `ON DELETE SET DEFAULT` |
|---|---|
| PostgreSQL, SQLite, SQL Server | written and run: deleting the referenced row writes the column's default into the rows that referenced it |
| MySQL, MariaDB | written, accepted, and not run: InnoDB refuses the delete, as `NO ACTION` would, and nothing says so. MySQL's catalog echoes `SET DEFAULT`, so the lie survives introspection; MariaDB's records `RESTRICT`, so `Diff` of a schema that declares `SET DEFAULT` re-plans the key — `DROP FOREIGN KEY` and `ADD FOREIGN KEY` — on every run, and the loop never converges |
| Oracle | no such clause; the dialect refuses it before anything is sent, with `ErrUnsupportedFeature` naming the key and the clause |

The owner decided on 2026-10-06 (QK-60) that in 2.0 the action means the
same on the six. The plan is to **refuse** it on MySQL and MariaDB the way
Oracle refuses it, not to emulate it. Refusing today would fail a statement
that succeeds, which QADR-0010 keeps for the major.

## Affected surfaces

On MySQL and MariaDB, a foreign key with `SET DEFAULT` in either clause:

- `Client.AddForeignKey`;
- `Client.ApplyPlan`, for an `OpAddForeignKey` and for the foreign keys of
  an `OpCreateTable`, and so `Client.Sync` and the CLI's `migrate` commands
  that apply a plan;
- `quarkdriver.ReferentialActioner`, which the MySQL dialect (MariaDB
  delegates to it) does not implement today and implements in 2.0.

Not affected: the other four actions on MySQL and MariaDB; every action on
the other four engines; a foreign key already in the database, which keeps
whatever its catalog holds.

## What changes at 2.0

The MySQL dialect answers `ActionUnsupported` for `SET DEFAULT` in both
clauses. `AddForeignKey` and `ApplyPlan` then fail with an error wrapping
`ErrUnsupportedFeature` that names the key and the clause, and send
nothing — `ApplyPlan` asks about every key of the plan before it runs the
first operation, so a plan with one such key changes nothing. This is what
the Oracle dialect does today for `ON DELETE SET DEFAULT`, `ON DELETE
RESTRICT` and every `ON UPDATE` action but `NO ACTION`.

**Why refuse rather than emulate.** Emulating `SET DEFAULT` on InnoDB means
a trigger per key — `BEFORE DELETE` on the parent, writing the default into
the children — which the catalog does not list as part of the key, `Diff`
cannot see, and the migration loop would then have to own. Quark declares
an engine's gaps instead of papering over them: `FULL OUTER JOIN` is refused
on MySQL and MariaDB, not rewritten, and Oracle's missing actions are
refused, not simulated. Refusing turns a silent lie — a schema that says
`SET DEFAULT` and an engine that does `NO ACTION` — into an error at plan
time, before any statement runs.

## Migration path

- **Now (v1):** there is no switch — in v1 the clause keeps being written.
  Replace the action where the schema targets MySQL or MariaDB:

  ```go
  // before: accepted by InnoDB, run as NO ACTION; re-planned on MariaDB on every diff
  fk := quark.ForeignKey{Columns: []string{"parent_id"}, RefTable: "parents", RefColumns: []string{"id"}, OnDelete: "SET DEFAULT"}

  // after: what the engine does today, said out loud — nothing changes at run time
  fk := quark.ForeignKey{Columns: []string{"parent_id"}, RefTable: "parents", RefColumns: []string{"id"}, OnDelete: "NO ACTION"}

  // or the action that runs on all six, if the column may be cleared
  fk.OnDelete = "SET NULL"
  ```

  A schema shared across engines keeps `SET DEFAULT` where it runs and
  declares `NO ACTION` or `SET NULL` for MySQL and MariaDB.
- **At 2.0:** `SET DEFAULT` on MySQL and MariaDB is `ErrUnsupportedFeature`,
  nothing sent.
- **Behaviour differences, from the caller's side:** on MySQL and MariaDB a
  plan, an `AddForeignKey` or a `CREATE TABLE` that declares the action
  stops applying and returns an error naming it. On MariaDB a `Diff` that
  re-planned the key on every run stops, because the desired schema no
  longer declares an action the engine cannot hold.
- **Required app changes:** none to keep compiling. Schemas and migrations
  that declare `SET DEFAULT` for MySQL or MariaDB change the action before
  the upgrade.

## Detection

- Source scan, Go and SQL:

  ```sh
  grep -rniE 'SET DEFAULT' --include='*.go' --include='*.sql' .
  ```

  Among the Go hits, `OnDelete:`/`OnUpdate:` fields and the last two
  arguments of `AddForeignKey`; a column default (`ALTER COLUMN … SET
  DEFAULT`) is not a referential action and is not affected. Only schemas
  that target MySQL or MariaDB change.
- Runtime signature in v1: on MariaDB, a `PlanMigration` or `Diff` that
  proposes `DROP FOREIGN KEY` and `ADD FOREIGN KEY` for the same key on
  every run. On MySQL there is none: the catalog echoes the clause. The
  introspected `ForeignKey.OnDelete` says `SET DEFAULT` on MySQL and
  `RESTRICT` on MariaDB for the same declaration.

## What breaks if this is ignored

At `v2.0.0`, on MySQL and MariaDB, a plan, an `AddForeignKey` or a
`CREATE TABLE` that declares `SET DEFAULT` fails with
`ErrUnsupportedFeature` before anything is sent, and a migration that used
to apply stops applying until the action is changed. Foreign keys already
in the database are untouched. Nothing changes on PostgreSQL, SQLite, SQL
Server or Oracle, nor for any other action.

## Change window

- **Why `v2.0.0`:** a statement that succeeds today must not start failing
  within `v1.x` (QADR-0010), even when what it declares is not what the
  engine does. The change lands with the major.
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

- **The v1 behaviour is pinned per engine.** `TestSuite<Engine>/OnDeleteSetDefault`
  (`internal/enginesuite/on_delete_set_default_test.go`), in the engine
  suites that run on the six in CI (`-tags=integration`; SQLite without
  it), with `<Engine>` one of `Postgres`, `MySQL`, `MariaDB`, `MSSQL`,
  `Oracle`, `SQLite`:

  ```sh
  cd internal/enginesuite && go test -tags=integration -count=1 \
    -run '^TestSuiteMySQL$/^OnDeleteSetDefault$' .
  ```

  The test switches on the dialect — `runsIt`, `acceptsAndIgnores`,
  `refuses` — and the MySQL and MariaDB branch (`acceptsAndIgnores`: the
  key is accepted, the delete is refused, the child keeps its value) is the
  diff of the 2.0 PR: both engines then take the Oracle branch. It also logs
  what each catalog says and whether `Diff` re-plans the applied schema,
  which is how the MariaDB row of the table above was measured.
- **The dialect's answers are pinned without an engine.**
  `TestReferentialClauses` (`referential_actions_test.go`, root module),
  whose `mysql writes all` case gains a refusal in 2.0:

  ```sh
  go test -count=1 -run '^TestReferentialClauses$' .
  ```

- **The driver kit follows the answer.** `checkReferentialActioner`
  (`quarkdriver/drivertest/dialect_schema.go`) judges whatever the dialect
  answers against the engine — an action the dialect refuses never reaches
  it — so the kit needs no change when the MySQL dialect starts answering.
- **Release note:** the release that carries this file announces it.
- **Rollback plan:** not applicable in v1 — nothing changes.

## Timeline

- Decided: `2026-10-06` (QK-60): the same on the six.
- Written: `2026-10-16` (A12 `M0-Q`), with "refuse, as Oracle does" as the
  mechanism.
- Review checkpoint: `2027-01-17`, when the window opens.
- Change: at `v2.0.0` planning (`M1`), in one PR that gives the MySQL
  dialect its `ReferentialActioner` and changes the pinned tests.
