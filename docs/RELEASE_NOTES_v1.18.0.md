# Release notes — v1.18.0

A minor that publishes the eight behaviour changes Quark makes at its 2.0,
with the way to stay on today's behaviour where one is needed, and fixes a
batch write that decided the key column from its first entity alone. It also
adds one tag token, `quark:"literal_column"`, for a column whose name is a
`pkg/model` directive. Nothing is removed and no default changes in this
release: the notices announce, and the changes ship with the major. If your
code calls `CreateBatch` or `UpsertBatch` with a mix of entities that carry a
key and entities that do not, upgrade.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>

## Fixed

**`CreateBatch` and `UpsertBatch` decide the key column per run of entities
(QK-70; [#479](https://github.com/jcsvwinston/quark/pull/479)).** Both looked
at the first entity of the batch to decide whether the primary-key column went
into the `INSERT`, and applied that decision to every row. A batch whose first
entity had no key and a later one had a key silently dropped the later key: the
row was inserted with a database-assigned key while the entity kept the one it
carried. A batch whose first entity had a key and a later one had none inserted
`id=0` for the later one, or failed the whole batch on an engine that rejects
an explicit zero. The key column is now decided per run of consecutive entities
that agree on carrying their key, in the batch's order: each run gets its own
statements through the same per-engine forms as before, the key left out when
the run carries none and written when it does. A batch whose entities all agree
is one run and sends exactly the statements it always did; a batch that
alternates goes row by row, which is slower and correct. Validation and the
`BeforeCreate` hooks still run once for the whole batch. Whether an engine
accepts a carried key is still the engine's business: SQL Server's `IDENTITY`
and Oracle's `GENERATED ALWAYS` now refuse the run that carries one instead of
the key being dropped. The
[batch operations guide](https://jcsvwinston.github.io/quantum/quark/guides/batch-operations/) and the
[CRUD reference](https://jcsvwinston.github.io/quantum/quark/reference/api/crud/) no longer state the first-entity rule.

## Added

**`quark:"literal_column"` keeps a column whose name is a `pkg/model`
directive (QK-76; [#476](https://github.com/jcsvwinston/quark/pull/476)).**
A `db` tag whose column name is exactly a Nucleus `pkg/model` directive
(`db:"pk"`, `db:"readonly"`, `db:"tenant"`, `db:"fk"` and the rest) has logged
a `WARN` and migrated a column of that name since v1.14.0. Quark's own grammar
had no unambiguous spelling for "the column really has that name": `db:"column:pk"`
is the `pkg/model` grammar and is `ErrInvalidTag` today. The new token says it:

```go
PK string `db:"pk" quark:"literal_column"` // a column really named pk
```

It silences the warning now, keeps the model valid in 2.0, combines with the
other tokens and does nothing on a field whose column name is not a directive.
An older Quark rejects it as an unknown token. The warning now names the token
and `DEP-2026-011` instead of saying to ignore it, and the hint for `index`
points at `quark:"index"`. Quark warned about itself: the audit model behind
`EnableAuditLog` has a column really called `pk` and logged this `WARN` on
every call since v1.14.0; it now carries the token. The
[modeling guide](https://jcsvwinston.github.io/quantum/quark/guides/modeling/) has the token in its tag table.

## Announced for 2.0

Eight behaviour changes, `DEP-2026-004` to `DEP-2026-011`, each with the test
that pins today's behaviour, the way to stay in 1.x where one exists, and a
window that opens **no earlier than 2027-02-07**. `DEP-2026-004` to
`DEP-2026-010` were first dated 2027-01-17 for the set of 2026-10-19; Quark
cut no release in that set, so they are published with this one and re-dated.
In 1.x nothing below changes.

- **`DEP-2026-004`.** `UpdateMap`, the update branch of `Upsert` and
  `UpsertBatch`, `Delete` and `HardDelete` of an entity, the soft delete and
  `Restore` on a model with a `quark:"version"` field join optimistic locking,
  as `Update`, `UpdateFields` and `Tracked.Save` already do.
- **`DEP-2026-005`.** A write by entity that touches no row runs no `After*`
  hook, writes no audit row and publishes no event, with or without
  conditions on the query.
- **`DEP-2026-006`.** `Upsert` and `UpsertBatch` do the same on the six
  engines: an empty `updateCols` ignores the conflict, and a duplicate on
  another unique key is `ErrConstraintViolation`.
- **`DEP-2026-007`.** `SET DEFAULT` in a foreign key is refused on MySQL and
  MariaDB before anything is sent, as on Oracle; today both accept it without
  doing it.
- **`DEP-2026-008`.** `GroupBy` without `Select` or `SelectExpr` is
  `ErrInvalidQuery` instead of a `WARN`.
- **`DEP-2026-009`.** The plain `LIKE` forms (`Where(col, "LIKE", p)`, the
  typed `Like`, `Cmp`) declare `ESCAPE '\'` on every engine.
- **`DEP-2026-010`.** On Oracle a sized string column is rendered
  `VARCHAR2(n CHAR)`, so the length counts characters as on the other five
  engines; this is the DDL the plan renders, not the data.
- **`DEP-2026-011`.** A `db` tag whose column name is a bare `pkg/model`
  directive is `ErrInvalidTag` instead of a `WARN`; `quark:"literal_column"`,
  above, keeps a column really named so.

All eleven open notices, with their dates, are in the
[register of deprecations and behaviour changes](https://github.com/jcsvwinston/quark/blob/main/docs/deprecations/README.md).

## Build and modules

**Built and tested with Go 1.26.10
([#477](https://github.com/jcsvwinston/quark/pull/477)).** The toolchain that
builds and tests Quark, and builds the CLI archives attached to this release,
moves from Go 1.26.9 to 1.26.10, which fixes GO-2026-6809 in `crypto/x509`.
The library still requires Go 1.25.7 or later and builds with the Go your
application uses; if your application verifies certificates, build it with a
Go release that has the fix.

**Module floors.** `cmd/quark` (v1.2.2) and the five `drivers/*`
modules (v0.3.1) require `github.com/jcsvwinston/quark v1.17.0`
([#480](https://github.com/jcsvwinston/quark/pull/480));
no driver or CLI code changed.
