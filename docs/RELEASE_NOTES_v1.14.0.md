# Release notes — v1.14.0

The query bench goes from 44 to 58 of 60 queries expressible with the typed
API, and the generated integer and float types stop being too narrow for the
Go types they came from.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>

## Changed

### Queries that needed RawQuery, and no longer do

`quark/docs/query-bench.md` measures which of 60 real-world queries the typed
API can express. It went from **44 to 58**, and the sixteen that were missing
turned out to come from seven causes rather than sixteen problems.

**Functions the AST would not render.** `CountDistinct`, `Case` (a searched
`CASE WHEN … THEN … ELSE … END`), `JSONExtract`, and the window-function
leaves `NTile`, `PercentRank`, `CumeDist`, `FirstValue`, `LastValue` and
`NthValue`. The ten-entry function whitelist was **not** widened: it is a
security barrier — `Func` renders its name straight into SQL — and none of
these was really asking for more names. `DISTINCT` is a modifier, `CASE` has
its own grammar, and the JSON accessor is spelled differently by every engine,
so the dialect renders it.

**Reading from something other than the model's table.** `FromCTE` selects
from a CTE declared with `With`, for when the derived rows *are* the query;
`FromTable` names the source for a projection DTO, which `For[T]` used to send
at a table named after the struct.

**The rest.** `PreloadWhere` filters which related rows load. `Window.Rows` and
`Window.Range` set the frame — without one, a "moving average" was silently a
running average. `UpdateMap` reads an `Expr` value as SQL, so `stock = stock -
1` is expressible with `Subtract` and keeps its atomicity. `OnExpr` puts a
literal in a `JOIN … ON`, which matters for an outer join: moving the same
predicate to `WHERE` turns a `LEFT JOIN` into an inner one. `FullJoin` and
`CrossJoin` complete the set.

### Migrations can be rolled back

`Plan.Down()` returns the plan that undoes one: each operation inverted, in
reverse order. Operations that record a name but not the shape behind it —
`DROP TABLE`, `DROP COLUMN` — return `ErrIrreversibleOperation` naming what is
missing, rather than emitting a partial rollback that reports success and
leaves the schema subtly different.

### A model written for another tag grammar is refused

Quark and Nucleus's `pkg/model` both read a tag called `db` and disagree about
what is in it. A model written the other way produced a Quark column called
literally `column:email;unique;not null` — no error, no warning, and a table
nobody could query. A `db` tag whose column name cannot be an identifier is
now rejected, with an error that shows the Quark spelling. A name that *is* a
legal identifier but reads like a directive (`db:"pk"`) warns instead: a table
really can have a column called `pk`.

## Fixed

### Integer and float columns were too narrow

Every Go integer width mapped to a single `INTEGER`, which is four bytes on
PostgreSQL, MySQL and SQL Server. An `int64` past 2³¹ was **rejected** by those
engines — MySQL says `Out of range value for column` — while looking fine on
SQLite, whose `INTEGER` is dynamically sized. Auto-increment keys were `SERIAL`
and `INT` for the same reason and ran out at 2,147,483,647 rows. `float64`
mapped to `REAL`, which is single precision on PostgreSQL: about seven
significant digits where a `float64` carries fifteen.

Integers now map by width, floats take the double-precision type per engine,
and integer keys are 64-bit everywhere. The full matrix is published at
[Type matrix](https://jcsvwinston.github.io/quantum/quark/reference/type-matrix/)
and is **generated from the type mapper**, so it cannot drift from what the
code does.

**If you have tables created before this release**, `PlanMigration` reports the
widening as an `ALTER COLUMN`. Applying it is safe — widening loses no data —
but on a large table the engine may rewrite it, so run it when a rewrite is
acceptable rather than at deploy time. Tables created from here on need
nothing.

<!--
  
  
  ### Added
  
  * add FromCTE, and correct two bench cases S0 measured wrong ([#391](https://github.com/jcsvwinston/quark/issues/391)) ([e87d003](https://github.com/jcsvwinston/quark/commit/e87d003e30b476a60e4a05edf72968b850a1a08c))
  * add Plan.Down, the inverse of a migration plan ([#394](https://github.com/jcsvwinston/quark/issues/394)) ([e2988bf](https://github.com/jcsvwinston/quark/commit/e2988bfe4524adb713ee72dfc30f0e206725cbc0))
  * close the last six query-bench gaps ([#392](https://github.com/jcsvwinston/quark/issues/392)) ([5338644](https://github.com/jcsvwinston/quark/commit/5338644caaffcf97e441a13b5643c3a760d284a6))
  * express COUNT(DISTINCT), CASE, JSON projection and the rest of the window functions ([#389](https://github.com/jcsvwinston/quark/issues/389)) ([cf6afdc](https://github.com/jcsvwinston/quark/commit/cf6afdc626feab03f2792de603a07b8bfaf48478))
  * refuse a db tag written in the Nucleus pkg/model grammar ([#395](https://github.com/jcsvwinston/quark/issues/395)) ([5be7c42](https://github.com/jcsvwinston/quark/commit/5be7c425e6d2c1fffcb57f63060767266e8bcccf))
  
  
  ### Fixed
  
  * map integers and floats by width (QK-21) ([#393](https://github.com/jcsvwinston/quark/issues/393)) ([a7c1a7a](https://github.com/jcsvwinston/quark/commit/a7c1a7af01ba45cf373e77d39230ea3a9efee4f9))
-->
