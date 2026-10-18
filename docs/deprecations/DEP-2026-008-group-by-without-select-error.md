# Behaviour-Change Notice: `GroupBy` without a projection is an error in 2.0

- ID: `DEP-2026-008`
- Status: `completed`
- Announced in: the first release after `v1.17.0` that carries this file — a
  documentation-only change cuts no release of its own
- Earliest change: `v2.0.0`, no earlier than `2027-01-17`
- Scope: `behaviour`
- Affected lifecycle tag: `stable`
- Owner: `@jcsvwinston`

## Summary

`GroupBy` without `Select` or `SelectExpr` leaves the projection at
`SELECT *`, so the statement is `SELECT * FROM t GROUP BY col`. PostgreSQL,
SQL Server, Oracle and MySQL in its default strict mode
(`ONLY_FULL_GROUP_BY`) reject it: the non-aggregated columns are not
functionally dependent on the grouping key. SQLite and MySQL in its
permissive mode run it and return an arbitrary row per group. The query
passes in development on SQLite and fails on deploy, which is the worst
shape a defect can take (QK-24, measured by the query bench as cases Q22 and
Q23).

Since quark#392 (`v1.14.0`) the builder says so: when a query carries a
`GROUP BY` and no projection, `buildSelect` logs a `WARN` that names the fix
and runs the query anyway. That is the detection in v1. Making it an error
is the right end state and breaks the callers who rely on the permissive
engines, so the owner decided on 2026-10-06 (QK-24) that it becomes an
error in 2.0. QADR-0010 keeps that kind of change for the major.

## Affected surfaces

- `(*quark.Query[T]).GroupBy` on a query with no `Select` and no
  `SelectExpr`, on every path that renders the statement through
  `buildSelect`: `List`, `First`, `Find`, `Cursor`, `Paginate`,
  `PaginateAfter`, and a query used as a subquery or a CTE body.

Not affected: a `GroupBy` with a `Select` naming the grouped columns, or a
`SelectExpr` for each aggregate, which is correct on every engine today and
does not change; `Count` and the aggregates (`Sum`, `Avg`, `Min`, `Max`),
which render their own projection.

## What changes at 2.0

A query with `GroupBy` and no projection returns an error wrapping
`ErrInvalidQuery` from the builder, before any statement is sent, on every
engine — SQLite and permissive MySQL included. The `WARN` goes away: there
is nothing left to warn about.

## Migration path

- **Now (v1):** name the projection. With it the query is correct on every
  engine today and does not change in 2.0.

  ```go
  // before: SELECT * … GROUP BY "status" — rows on SQLite, an error on PostgreSQL, a WARN from Quark
  rows, err := quark.For[Order](ctx, client).GroupBy("status").List()

  // after: the grouped column, and the aggregate as an expression
  rows, err := quark.For[Order](ctx, client).
      Select("status").
      SelectExpr("total", quark.Func("COUNT", quark.Col("id"))).
      GroupBy("status").
      List()
  ```

  A result whose shape is not the model's goes through `FromTable` or a
  DTO, as the querying guide shows.
- **At 2.0:** the unprojected query is `ErrInvalidQuery`.
- **Behaviour differences, from the caller's side:** on SQLite and
  permissive MySQL a query that returned one arbitrary row per group returns
  an error; on the other four engines the engine's error becomes Quark's and
  arrives before the statement.
- **Required app changes:** none to keep compiling. Every `GroupBy` gains
  the `Select` or `SelectExpr` it needs — which is what the `WARN` has been
  asking for since `v1.14.0`.

## Detection

- Runtime signature in v1: the `WARN` line in the client's logger,

  ```
  GROUP BY without Select() or SelectExpr() emits SELECT *, which PostgreSQL, SQL Server, Oracle and MySQL in strict mode reject … table=<table> group_by=<columns>
  ```

  one per query built, with the table and the grouped columns as
  attributes. Count them in the logs of a run that covers the application's
  queries; each distinct `table`/`group_by` pair is a call site to fix.
- Source scan, to find the candidates: every `GroupBy(` whose chain carries
  no `Select(` or `SelectExpr(`:

  ```sh
  grep -rn 'GroupBy(' --include='*.go' .
  ```

  Chains span lines, so the hits are reviewed by hand; the log is the
  reliable detector.

## What breaks if this is ignored

At `v2.0.0` a `GroupBy` without a projection stops returning rows on SQLite
and permissive MySQL and returns `ErrInvalidQuery` from `List`, `First` and
the other paths above. A program that only ran on those engines, and
ignored the warning, stops getting results from those queries until it
names the projection. On the other four engines the query already failed;
the error changes from the engine's to Quark's, so a program that matched
the engine's error text has to match `ErrInvalidQuery` instead.

## Change window

- **Why `v2.0.0`:** a query that returns rows today on two engines must not
  start returning an error within `v1.x` (QADR-0010). The change lands with
  the major.
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

- **The v1 behaviour is pinned.** `TestGroupByWithoutSelectWarnsAndRunsInV1`
  (`group_by_projection_test.go`, SQLite):

  ```sh
  go test -count=1 -run '^TestGroupByWithoutSelectWarnsAndRunsInV1$' .
  ```

  It asserts that the unprojected query runs on SQLite and logs the `WARN`
  with its `table` and `group_by` attributes, and that the same query with
  `Select` or `SelectExpr` runs with nothing logged. The 2.0 PR changes its
  first half to assert `ErrInvalidQuery` and no log line, in the same commit
  as the error.
- **The query bench records the gap.** Cases Q22 and Q23 of
  `internal/enginesuite/querybench_cases_test.go` are `wrong-sql` for this
  reason, and `docs/query-bench.md` counts them; the 2.0 PR moves them,
  which changes the count.
- **Release note:** the release that carries this file announces it.
- **Rollback plan:** not applicable in v1 — nothing changes.

## Timeline

- Detected in v1: `v1.14.0` (quark#392), the `WARN`.
- Decided: `2026-10-06` (QK-24).
- Written: `2026-10-16` (A12 `M0-Q`).
- Review checkpoint: `2027-01-17`, when the window opens.
- Change: at `v2.0.0` planning (`M1`), in one PR that turns the warning
  into the error and changes the pinned test and the bench cases.
- Applied: `dry-run/2.0`, 2026-10-18 — `buildSelect` returns an error
  wrapping `ErrInvalidQuery` on every path that renders the statement; the
  `WARN` is gone; `TestGroupByWithoutSelectIsRefused`, Q22/Q23 projected and
  the query bench at 60/0/0. `Paginate` still sends its `COUNT` first (its
  own projection) and refuses the grouped `SELECT` after it.
