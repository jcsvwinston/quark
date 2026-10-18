# Behaviour-Change Notice: the plain `LIKE` declares its escape character on every engine in 2.0

- ID: `DEP-2026-009`
- Status: `completed`
- Announced in: the first release after `v1.17.0` that carries this file — a
  documentation-only change cuts no release of its own
- Earliest change: `v2.0.0`, no earlier than `2027-01-17`
- Scope: `behaviour`
- Affected lifecycle tag: `stable`
- Owner: `@jcsvwinston`

## Summary

The plain `LIKE` forms — `Where(col, "LIKE", pattern)` and
`Where(col, "NOT LIKE", pattern)`, the typed `TypedStringColumn.Like` and
`NotLike`, and the AST `Cmp(lhs, "LIKE", Lit(pattern))` — hand the engine
the pattern as given, with no `ESCAPE` clause, under the engine's own
default. That default is not the same on the six (QK-32, born in A8 when
QK-25 was closed): PostgreSQL, MySQL and MariaDB read a backslash as an
escape character; SQLite, SQL Server and Oracle read it as a character like
any other (and SQL Server reads `[` as the start of a character class). The
same published query means different things per engine — measured on the
six on 2026-10-16: `Where("name", "LIKE", "%\%%")` finds the row named
`100% off` on PostgreSQL, MySQL and MariaDB, and the row named `back\slash`
on SQLite, SQL Server and Oracle.

A8 (`v1.15.0`) added the portable forms by addition, without touching the
plain one: `WhereLike`, `WhereNotLike`, `WhereContains`, `WhereStartsWith`,
`WhereEndsWith`; the typed `LikeEscaped`, `NotLikeEscaped`, `Contains`,
`StartsWith`, `EndsWith`; the AST `Like`, `NotLike`, `Contains`,
`StartsWith`, `EndsWith`; and `EscapeLike` to compose a pattern from user
text. Every one of them declares `ESCAPE '\'` on the statement (spelled
`'\\'` on MySQL and MariaDB, whose string literals read a backslash as an
escape), so `%` and `_` are wildcards and a backslash makes the next
character literal, on every engine. Declaring the clause on the plain form
too would have unified the meaning, and it changes what a backslash means
on three engines for every query already published — so A8 left it, and the
owner decided on 2026-10-06 (QK-32) that the plain forms declare the escape
in 2.0. QADR-0010 keeps that kind of change for the major.

## Affected surfaces

- `(*quark.Query[T]).Where` with the operators `LIKE` and `NOT LIKE` (and
  `Or`, `WhereNot` and the other builder doors that take an operator),
  rendered by `buildWhereClause` for a `SELECT` and by the four write
  renderers of `query_crud.go` — `UpdateMap`, `DeleteBy` and the writes by
  key narrow their rows through the same conditions;
- `TypedStringColumn.Like` and `TypedStringColumn.NotLike`;
- `Cmp(lhs, "LIKE", rhs)` and `Cmp(lhs, "NOT LIKE", rhs)` in the AST
  (`WhereExpr`, `OnExpr`, `Having`).

Not affected: the escaped forms listed above, which declare the clause
today and keep declaring it — a call that uses them does not change in 2.0.
A plain pattern with no backslash in it means the same before and after on
every engine; only the backslash changes meaning, and only on SQLite, SQL
Server and Oracle.

## What changes at 2.0

Every plain `LIKE` and `NOT LIKE` Quark renders declares `ESCAPE '\'`, the
way the escaped forms do, and the pattern is validated the same way: a
pattern that ends in a dangling backslash is `ErrInvalidQuery` before it
reaches the engine. One meaning on the six engines: `%` and `_` are
wildcards, `\%`, `\_` and `\\` are the literal characters.

Per engine:

| Engine | A backslash in a plain pattern today | In 2.0 |
|---|---|---|
| PostgreSQL, MySQL, MariaDB | an escape already (the engine's default) | an escape, declared — the statement text gains the tail, the rows do not change |
| SQLite, SQL Server, Oracle | a character | an escape: `\%` stops meaning "a backslash, then anything" and means a literal `%`; `\\` is one backslash; a trailing `\` is refused |

MySQL under `NO_BACKSLASH_ESCAPES`, where a bare backslash is a character
today, follows the declared escape in 2.0 like the other engines.

## Migration path

- **Now (v1):** use the escaped forms, which are the portable ones and do
  not change in 2.0.

  ```go
  // before: the engine's default decides what a backslash means
  q.Where("title", "LIKE", "%"+text+"%")

  // after: text the user typed, matched literally, on every engine
  q.WhereContains("title", text)

  // after: a pattern you composed yourself, with the escape declared
  q.WhereLike("title", "%"+quark.EscapeLike(text)+"%")

  // typed and AST counterparts
  Title.Contains(text)                      // instead of Title.Like("%"+text+"%")
  quark.Like(quark.Col("title"), pattern)   // instead of quark.Cmp(quark.Col("title"), "LIKE", quark.Lit(pattern))
  ```

  A literal backslash in a pattern is written `\\` on the escaped forms —
  which is what the plain form reads in 2.0 as well.
- **At 2.0:** the plain forms declare the escape; the escaped forms become
  the same thing under another name and keep compiling.
- **Behaviour differences, from the caller's side:** on SQLite, SQL Server
  and Oracle a plain pattern that used a backslash as a character matches
  different rows; a plain pattern ending in a backslash is refused. On
  PostgreSQL, MySQL and MariaDB the rows do not change; the statement text
  does, so a cache keyed on it misses once.
- **Required app changes:** none to keep compiling. A plain pattern with a
  backslash in it, on SQLite, SQL Server or Oracle, is rewritten to say what
  it means under the declared escape — or moved to `WhereLike`, which says
  it today.

## Detection

- Source scan for the plain forms:

  ```sh
  grep -rnE '"(NOT )?LIKE"' --include='*.go' .        # Where / Or / WhereNot / Cmp with the operator as a string
  grep -rnE '\.(Like|NotLike)\(' --include='*.go' .   # the typed plain forms
  ```

  Only the hits whose pattern can contain a backslash change meaning, and
  only on SQLite, SQL Server and Oracle; a pattern composed from user text
  can, so every hit that interpolates user text is a candidate.
- Runtime signature in v1, through a `QueryObserver`: a statement with
  `LIKE` and no ` ESCAPE '` is a plain form — the escaped ones always carry
  the tail (`TestPlainLikeIsUnchanged` asserts exactly that absence).

## What breaks if this is ignored

At `v2.0.0`, on SQLite, SQL Server and Oracle, a plain pattern that used a
backslash to mean a backslash stops matching the rows it matched: `%\%%`
stops finding `back\slash` and finds `100% off`, as it does on the other
three engines today; a pattern ending in a backslash is refused before the
engine with `ErrInvalidQuery`. On PostgreSQL, MySQL and MariaDB the rows do
not change, and the statement text gains ` ESCAPE '\'` (`'\\'` on MySQL and
MariaDB). Nothing changes for a call that already uses `WhereLike`,
`WhereContains`, `WhereStartsWith`, `WhereEndsWith` or their typed and AST
counterparts, nor for a plain pattern with no backslash in it.

## Change window

- **Why `v2.0.0`:** a published query must not change the rows it returns
  within `v1.x` (QADR-0010), and this one does on three engines. The change
  lands with the major.
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

- **The v1 behaviour is pinned**, in the root module and per engine.
  `TestPlainLikeIsUnchanged` (`like_test.go`, SQLite) asserts the plain
  form carries no `ESCAPE` tail:

  ```sh
  go test -count=1 -run '^TestPlainLikeIsUnchanged$' .
  ```

  `TestSuite<Engine>/LikeEscape/PlainFormKeepsTheEngineDefault`
  (`internal/enginesuite/suite_test.go`) asserts what the opaque pattern
  means on each engine — `100% off` on PostgreSQL, MySQL and MariaDB,
  `back\slash` on SQLite, SQL Server and Oracle — in the engine suites that
  run on the six in CI (`-tags=integration`; SQLite without it), with
  `<Engine>` one of `Postgres`, `MySQL`, `MariaDB`, `MSSQL`, `Oracle`,
  `SQLite`:

  ```sh
  cd internal/enginesuite && go test -tags=integration -count=1 \
    -run '^TestSuitePostgres$/^LikeEscape$' .
  ```

  The 2.0 PR changes both in the same commit as the clause: the root test
  asserts the tail, and the per-engine subtest asserts `100% off` on the
  six.
- **The escaped forms are exercised** by the rest of `like_test.go`
  (`TestWhereContainsMatchesTheUsersTextLiterally`,
  `TestWhereNotLikeExcludesTheLiteralMatch`,
  `TestLikeEscapeTailIsSpelledPerDialect`,
  `TestDanglingEscapeIsRefusedBeforeTheEngine`,
  `TestEscapedLikeReachesUpdateAndDelete`) and by the rest of
  `TestSuite<Engine>/LikeEscape`, which the enterprise bench reads as
  control `LIKE-07`; they do not change.
- **Release note:** the release that carries this file announces it.
- **Rollback plan:** not applicable in v1 — nothing changes.

## Timeline

- Escaped forms shipped: `v1.15.0` (A8), with the plain form untouched.
- Decided: `2026-10-06` (QK-32).
- Written: `2026-10-16` (A12 `M0-Q`).
- Review checkpoint: `2027-01-17`, when the window opens.
- Change: at `v2.0.0` planning (`M1`), in one PR that declares the clause
  on the five renderers and the AST, validates the plain pattern, and
  changes the two pinned tests.
- Applied: `dry-run/2.0`, 2026-10-18 — every `LIKE`/`NOT LIKE` the five
  renderers and the AST emit declares `ESCAPE '\'`; the pattern is validated
  at `Where`, `WhereNot`, `Having`, `HavingAggregate`, `WhereJSON`,
  `PreloadWhere`, the typed `Like`/`NotLike` and `Cmp` with a literal; the
  pinned tests flipped (`TestPlainLikeDeclaresTheEscape`,
  `LikeEscape/PlainFormDeclaresTheEscape`) and `LIKE-01/02/05` re-recorded
  with the tail as the ground.
