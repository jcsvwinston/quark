# Quark v0.4.0 — Release Notes

> **Date:** 2026-05-10
> **Status:** late-alpha. Not yet v1.0 production-ready.
> See [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) for the honest gap analysis between the current state and a planned v1.0.

Phase 2 release: the composable query builder. v0.3 closed the P0 backlog and shipped the rich-type / dirty-tracking layer; v0.4 lands the typed expression AST and the structured query primitives that build on it (subqueries, CTEs, window functions, set operators, pessimistic locking). One BREAKING change: `Join` / `LeftJoin` / `RightJoin` return a `*JoinBuilder[T]` instead of taking an `(table, onClause)` pair — full migration in [`MIGRATION_v0.4.0.md`](MIGRATION_v0.4.0.md).

## What's in this release

### Composable query builder (Phase 2)

- **F2-AST — Composable expression AST**: `Expr` interface with `Col`, `Lit`, `And`, `Or`, `Not`, `Cmp` (+ `Eq`/`Ne`/`Lt`/`Gt`/`Lte`/`Gte`), `In`, `NotIn`, `Func` (10-name whitelist: COUNT/SUM/AVG/MIN/MAX/LOWER/UPPER/LENGTH/COALESCE/ABS). New `Query[T].WhereExpr(e)` and `HavingExpr(e)` integrate the AST into the existing where-clause pipeline. Identifiers go through `SQLGuard.ValidateIdentifier` at every leaf; operators through `SQLGuard.ValidateOperator`. The AST emits `?` as a neutral bind marker that `substitutePathMarkers` rewrites to the dialect placeholder at render time.

- **F2-subqueries — Typed subqueries**: `Query[T].AsSubquery() (*Subquery, error)` captures the rendered SELECT (with `?` markers) plus the inner args. AST wrappers `Sub`, `Exists`, `NotExists`, `InSub`, `NotInSub` embed the subquery as an expression leaf. Pessimistic locks on the inner query are rejected with `ErrUnsupportedFeature` (MSSQL inlines `WITH (UPDLOCK)` in the FROM clause, illegal inside an `IN (SELECT ...)` context).

- **F2-CTE — Common Table Expressions**: `Query[T].With(name, sub)` and `Query[T].WithRecursive(name, sub)` prepend a `WITH "name" AS (<inner>)` (or `WITH RECURSIVE ...`) prefix to the outer SELECT. Inner args are substituted from `?` markers and prepended to the args slice; outer WHERE/HAVING `argIndex` shifts accordingly so dialect placeholders line up. The base `buildSelect`, `Count`, and `aggregate` paths share a `buildCTEPrefix` helper so CTE-aware queries route through every aggregation path correctly.

- **F2-window — Window functions**: `Query[T].SelectExpr(alias, e)` projects an AST expression into the SELECT list. New `Window` immutable spec (`NewWindow().PartitionBy(...).OrderBy(col, desc)`) plus `Over(inner, w)` combinator. Dedicated leaves `RowNumber`, `Rank`, `DenseRank`, `Lag(col, offset)`, `Lead(col, offset)` cover the most-used window functions; Lag/Lead offset is bound as a parameter, not interpolated.

- **F2-set — Set operators**: `Union`, `UnionAll`, `Intersect`, `Except` between `Query[T]` operands of matching `T`. Renders flat (no parens around operands — SQLite rejects parenthesised operands). Dialect-keyword translation in a package-level `setOpKeyword` helper: Oracle `EXCEPT` → `MINUS`; MySQL/MariaDB return `ErrUnsupportedFeature` for `INTERSECT`/`EXCEPT`; SQLite rejects `INTERSECT ALL`/`EXCEPT ALL`. Operand restrictions: no `ORDER BY`/`LIMIT`/`OFFSET`/lock/own-CTEs/nested-set-ops on the operand; no lock on the base. Outer `OrderBy`/`Limit` apply to the combined result.

- **F2-locking — Pessimistic locking**: `Query[T].ForUpdate()`, `ForShare()`, `SkipLocked()`, `NoWait()`. New `Dialect.LockSuffix(LockOptions) (tableHint, suffix string, err error)` interface method consumed by `buildSelect`. PG / MySQL / MariaDB / Oracle emit `FOR UPDATE [SKIP LOCKED|NOWAIT]` / `FOR SHARE`; MSSQL emits `WITH (UPDLOCK, ROWLOCK [, READPAST])` table hints in FROM; SQLite returns `ErrUnsupportedFeature` for any non-zero lock option (use `BEGIN IMMEDIATE` instead). New error sentinel `ErrUnsupportedFeature`.

- **F2-nested-preload — Dotted-path Preload**: `Preload("Orders.Items.Product")` walks the chain in one pass. `parsePreloads` builds a `preloadNode` tree and merges shared prefixes so `Preload("Posts", "Posts.Comments")` only loads `Posts` once. Per-relation loaders moved from `*Query[T]` to `*BaseQuery` accepting `reflect.Value`, so the recursive descent doesn't need a generic instantiation per level.

- **F2-IN-chunking — Eager-loading paths chunk parent keys**: a `Preload` over a large parent set used to assemble a single `IN(...)` with one bind per parent — silently broken on Oracle (1000-IN cap) and at risk on SQL Server (~2100 bind ceiling). Fixed: the three relation loaders now chunk at 1000 keys per query and aggregate results across chunks via a new internal `chunkParentKeys`. Tenant predicates and polymorphic-type discriminators are re-applied per chunk.

- **F2-having-agg — Aggregate HAVING shortcut**: `Query[T].HavingAggregate(fn, column, op, value)` writes `HAVING COUNT(*) > 5` / `HAVING SUM(amount) >= 100` / etc. without falling back to `RawQuery`. `fn` whitelisted to COUNT/SUM/AVG/MIN/MAX (case-insensitive); `column` validated through SQLGuard, except `*` which is only allowed with COUNT. The fully composable form `HavingExpr(Gt(Func("COUNT", Col("*")), Lit(5)))` is the structured counterpart for predicates the shortcut can't model.

### Changed (BREAKING)

- **F2-join-builder — Structured `Join(table).On(...)` builder**: `Join`, `LeftJoin`, and `RightJoin` now return a `*JoinBuilder[T]` instead of taking `(table, onClause)`. Complete the JOIN with `.On(left, op, right)` (the typed binary identifier form) or `.OnRaw(onClause)` (the v0.3.x string-raw shape, still validated through `guard.ValidateJoinOn`). Both forms route through the same identifier-only grammar — only the call shape changed, not the validation surface. See [`MIGRATION_v0.4.0.md`](MIGRATION_v0.4.0.md) for the mechanical rewrite (a `gofmt -r` rule covers it). Closes the v0.2 deprecation notice.

## Known limitations

- Quark is **late-alpha** (~v0.4). Not v1.0 production-ready. The remaining roadmap is in [`docs/ROADMAP.md`](ROADMAP.md): schema-diff migrations (Phase 3), observability/metrics depth (Phase 4), real RLS engine via `CREATE POLICY` + `SET LOCAL` (Phase 5), codegen-generated row binders (Phase 6).
- End-to-end coverage is currently exercised against SQLite via the SharedSuite. The remaining five engines (PostgreSQL, MySQL, MariaDB, MSSQL, Oracle) need testcontainers wired up — F0-8 in [`TASKS.md`](../TASKS.md). The AST and the surface that composes on it are dialect-agnostic by construction (rendering uses `?` markers + `substitutePathMarkers` for dialect placeholder substitution), but the cross-engine end-to-end signal isn't there yet.
- Set operators on MySQL/MariaDB are limited to `UNION` / `UNION ALL`; `INTERSECT` / `EXCEPT` return `ErrUnsupportedFeature` (rewrite as a JOIN). Oracle uses `MINUS` for `EXCEPT`. SQLite rejects `INTERSECT ALL` / `EXCEPT ALL`.
- Subquery and set-op operands cannot carry pessimistic locks. The dialect-specific lock emission would land in a position most engines' locking semantics don't model cleanly. Apply locks on the outer query.

## Upgrading

```bash
go get github.com/jcsvwinston/quark@v0.4.0
```

If you call `Query[T].Join` / `LeftJoin` / `RightJoin`, follow [`MIGRATION_v0.4.0.md`](MIGRATION_v0.4.0.md) — a 5-second rewrite per call site (`Join(t, on)` → `Join(t).OnRaw(on)` for the literal port, or the typed `Join(t).On(left, op, right)` when the ON clause is a single binary comparison).

## Versioned docs

The page-versioned site for v0.4.0 is at `https://jcsvwinston.github.io/quark-docs/docs/0.4.0/`.
