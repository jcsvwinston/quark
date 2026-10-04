# Release notes — v1.15.2

A patch that fixes the rows that conditional writes touch — a defect present
since the first release — and raises the sibling module floors.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>

## Fixed

- **Writes with conditions now match the rows a read with the same conditions
  returns** ([#428](https://github.com/jcsvwinston/quark/pull/428)). Since the
  first release, `DeleteBy`, `UpdateMap` and `UpdateFields` rendered their own
  `WHERE` as `col OP ?` joined by `AND`, ignoring each condition's logic:
  - `WhereNot` lost its `NOT`. `WhereNot("status", "=", "active").DeleteBy()`
    ran `DELETE … WHERE "status" = ?` and **deleted the rows it was meant to
    keep**; `UpdateMap` and `UpdateFields` wrote them the same way.
  - `Or` groups were joined with `AND`, and `IN`, `NOT IN`, `BETWEEN` and
    `NOT BETWEEN` failed with a driver error.

  The write paths now go through the same renderer as `List`, wrapped in
  parentheses after the key and tenant predicates, with placeholders numbered
  after the `SET` arguments. A test checks, on SQLite and on the six-engine
  suite, that each write touches exactly the rows `List` returns for the same
  conditions. `PreloadWhere` filters use the same renderer. **If you call
  `DeleteBy`, `UpdateMap` or `UpdateFields` after `WhereNot`, check the data
  those calls touched**; plain `Where` with comparison operators was not
  affected.
- **Module floors.** `cmd/quark` (v1.1.2) and the five `drivers/*` modules
  (v0.2.4) require `github.com/jcsvwinston/quark v1.15.1`
  ([#429](https://github.com/jcsvwinston/quark/pull/429)).

## Documentation

- The CRUD reference said `Update` merges the primary key with the query's
  `Where` clauses and showed `Where("tenant_id", …)` as a guard. It never did:
  `Update(entity)`, `UpdateBatch`, `Delete(entity)` and `HardDelete(entity)`
  write by key and ignore the query's conditions. The reference now says so;
  until that changes, use `UpdateFields` or `DeleteBy` when a condition must
  guard a write.
