# Migration to v0.2.0

> Target tag: **v0.2.0** (Fase 0 — see `docs/ANALISIS_MADUREZ.md` §4).
> This document tracks breaking changes accumulated on `main` after `v0.1.1`
> and is updated in the same PR as each change. The actual tag is cut once
> the Fase 0 backlog (`TASKS.md`) is empty.

## Breaking changes

### `Dialect.JSONExtract` signature changed (P0-2)

The dialect interface method used to return a single `string`. It now returns
a SQL fragment, the bind args required by it, and an error:

```go
// Old
JSONExtract(column, path string) string

// New
JSONExtract(column, path string) (sql string, args []any, err error)
```

The change is required to bind the JSON path as a parameter rather than
interpolate it (P0-2 was a SQL-injection vector). The path is also validated
against `internal/guard.ValidateJSONPath` before any dialect builds the
fragment.

The returned SQL fragment uses literal `?` as a neutral bind marker;
`buildWhereClause` substitutes each `?` for the dialect's placeholder syntax
(`$N`, `?`, `@pN`, `:N`) at the appropriate arg index.

#### Example outputs (column `"data"`, path `"user.name"`)

| Dialect | sql | args |
| --- | --- | --- |
| PostgreSQL | `jsonb_extract_path_text(("data")::jsonb, ?, ?)` | `["user", "name"]` |
| MySQL | ``JSON_EXTRACT(`data`, ?)`` | `["$.user.name"]` |
| MariaDB | ``JSON_VALUE(`data`, ?)`` | `["$.user.name"]` |
| SQLite | `JSON_EXTRACT("data", ?)` | `["$.user.name"]` |
| SQL Server | `JSON_VALUE([data], ?)` | `["$.user.name"]` |
| Oracle | `JSON_VALUE("DATA", ?)` | `["$.user.name"]` |

#### Who is affected

Any caller of `quark.RegisterDialect("name", customDialect)` with a custom
`Dialect` implementation. Pre-1.0; we have no record of external custom
dialects.

#### Migration steps

1. Update the custom dialect's `JSONExtract` to return three values.
2. Call `guard.ValidateJSONPath(path)` first; on error, return
   `("", nil, err)`.
3. Build the SQL fragment using `?` as the bind marker (one per path arg).
4. Return `(sql, args, nil)`.

Reference implementation: see `dialect.go` for the in-tree dialects.

### New error sentinel `ErrInvalidJSONPath`

Returned by `WhereJSON` at execution time (`List`, `First`, `Iter`, etc.) when
the path does not match
`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$` (max 256 chars).

```go
_, err := quark.For[Doc](ctx, client).
    WhereJSON("data", "x'; DROP TABLE--", "=", "y").
    List()
if errors.Is(err, quark.ErrInvalidJSONPath) {
    // expected — path was rejected before any SQL ran
}
```

Leading `$` (the JSONPath sigil that some engines use internally, e.g.
`$.user.name`) is rejected at the API surface. The dialect adds it where
needed; the public API stays uniform across motors.

### `Join` / `LeftJoin` / `RightJoin` `on` clause now validated (P0-5)

The string-raw `on` argument is **deprecated**. Previously it was emitted
verbatim into the SQL; now it is validated against the identifier-only
grammar enforced by `internal/guard.ValidateJoinOn`:

| Accepted | Rejected (returns `ErrInvalidJoin`) |
| --- | --- |
| `Join("u", "u.id = o.user_id")` | `Join("u", "u.id = o.user_id; DROP TABLE o")` |
| `Join("u", "u.id = o.user_id AND u.tenant = o.tenant")` | `Join("u", "u.id = 1")` |
| `Join("u", "a.x = b.y OR c.z = d.w")` | `Join("u", "(u.id = o.user_id)")` |
| | `Join("u", "LOWER(u.id) = o.user_id")` |
| | `Join("u", "u.id = o.user_id -- comment")` |

#### Migration steps

If you have a `Join` call site whose `on` clause does not fit the grammar
above, you have two options for v0.2:

1. Move the join into a `RawQuery` call (gated by `AllowRawQueries`).
2. Restructure the join as a single SELECT with a `Where` predicate that
   uses simple identifier names plus a database view that pre-joins the
   tables.

In v0.4 a structured `Join(table).On(col, op, otherCol)` builder will replace
the string-raw form. The deprecation period gives callers the v0.2 → v0.3
release window to migrate.

## Non-breaking changes

For non-breaking entries see `CHANGELOG.md` under `[Unreleased]`.
