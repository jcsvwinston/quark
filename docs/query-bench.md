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

**58 of 60 typed. 2 emit the wrong SQL. 0 have no API.**

| family | typed | wrong-sql | no-api |
|---|---|---|---|
| filtering | 10 | 0 | 0 |
| joins | 8 | 0 | 0 |
| aggregation | 6 | 2 | 0 |
| subquery | 7 | 0 | 0 |
| cte | 5 | 0 | 0 |
| window | 7 | 0 | 0 |
| setop | 4 | 0 | 0 |
| json | 3 | 0 | 0 |
| locking | 4 | 0 | 0 |
| writes | 4 | 0 | 0 |

**Every query in the bench can now be written with the typed API.** The two
that remain are not a missing capability — they run, and emit SQL that is
portable only to SQLite.

### Progress

| session | typed | what it closed |
|---|---|---|
| S0 (measurement) | 44 | — |
| S1 | 48 | the four cases that needed a function the AST would not render |
| S2 | 52 | `FromCTE`, plus two cases S0 had measured wrong |
| **S3** | **58** | the six one-offs: DTO projection, filtered preload, window frames, arithmetic in `SET`, `OnExpr`, `FullJoin`/`CrossJoin` |

## What remains

Two cases, one cause, and it is not a missing capability.

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

### CLOSED by S2 — reading from a CTE, and two cases S0 got wrong

`With("t", sub)` declares the CTE and leaves the outer `SELECT` on the base
table. Joining the CTE reaches it, which is the right shape when you want rows
from both — but not when the derived rows **are** the query. `FromCTE(name)`
replaces the model's table in the `FROM`, and closes Q33.

**Two of the four cases in this group were never broken.** S0 recorded them as
gaps and was wrong, which is worth writing down plainly:

- **Q36/Q37, the recursive CTE.** A recursive body is
  *anchor* `UNION ALL` *step*, and `UnionAll(...).AsSubquery()` has composed
  exactly that since set operators landed. What S0 read was the comment on
  `WithRecursive`, which still said the typed subquery surface could not model
  `UNION` — stale long enough to outlive the thing it described. The
  measurement then wrote the case the way the comment implied, got a
  non-recursive statement, and recorded the capability as missing. The comment
  is corrected, and Q37 now asserts a **four-level** tree comes back whole: a
  two-level tree cannot tell recursion from a single join.
- **Q44, top-N per group.** Joining the ranked CTE already worked. S0's
  version failed because it declared the CTE and never referenced it.

The lesson generalises past this bench: a measurement that trusts a comment
measures the comment. Both cases now run against a real database and assert
their result, not just their SQL.

### `GROUP BY` without a projection — 2 cases

`GroupBy` with no `Select` or `SelectExpr` leaves the projection as
`SELECT *`. That is invalid with `GROUP BY` on every engine except SQLite and
MySQL in its permissive mode: the non-aggregated columns are not functionally
dependent on the grouping key.

It passes in development and fails on deploy, which is the worst shape a
defect can take, so **S3 made it WARN**, naming the fix. Making it an *error*
is the right end state and breaks callers relying on the permissive engines,
so it waits for the major that QADR-0010 collects breaking changes into.
Adding `Select(...)` makes the query correct today.

### CLOSED by S3 — the six one-offs

| case | what it needed |
|---|---|
| Q17, projecting a join onto a DTO | `FromTable` — `For[T]` takes the source table from `T`, so a result-shape struct has to name its own |
| Q16, filtered eager load | `PreloadWhere` — `Preload` is variadic over names, so a condition could not ride in it |
| Q43, window frame | `Window.Rows` / `Range` with typed bounds |
| Q60, `stock = stock - 1` | `UpdateMap` reading an `Expr` value as SQL, plus `Add`/`Subtract`/`Multiply`/`Divide` |
| Q14, literal in `ON` | `OnExpr` — an AST clause binds its literals, so the identifier-only grammar that polices caller strings does not apply |
| Q18, full and cross joins | `FullJoin`, `CrossJoin` |

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
