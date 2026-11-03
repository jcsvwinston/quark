# Behaviour-Change Notice: a `pkg/model` directive as a `db` tag is `ErrInvalidTag` in 2.0

- ID: `DEP-2026-011`
- Status: `active`
- Announced in: the first release after `v1.17.0` that carries this file,
  together with the `quark:"literal_column"` token that is the way out
- Earliest change: `v2.0.0`, no earlier than `2027-02-07`
- Scope: `behaviour`
- Affected lifecycle tag: `stable`
- Owner: `@jcsvwinston`

## Summary

Quark and Nucleus's `pkg/model` both read a struct tag called `db`, with two
different grammars. In Quark the tag holds the **column name**, plus
comma-separated options (`db:"email,size=255"`); in `pkg/model` it holds
**directives** separated by semicolons, the column name among them
(`db:"column:email;unique;not null"`, `db:"pk"`, `db:"readonly"`).

Since `v1.14.0` (quark#395, NU-50) Quark refuses a `db` tag whose column name
cannot be an identifier — the semicolon form — with `ErrInvalidTag` and a
message that spells the model in Quark's grammar. A tag whose column name is
exactly one of the `pkg/model` directives is a legal identifier, so `v1.14.0`
made it a warning and not an error: `Client.RegisterModel` and
`Client.Migrate` log a `WARN`, and `Migrate` creates a column with that name.
A model copied from `pkg/model` with `ID int64 \`db:"pk"\`` gets a column
called `pk` that is not the primary key; one with `db:"readonly"` gets a
column called `readonly`. The suite's re-audit of 1.42.0 measured it (QK-76)
and put the hardening on the closed list of the major: a column really
called `pk` is rare, a `db:"pk"` copied from `pkg/model` is not.

The directives are the ones `internal/schema` lists in
`nucleusDBDirectives`, compared without case: `pk`, `primarykey`,
`primary_key`, `readonly`, `read_only`, `ro`, `index`, `unique`, `tenant`,
`fk`, `required`, `autocreatetime` and `autoupdatetime`.

## Affected surfaces

- `Client.RegisterModel`, and `Client.Migrate` — so `MigrateRegistered`,
  `Sync` (which migrates each model before reading the schema, except under
  `DryRun`) and `EnableAuditLog` (which migrates the audit model) — on a
  model with a field whose `db` tag's column name is one of the directives
  above and whose `quark` tag does not carry `literal_column`.
- `ModelMeta.TagWarnings` and `ModelMeta.TagError` (`GetModelMeta`,
  `GetModelMetaByType`): the warning moves from the first to the second.

Not affected: a field with `quark:"literal_column"`, which is accepted with
no warning today and in 2.0; every other column name; the semicolon form,
which is already `ErrInvalidTag`; `PlanMigration`, `ApplyPlan` and the
CLI's `migrate … --from-models`, which surface neither tag warnings nor tag
errors in v1 (whether 2.0 extends the tag check to them is a separate
question for `M1`); Quark's own audit table, whose `pk` column carries the
token since this notice.

## What changes at 2.0

The directive-shaped column name joins the tag errors: `computeModelMeta`
records it in `TagError`, and `RegisterModel` and `Migrate` return an error
wrapping `ErrInvalidTag` that names the field, the directive and both ways
to fix it, before any DDL is sent. The `WARN` goes away.

## Migration path

- **Now (v1), if the tag meant the directive:** write it in Quark's
  grammar. The warning names the replacement for each directive:

  ```go
  // before: a column called "pk", not a key, and a WARN
  ID int64 `db:"pk"`

  // after
  ID int64 `db:"id" pk:"true"`
  ```

  `unique`, `required` and `index` become `quark:"unique"`,
  `quark:"not_null"` and `quark:"index"`;
  `fk` becomes a `rel:"belongs_to"` relation with `join:"…"`; `readonly`,
  `tenant` and the time directives have no tag in Quark (tenancy is set on
  the client, timestamps in a hook).
- **Now (v1), if the column really has that name:** add
  `quark:"literal_column"` to the field. The column and the DDL do not
  change, the warning stops, and the model stays valid in 2.0:

  ```go
  PK string `db:"pk" quark:"literal_column"`
  ```

  The token combines with the others (`quark:"not_null,literal_column"`) and
  changes nothing on a field whose column name is not a directive. The token
  is new in this release; an older Quark rejects it as an unknown token.
- **At 2.0:** the bare directive is `ErrInvalidTag`; the two forms above are
  what is accepted.
- **Behaviour differences, from the caller's side:** a program whose models
  are clean sees nothing. A program that ignored the warning stops at
  `RegisterModel` or `Migrate` with an error instead of a log line.
- **Required app changes:** none to keep compiling. Each field the warning
  names gains either the Quark spelling or `quark:"literal_column"`.

## Detection

- Runtime signature in v1: the `WARN` line in the client's logger, one per
  field each time `RegisterModel` or `Migrate` sees the model,

  ```
  level=WARN msg="struct tag looks like the Nucleus pkg/model grammar" detail="<Model>.<Field>: db:\"pk\" names a column called \"pk\". … add quark:\"literal_column\" to the field. From v2.0.0 this tag is ErrInvalidTag without it (DEP-2026-011)."
  ```

  Search the logs for `DEP-2026-011`; the message header is shared with
  other tag warnings, the id is not.
- Without running the program: `GetModelMeta[T]().TagWarnings` lists the
  same lines for a model, and a test can assert it is empty.
- Source scan, to find the candidates:

  ```sh
  grep -rnE 'db:"(pk|primarykey|primary_key|readonly|read_only|ro|index|unique|tenant|fk|required|autocreatetime|autoupdatetime)[",]' --include='*.go' -i .
  ```

## What breaks if this is ignored

At `v2.0.0` a model with a bare directive as its column name no longer
registers or migrates: `RegisterModel` and `Migrate` return `ErrInvalidTag`,
and so does `EnableAuditLog`, `Sync` or `MigrateRegistered` over such a
model. A program that starts by migrating its models stops at startup with
the error naming every offending field. Queries through `quark.For[T]` on a
model that is never registered or migrated are not checked and keep working.

## Change window

- **Why `v2.0.0`:** a model that registers and migrates today must not start
  failing within `v1.x` (QADR-0010). The change lands with the major.
- **Why `2027-02-07`:** the suite's support policy sets a minimum of 90
  calendar days of notice, counted from the set that publishes the notice.
  The re-audit put the change on the list of the major on 2026-10-27; this
  notice is dated 2026-11-03 and rides the next Quark release, expected in
  the set of 2026-11-09; 90 days later is 2027-02-07. If a later set is the
  first to pin it, the date moves with that set, and the suite's list of
  the major (`quantum/docs/planes/quantum-2.0.md` §3.0) carries the date
  that governs.
- The date opens the window; the version is where the change can land.
  Whichever comes second governs.

## Validation

- **The v1 behaviour is pinned.**
  `TestDirectiveShapedColumnNameWarnsAndMigratesInV1`
  (`tag_grammar_test.go`, SQLite):

  ```sh
  go test -count=1 -run '^TestDirectiveShapedColumnNameWarnsAndMigratesInV1$' .
  ```

  It asserts that a model with `db:"pk"` and `db:"readonly"` has no
  `TagError` and one warning per field naming the token and this notice,
  that `RegisterModel` and `Migrate` return nil and log one `WARN` per field
  each, and that the table has columns called `pk` and `readonly` a row
  round-trips through. The 2.0 PR changes it to assert `ErrInvalidTag` and
  no table, in the same commit as the error.
- **The way out is pinned.**
  `TestLiteralColumnKeepsADirectiveNamedColumnWithoutWarning`
  (`tag_grammar_test.go`) asserts that the same columns with
  `quark:"literal_column"` produce no warning, no error, no log line, the
  same `pk` and `readonly` columns, and that `not_null` beside the token
  still applies. It does not change at 2.0.
  `TestEnableAuditLogDoesNotWarnAboutItsOwnPKColumn` asserts that Quark's
  own audit model carries the token.
- **Release note:** the release that carries this file announces it.
- **Rollback plan:** not applicable in v1 — nothing changes but the text of
  the warning and the new token.

## Timeline

- Detected in v1: `v1.14.0` (quark#395), the `WARN`.
- Put on the list of the major: `2026-10-27` (A12 `R1`, QK-76).
- Written: `2026-11-03` (A12 `M0-R`), with `quark:"literal_column"` as the
  way out and the audit model opted in.
- Review checkpoint: `2027-02-07`, when the window opens.
- Change: at `v2.0.0` planning (`M1`), in one PR that moves the warning into
  `TagError` and changes the pinned v1 test.
