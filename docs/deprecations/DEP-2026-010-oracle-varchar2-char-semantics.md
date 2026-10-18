# Behaviour-Change Notice: on Oracle a sized string column counts characters in 2.0

- ID: `DEP-2026-010`
- Status: `completed`
- Announced in: the first release after `v1.17.0` that carries this file — a
  documentation-only change cuts no release of its own
- Earliest change: `v2.0.0`, no earlier than `2027-01-17`
- Scope: `behaviour` (the DDL the migration plan renders)
- Affected lifecycle tag: `stable`
- Owner: `@jcsvwinston`

## Summary

A string field with a length — `db:"name,size=40"`, a `ColumnSpec` of
`KindString` with `Size`, a string primary key, a uuid or an inet mapped to
text — is rendered by the migration plan as the engine's sized string type:
`VARCHAR(40)` on PostgreSQL, MySQL, MariaDB and SQLite, `NVARCHAR(40)` on SQL
Server, and `VARCHAR2(40)` on Oracle (`internal/migrate/engines.go`,
`engineBase` and `applySize`; the public entry points are `migrate.SQLType`,
`migrate.SQLTypeWithOpts` and `migrate.EngineColumnType`). Five engines count
that length in characters. Oracle counts `VARCHAR2(40)` in **bytes** unless the
session or the database says otherwise (`NLS_LENGTH_SEMANTICS`, `BYTE` by
default), so a value of 40 non-ASCII characters that fits everywhere else is
`ORA-12899: value too large` on Oracle (QK-50, seen in A11 `Q4`). The
introspection reads `CHAR_LENGTH` and never `CHAR_USED`, so `Diff` cannot
tell a byte column from a character column either.

The owner decided on 2026-10-16 (D7 of the suite's 2.0 contract) that in 2.0
the length means the same on the six engines: the plan renders
`VARCHAR2(40 CHAR)` on Oracle. It is a change of the DDL the plan emits, not
of the data: a column created before the upgrade keeps the semantics it was
created with until a migration touches it.

## Affected surfaces

On Oracle only:

- the type the plan renders for `KindString` with a `Size`, for a string
  primary key (`VARCHAR2(36)`), for a uuid mapped to text (`VARCHAR2(36)`) and
  for an inet mapped to text (`VARCHAR2(45)`); the sized default
  `VARCHAR2(255)` for a string without `Size`;
- so `Client.Sync`, `Client.ApplyPlan`, `PlanMigration`, `migrate diff`,
  `migrate plan --from-models` and the scaffolds the CLI writes;
- `Diff` and `migrate verify`, which in 2.0 read `CHAR_USED` and propose an
  `ALTER COLUMN` for a byte column whose model declares a length.

Not affected: `KindText` (`CLOB`), which has no length; every other engine; a
column already in the database, until a migration touches it; an Oracle
database or session whose `NLS_LENGTH_SEMANTICS` is already `CHAR`, where
`VARCHAR2(40)` and `VARCHAR2(40 CHAR)` are the same column.

## What changes at 2.0

`engineBase` and `applySize` render `VARCHAR2(n CHAR)` for Oracle wherever
they render `VARCHAR2(n)` today, the string primary key and the text forms of
uuid and inet included. The Oracle introspection reads `CHAR_USED` beside
`CHAR_LENGTH`, so `Diff` sees a `B` column under a model that declares a
length as a type change and plans the `ALTER TABLE … MODIFY (col VARCHAR2(n
CHAR))`, which Oracle runs in place. Introspection, verification and the
CLI's scaffolds change in the same commit as the renderer.

## Migration path

- **Now (v1):** there is no switch in Quark; the semantics belong to the
  session or the database. To get character semantics today, set them in
  Oracle before the migrations run:

  ```sql
  ALTER SESSION SET NLS_LENGTH_SEMANTICS = CHAR;   -- for the connection that runs the plan
  -- or, database-wide, by the DBA:
  ALTER SYSTEM SET NLS_LENGTH_SEMANTICS = CHAR SCOPE = SPFILE;
  ```

  Under `CHAR` semantics the `VARCHAR2(40)` the plan renders already counts
  characters, and the 2.0 DDL creates the same column: an application that
  runs its migrations this way sees no change at the upgrade.
- **At 2.0:** the plan writes the semantics into the DDL, so the column is a
  character column whatever the session says.
- **Behaviour differences, from the caller's side:** a new table created
  after the upgrade on a `BYTE` database accepts 40 characters, not 40
  bytes, in a `VARCHAR2(40 CHAR)`; `Diff` on an existing byte column proposes
  one `MODIFY` per column the first time it runs, and converges afterwards.
- **Required app changes:** none to keep compiling. Operators who rely on
  byte semantics on purpose (a column sized for a fixed byte budget) size the
  column accordingly or keep it out of the plan.

## Detection

- Whether the database counts bytes or characters by default:

  ```sql
  SELECT value FROM nls_database_parameters WHERE parameter = 'NLS_LENGTH_SEMANTICS';
  ```

- Which existing columns are byte columns, and will be re-planned once at 2.0:

  ```sql
  SELECT table_name, column_name, char_length, char_used
    FROM user_tab_columns
   WHERE data_type IN ('VARCHAR2', 'CHAR') AND char_used = 'B';
  ```

- In Go, the type the plan renders today for an Oracle string of length n:
  `migrate.SQLTypeWithOpts("oracle", reflect.TypeOf(""), migrate.TypeOptions{Size: n})`
  returns `VARCHAR2(n)`; from 2.0, `VARCHAR2(n CHAR)`.

## What breaks if this is ignored

Nothing fails at `v2.0.0`. The first `Diff` or `migrate diff` after the
upgrade proposes a `MODIFY` for every sized string column that is a byte
column, and a scripted migration pipeline that expects an empty plan sees a
non-empty one. A column resized that way holds more bytes than before, never
fewer, so no stored value is rejected by the change itself.

## Change window

- **Why `v2.0.0`:** the DDL a model renders is part of what a migration
  pipeline compares against; changing it within `v1.x` would make every
  Oracle `Diff` non-empty at a minor upgrade (QADR-0010).
- **Why `2027-01-17`:** the suite's support policy sets a minimum of 90
  calendar days of notice. The owner decided on 2026-10-16 and this notice
  rides the first release after `v1.17.0`, expected in the set of
  2026-10-19; 90 days later is 2027-01-17.
- The date opens the window; the version is where the change can land.
  Whichever comes second governs.

## Validation

- **The v1 rendering was pinned.** `TestOracleSizedStringsCountBytesUntilV2`
  (in `internal/migrate`, a file then named `oracle_varchar2_v1_test.go`)
  asserted `VARCHAR2(255)`,
  `VARCHAR2(40)`, `VARCHAR2(36)` and `VARCHAR2(45)` without `CHAR` through
  `SQLType`, `SQLTypeWithOpts` and `EngineColumnType`. The pre-existing
  `TestSQLType_NonPK_String`, `TestSQLType_StringPK_UUID` and
  `TestPrecisionScaleRefinesFloatsOnly` carried the same literals. The 2.0
  change flipped them in the same commit as the renderer: the pin is now
  `TestOracleSizedStringsCountCharacters`
  (`internal/migrate/oracle_varchar2_char_test.go`), which fails on any
  `VARCHAR2` without ` CHAR)`.
- **Introspection today:** `dialect_introspection.go` reads `CHAR_LENGTH`
  for `VARCHAR2`, `CHAR`, `NVARCHAR2` and `NCHAR`; the 2.0 PR adds
  `CHAR_USED` and its test on the Oracle lane of the engine matrix.
- **Release note:** the release that carries this file announces it.
- **Rollback plan:** not applicable in v1 — nothing changes. After 2.0, a
  column modified to `CHAR` semantics can be modified back by hand; the plan
  does not shrink columns.

## Timeline

- Decided: `2026-10-16` (QK-50, D7 of A12 `M0`).
- Written: `2026-10-16` (A12 `M0-Q`, quark#471).
- Review checkpoint: `2027-01-17`, when the window opens.
- Change: at `v2.0.0` planning (`M1`), in one PR that changes the renderer,
  the introspection and the pinned tests.
- Applied: `dry-run/2.0`, 2026-10-18 — `internal/migrate/engines.go` renders
  `VARCHAR2(n CHAR)` for every sized string, the string key and the text
  forms of uuid/inet; the Oracle introspection reads `CHAR_USED` and reports
  `VARCHAR2(n CHAR|BYTE)`; `normalizeType` treats a bare length as `BYTE` so
  a hand-written `VARCHAR2(n)` does not re-plan and a byte column under the
  model's form is one `MODIFY`; the Oracle lane (`OracleLengthSemantics`)
  was not run here (no engine).
