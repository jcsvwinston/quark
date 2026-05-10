# Migration guide — v0.3.0 → v0.4.0

v0.4.0 is the Phase 2 cut. Most additions are purely additive (the
`Where`/`Having` AST, `AsSubquery`, `With`/`WithRecursive`, `SelectExpr`
+ window helpers, `Union`/`Intersect`/`Except`); they don't break
existing callers.

There is **one breaking change**: the `Join` / `LeftJoin` / `RightJoin`
methods on `*Query[T]` now return a typed `*JoinBuilder[T]` instead of
taking the ON clause as a second argument. The replacement was
flagged as deprecated in v0.2 (see the "v0.4 deprecation notice" in the
v0.3.x godoc).

## Why

The string-raw form encouraged callers to assemble ON clauses manually
and route them through `guard.ValidateJoinOn` at exec time. That works
but the validation error landed late and the join API didn't compose
with the rest of the typed builder. The structured builder makes the
two halves of a JOIN explicit (table, then ON), and the typed
`.On(left, op, right)` keeps the validation errors at the call site
where they're easy to spot.

## How to migrate

Two replacement methods cover every existing call site:

### Single binary identifier comparison — use `.On(left, op, right)`

This is the typed shape and covers the overwhelming majority of JOINs.

| v0.3.x | v0.4.0 |
| --- | --- |
| `q.Join("users", "users.id = orders.user_id")` | `q.Join("users").On("users.id", "=", "orders.user_id")` |
| `q.LeftJoin("orders", "orders.user_id = users.id")` | `q.LeftJoin("orders").On("orders.user_id", "=", "users.id")` |
| `q.RightJoin("audit", "audit.entity_id = users.id")` | `q.RightJoin("audit").On("audit.entity_id", "=", "users.id")` |

### Multi-condition ON clause — use `.OnRaw(onClause)`

For ON clauses with AND-chained binary identifier comparisons (the
shape `guard.ValidateJoinOn` accepts), `.OnRaw` is the mechanical
rewrite of the second argument.

| v0.3.x | v0.4.0 |
| --- | --- |
| `q.Join("users", "users.id = orders.user_id AND users.tenant_id = orders.tenant_id")` | `q.Join("users").OnRaw("users.id = orders.user_id AND users.tenant_id = orders.tenant_id")` |

`.OnRaw` runs through the same validator as the legacy form, so the
behaviour is identical for accepted shapes; injection attempts continue
to surface as `ErrInvalidJoin` at exec time.

## What to expect at compile time

Existing code that calls the two-argument form will fail to compile
with messages of the form:

```
too many arguments in call to q.Join
    have (string, string)
    want (string)
```

A find-and-replace pass over your project usually completes the
migration in a single commit. If you have a large code base, a
`gofmt -r` rewrite rule covers the typed-form replacement:

```bash
gofmt -r 'a.Join(b, c) -> a.Join(b).OnRaw(c)' -w ./...
gofmt -r 'a.LeftJoin(b, c) -> a.LeftJoin(b).OnRaw(c)' -w ./...
gofmt -r 'a.RightJoin(b, c) -> a.RightJoin(b).OnRaw(c)' -w ./...
```

This produces correct (if slightly verbose) code in one pass; if you
want the `.On(left, op, right)` form, you can apply that transform
manually for the JOINs you touch most.
