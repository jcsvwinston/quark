# Query bench — what Quark's typed API can and cannot express

This is the numerator of the A4 gate ("the bench of 60 queries is expressed
typed, without `RawQuery`"). It exists because that gate needs a number, and a
number needs something that produces it.

**Measured on 2026-09-11 against quark v1.13.0; updated as A4 closes gaps.** Regenerate with:

```bash
cd internal/enginesuite && go test -run TestQueryBench -v .
```

The bench is not prose. Every case is a Go function in
`internal/enginesuite/querybench_cases_test.go` that is executed against a
real database, and `TestQueryBench` asserts the **recorded verdict** rather
than success. Closing a gap therefore turns the suite red with "this one
passes now, update the verdict", which is what keeps this page honest.

## The verdicts

| verdict | meaning |
|---|---|
| **typed** | the typed API expresses it and the emitted SQL does what the case asked for |
| **wrong-sql** | it compiles and runs, but the SQL does something else — no error tells the caller |
| **no-api** | there is no way to express it: it does not compile, or it is rejected on every engine |

`wrong-sql` is a separate verdict on purpose. Counting those as passes is how
a capability matrix ends up claiming coverage it does not have — every one of
the four was found by reading the SQL the driver received, not by checking
whether the call returned an error.

## The result

**48 of 60 typed. 4 emit the wrong SQL. 8 have no API.**

| family | typed | wrong-sql | no-api |
|---|---|---|---|
| filtering | 10 | 0 | 0 |
| joins | 4 | 0 | 4 |
| aggregation | 6 | 2 | 0 |
| subquery | 6 | 1 | 0 |
| cte | 3 | 1 | 1 |
| window | 5 | 0 | 2 |
| setop | 4 | 0 | 0 |
| json | 3 | 0 | 0 |
| locking | 4 | 0 | 0 |
| writes | 3 | 0 | 1 |

Filtering, aggregation, set operations, JSON and locking are complete. The
gaps that remain cluster in joins, CTEs and windows.

### Progress

| session | typed | what it closed |
|---|---|---|
| S0 (measurement) | 44 | — |
| **S1** | **48** | the four cases that needed a function the AST would not render |

## The gaps, grouped by cause

Twelve failing cases come from **six** causes. Fixing them one case at a time
would be six times more work than fixing them one cause at a time.

### CLOSED by S1 — the four cases the AST would not render

`Func` accepts only `COUNT, SUM, AVG, MIN, MAX, LOWER, UPPER, LENGTH,
COALESCE, ABS`, and that cost Q24 (`COUNT(DISTINCT col)`), Q25 (conditional
aggregate), Q45 (`NTILE`/`PERCENT_RANK`) and Q52 (projecting a JSON member).

**The whitelist was not widened, and that was the decision.** It is a security
barrier — `Func` renders its name straight into SQL — and none of the four
cases was really asking for a longer list of names:

- `DISTINCT` is a **modifier on COUNT's argument**, not a function.
  `Func("DISTINCT", …)` put a keyword where an identifier goes. → `CountDistinct(expr)`.
- `CASE` is an **expression form with its own grammar**; no arity of
  `Func(name, args...)` renders it. → `Case().When(cond, result).Else(result)`.
- The JSON accessor is **named differently by every engine**
  (`jsonb_extract_path_text`, `JSON_EXTRACT`, `JSON_VALUE`), so a literal name
  in the whitelist would have been portable on exactly one of them. → `JSONExtract(column, path)`,
  which the dialect renders.
- The window functions are syntactically restricted to `OVER (…)` contexts the
  whitelist does not model — which is why `RowNumber` and `Rank` were already
  constructors. → `NTile`, `PercentRank`, `CumeDist`, `FirstValue`, `LastValue`, `NthValue`.

Every one emits a **constant** name, so no caller string reaches the SQL
surface: the same contract the existing window-function leaves keep.
`TestFuncWhitelistIsUnchanged` pins the ten entries, so widening the list
later is a decision someone re-takes rather than one that drifts.

**Three engine limits the bench could not see**, because it runs on SQLite.
The superapp acceptance gate exercises the same constructors against all six
engines, and that is where they surfaced — each is documented on the
constructor itself:

- **`NthValue` is not portable to SQL Server.** Five engines have
  `NTH_VALUE`; SQL Server does not, and Quark does not emulate it (an
  emulation would differ on NULLs and frames).
- **A `CASE` whose branches are all literals has no type in PostgreSQL.**
  Every `Lit()` binds as a parameter, so PostgreSQL infers `text` and
  `SUM(...)` over it fails with "function sum(text) does not exist". Give one
  branch a typed expression. The other five engines are more forgiving, which
  is what makes it easy to miss.
- **Oracle rejects a bare aggregate without `GROUP BY`** (ORA-00937), so
  `CountDistinct` needs a `GroupBy` to run there.

### 1. `With()` declares a CTE but never changes the `FROM` — 2 cases

`With("t", sub)` emits `WITH "t" AS (…) SELECT * FROM base_table`. The CTE is
declared and then ignored, unless the caller explicitly joins it (Q38, which
works). There is no `From(subquery)`.

That costs Q33 (derived table in `FROM`) and Q44 (top-N per group, which needs
to select from the windowed subquery). Q44 is the sharper illustration: the
statement runs, the CTE is there, and the engine reports "no such column:
ranked.rn" because the `SELECT` never left the base table.

### 2. `WITH RECURSIVE` emits the keyword without the recursion — 2 cases

`WithRecursive(name, sub)` takes a single subquery, and nothing can reference
the CTE being defined from inside its own body. The result is a statement that
is syntactically a recursive CTE and never recurses (Q36), and no way at all
to write the anchor-`UNION ALL`-recursive-term form (Q37).

This is the most misleading gap in the bench: the API name says the capability
is there. Walking a category tree — the canonical reason to reach for a
recursive CTE — cannot be done.

### 3. `For[T]` derives the `FROM` table from `T` — 1 case

A join cannot be projected onto a DTO (Q17): `For[OrderEmail]` selects `FROM
order_emails`, a table that does not exist. There is no way to say "read from
`orders`, scan into this struct". Any query whose result shape is not exactly
one registered model has to drop to `RawQuery`.

### 4. `Preload` has no per-relation condition — 1 case

`Preload(relations ...string)` is variadic over relation *names*. Q16 shows
the trap: `Preload("Orders", "status = ?")` compiles, and the extra argument
is read as a second relation name. It then fails with "relation not found" —
but **only once the parent query returns rows**, so against an empty table the
mistake is silent.

### 5. Windows have no frame clause — 1 case

`Window` exposes `PartitionBy` and `OrderBy` only (Q43). Omitting the frame is
not a smaller version of the query: the default frame runs from the start of
the partition to the current row, so a moving average silently becomes a
running one.

### 6. Four one-offs

- **Q14** — a literal cannot ride in a `JOIN … ON` clause; `OnRaw` accepts
  identifier-to-identifier conditions only. Moving the literal to `WHERE` is
  equivalent for an `INNER JOIN` and changes the result set for a `LEFT JOIN`.
- **Q18** — `Join`, `LeftJoin` and `RightJoin` are the whole set: no
  `FullJoin`, no `CrossJoin`.
- **Q60** — `UpdateMap` binds its values as driver arguments, so `stock =
  stock - 1` cannot be written; an `Expr` reaches `database/sql` as a struct.
  Read-modify-write is not equivalent — it loses the atomicity that is the
  reason to write the SQL form.
- **Q22 / Q23** — `GROUP BY` without a `Select()` leaves the projection as
  `SELECT *`, which is invalid with `GROUP BY` on every engine except SQLite
  and MySQL in its permissive mode. These two are the reason the `wrong-sql`
  verdict exists: they pass on the engine the bench runs on and would fail in
  production on PostgreSQL.

## Three things the bench found that are not gaps

Worth recording, because each one cost a wrong first attempt:

- **`IS NULL` is the operator, not `IS` with a nil value.** `Where("col",
  "IS", nil)` emits `col IS ?`, which SQLite accepts and PostgreSQL rejects as
  a syntax error. `Where("col", "IS NULL", nil)` is the spelling.
- **Correlated subqueries need `WhereExpr(Eq(Col, Col))`.** `Where("a", "=",
  Col("b"))` is rejected, because the value side is validated as a literal.
  With the right spelling, `EXISTS`, `NOT EXISTS` and correlated scalar
  subqueries in the projection all work (Q29-Q31).
- **JSON paths are dotted identifier chains, not JSONPath.**
  `WhereJSON("attrs", "dims.width", ">", 10)` works; `"$.dims.width"` is
  rejected. Filtering on JSON is supported; projecting from it is not (Q52).

## What this does not measure

- **Engines other than SQLite.** The bench runs on SQLite so it is cheap
  enough to be a gate. Cases whose answer depends on the engine say so in
  their `Note`, and the two `wrong-sql` aggregation cases are exactly the ones
  that would change verdict on PostgreSQL. Pointing the bench at the other
  five engines is worth doing and has not been done.
- **Row-level locking on the engines that have it.** Family I is marked typed
  on the strength of `locking_test.go`, which exercises `FOR UPDATE`,
  `SKIP LOCKED` and `NOWAIT` against PostgreSQL, MySQL and Oracle. SQLite
  rejects them with a clear `ErrUnsupportedFeature`, which is the right answer
  rather than a gap.
- **Performance.** Every verdict here is about expressiveness. Whether the
  emitted SQL is a good plan is a different measurement.
