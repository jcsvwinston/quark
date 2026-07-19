# Release notes — v1.3.0

**Set-operator round-out.** v1.3.0 exposes the multiset set-operators
`INTERSECT ALL` / `EXCEPT ALL` and corrects the per-dialect support story for
the whole set-operator family. No breaking changes — everything is additive.

Docs (1.3.0 is the current version): <https://jcsvwinston.github.io/quark/docs/>
(older versions under `/docs/1.2.2/` etc.)

## Added

- **`IntersectAll` / `ExceptAll` — multiset set-operators.** The existing
  `Intersect` and `Except` deduplicate their result; the new `ALL` variants keep
  duplicate rows (multiset semantics). These render on **PostgreSQL and
  MariaDB (10.5+)** only — the engines whose dialect actually implements
  `INTERSECT ALL` / `EXCEPT ALL`.

  Until now the renderer knew how to emit the `ALL` keyword but no public
  `Query` method reached it, so the whole `ALL` branch — including its dialect
  rejections — was unreachable code that no caller or test could exercise.

## Fixed

- **Set operators reject unsupported engines cleanly instead of emitting
  invalid SQL.** SQL Server and SQLite have `INTERSECT` / `EXCEPT` but no `ALL`
  variant of either, and Oracle only gained `INTERSECT ALL` / `MINUS ALL` in
  21c — a version Quark does not assume without a runtime probe. Requesting
  `IntersectAll` / `ExceptAll` there now returns `ErrUnsupportedFeature` at
  render time — the same contract every other unsupported dialect already
  followed. Previously the SQL Server
  path emitted `INTERSECT ALL` and the server answered with a misleading parse
  error (`Invalid usage of the option NEXT in the FETCH statement`) rather than
  a clear "unsupported".

- **`Intersect` / `Except` documentation corrected for MariaDB.** The godoc
  claimed MariaDB did not support `INTERSECT` / `EXCEPT`; it has since 10.3, and
  the live acceptance matrix runs them there. The only engine outside the
  supported set is MySQL, which gained them in 8.0.31 — a minor version Quark
  will not assume without probing the server.

## Support matrix

| Operator | Supported on |
| --- | --- |
| `Union`, `UnionAll` | all six engines |
| `Intersect`, `Except` | PostgreSQL, MariaDB, SQL Server, Oracle, SQLite (not MySQL) |
| `IntersectAll`, `ExceptAll` | PostgreSQL, MariaDB |

Everything else returns `ErrUnsupportedFeature` at render time, so an
unsupported combination fails fast and explicitly rather than reaching the
database.

## Documentation

The published docs no longer reference internal engineering artefacts (design
records, internal priority labels, internal-only files). A CI check
(`check_docs_product_voice.sh`) now keeps that vocabulary out of the site.

## Verification

The set-operator behaviour above is verified by execution against all six
engines (SQLite, PostgreSQL 16, MySQL 8, MariaDB, SQL Server 2022, Oracle 23)
in the live suite and the superapp acceptance harness — the supported engines
run the queries, the unsupported ones assert `ErrUnsupportedFeature`.

## Upgrade

Drop-in from any `v1.x`. No API changes to existing methods, no behaviour change
to `Intersect` / `Except` / `Union` / `UnionAll`.
