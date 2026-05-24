# Quark v0.12.0 — Release Notes

> **Date:** 2026-05-24
> **Status:** late-alpha. Not yet v1.0 production-ready.
> See [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) for the honest gap analysis between the current state and a planned v1.0.

A small Phase 6 release: opt-in **compile-time column type-safety** on top
of the code generator, plus an allocation fix on the write path.
**No breaking changes.** Both additions are inert unless you opt in —
the reflection path and the string query API behave exactly as before.

## Added

### Typed compile-time column accessors (F6-4)

`quark gen` now also emits, per model, a `<Model>Columns` value of typed
column handles, and the query builder gains `Query.WhereP`. Together they
let you build `WHERE` conditions without magic column strings and with
**compile-time** checking of both the column name and the bound value:

```go
adults, err := quark.For[Account](ctx, c).
    WhereP(AccountColumns.Email.Like("%@example.com"), AccountColumns.Age.Gte(18)).
    List()

AccountColumns.Emial.Eq("x")  // compile error: no field Emial
AccountColumns.Age.Eq("x")    // compile error: Age wants an int
```

- Each handle (`quark.TypedColumn[T]`) offers `Eq`/`Neq`/`Gt`/`Gte`/`Lt`/
  `Lte`/`In`/`NotIn`/`Between`/`IsNull`/`IsNotNull`, all typed to the
  field. String columns use `quark.TypedStringColumn`, which adds
  `Like`/`NotLike`.
- `WhereP` is **pure compile-time sugar** (ADR-0014): each predicate lowers
  to the identical internal condition `Where("col", op, val)` produces, so
  the typed and string APIs are interchangeable and mix on one query. The
  string form stays fully valid.
- The accessors register nothing with the runtime — they change neither
  query execution nor performance. Their only benefit is compile-time
  safety. Without codegen they do not exist.

This is the honest value of code generation per the v0.11.0 profiling
finding: **type-safety, not speed**. See the
[Code Generation guide](https://jcsvwinston.github.io/quark-docs).

## Performance

### Audit row diff is built only when an audit sink is configured

Every `Create`/`Update`/`UpdateFields`/`Delete` previously built the full
audit-row map (`rowToMap`) on every write, even with no audit log
configured — measured at ~9% of `InsertOne` allocations
(`benchmarks/PROFILING.md`). The diff is now computed inside `recordAudit`,
behind the existing "is auditing active?" gate, so it allocates nothing on
the common path. **No behavior change when auditing is enabled.**

## Notes

- The typed accessors do not offer `OR` / grouping — use the string
  `Where` / `Or` API for those. Calling `In()` / `NotIn()` with no values
  lowers to the same empty-`IN` condition as the string `WhereIn` (SQLite
  treats it as matching nothing; PostgreSQL/Oracle/SQL Server reject it),
  so pass at least one value.
- Generated files carry a versioned contract; the accessors do not change
  it (`//quark:gen v3` is unchanged). After changing a model, re-run
  `quark gen`.

[#104]: https://github.com/jcsvwinston/quark/pull/104
[#105]: https://github.com/jcsvwinston/quark/pull/105
