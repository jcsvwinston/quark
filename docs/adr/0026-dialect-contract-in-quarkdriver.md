---
id: 0026
title: The dialect contract moves to quarkdriver; package quark keeps every name as an alias
status: accepted
date: 2026-10-04
implemented: pending (arc A11, session Q3)
deciders: jcsvwinston
related: [0019, 0023, 0024]
supersedes: null
tags: [architecture, packaging, drivers, dialects, dx]
---

# 0026 — The dialect contract moves to quarkdriver

> **Successor of [ADR-0023](0023-driver-modules.md).** 0023 moved the engines
> out of the library and gave the classifier and the listener a leaf contract,
> `quarkdriver`. It left the dialect — the half of a driver that writes SQL —
> in package `quark`. This record decides how that half follows. Everything
> 0023 decided stays in force.

## Context

The A11 bench (`internal/extbench`, page `docs/extension-bench.md`) builds a
driver module the way a third party would — module path `example.com/extdriver`,
no workspace, `GOWORK=off` — and asks the build graph what it imports. Control
`DRV-02` measured, on 2026-10-04 against v1.15.0:

- The half that registers the **classifier** imports `quarkdriver` and not
  package `quark`: 163 packages.
- The half that registers the **dialect** cannot avoid package `quark`: 208
  packages. Two things force it:
  1. `RegisterDialect` lives in package `quark`.
  2. The `Dialect` interface names a type of package `quark`:
     `LockSuffix(opts LockOptions) (tableHint, suffix string, err error)`.
     A type outside the package cannot implement the interface without
     naming `quark.LockOptions`, and `LockSuffix` is documented to return
     `quark.ErrUnsupportedFeature` when the engine has no row locks.

`Dialect` is not the whole contract. Quark asserts four optional interfaces on
a dialect, and they name more of package `quark`:

| Optional interface | Asserted in | Types of package `quark` it names |
|---|---|---|
| `SavepointDialect` | `tx.go` | none |
| `ColumnTypeMapper` | `schema.go` | none |
| `MigrationLocker` | `migration_lock.go` | `MigrationLock`, `DBConnector`, `DBConn`, `Result`, `Row`; returns `ErrLockTimeout` / `ErrUnsupportedFeature` |
| `SchemaIntrospector` | `schema.go` | `Executor`, and the schema model: `Schema`, `Table`, `Column`, `Index`, `ForeignKey`, `Check` |

`SchemaIntrospector` matters more than its "optional" suggests. Control
`DRV-04` measured that the schema path — `Migrate`, `PlanMigration`, `Sync`,
`ApplyPlan` — decides by `Dialect.Name()` instead of asking the dialect, and
the arc's next sessions (A11 `Q2`) make it ask. A third-party engine that is
to take part in migrations has to answer `IntrospectSchema`, and today that
answer is a `quark.Schema`.

None of these types has a reason to live in package `quark` except history.
`LockOptions` carries one method (`IsZero`); the schema model carries none.

Two constraints frame the answer:

- **QADR-0010** (suite): nothing breaks before the major that closes A12. A
  change that cannot be made by addition stops and says so.
- **ADR-0023** already set the shape of a leaf contract: `quarkdriver` is the
  contract between Quark and a driver module, it does not import package
  `quark`, and when `EventPayload` / `EventListener` moved there in v1.9 they
  stayed in package `quark` as **type aliases**, so nobody changed a line.

## Decision

### 1. The contract moves to `quarkdriver`, the leaf 0023 created

Into `quarkdriver`, unchanged in shape and in documentation:

- `Dialect`, and the types its methods name: `LockOptions` (with `IsZero`),
  `LockMode` and its constants `LockNone`, `LockForUpdate`, `LockForShare`.
- The four optional interfaces Quark asserts on a dialect, and every type
  they name: `SavepointDialect`, `ColumnTypeMapper`, `MigrationLocker`,
  `MigrationLock`, `DBConnector`, `DBConn`, `Result`, `Row`,
  `SchemaIntrospector`, `Executor`, `Schema`, `Table`, `Column`, `Index`,
  `ForeignKey`, `Check`.
- The sentinels a dialect returns: `ErrUnsupportedFeature`, `ErrLockTimeout`.
- The registry: `RegisterDialect(name string, d Dialect)` and
  `LookupDialect(name string) (Dialect, bool)`, with the semantics package
  `quark` has today — a second registration under a name replaces the first,
  and every call is safe for concurrent use (the lock A11 `Q1` added for
  `DRV-01` moves with the map).

One package, not a new one: a driver author already imports `quarkdriver` for
the classifier and the listener, and 0023 named it "the contract". A second
contract package would make one engine's driver import two, for no property
the bench measures.

### 2. Package `quark` keeps every name, as an alias of the same type

```go
type Dialect = quarkdriver.Dialect
type LockOptions = quarkdriver.LockOptions
const LockForUpdate = quarkdriver.LockForUpdate
var ErrUnsupportedFeature = quarkdriver.ErrUnsupportedFeature

func RegisterDialect(name string, d Dialect) { quarkdriver.RegisterDialect(name, d) }
```

An alias is the same type, not a copy. Every dialect implemented anywhere
against `quark.Dialect` satisfies `quarkdriver.Dialect` without a change, and
the reverse; every signature that names `quark.Schema` or `quark.Executor`
compiles as it did; `errors.Is(err, quark.ErrUnsupportedFeature)` holds for an
error a driver built from `quarkdriver.ErrUnsupportedFeature`, because both
names hold the same value.

**Nothing is deprecated.** The names in `quark` are the application's
vocabulary (`quark.WithDialect(quark.PostgreSQL())`, a `quark.Dialect` field);
the names in `quarkdriver` are the driver author's. Both stay, as
`EventListener` did.

`DetectDialect` and `DetectDialectByName` stay in package `quark` — they know
the built-in names and return the built-in dialects — and consult
`quarkdriver.LookupDialect` first, which is what they do with the map today.
`ErrDialectNotSupported`, which only `DetectDialect` returns, stays.

### 3. New contract is born in the leaf

From this record on, a type that is part of the dialect contract is declared
in `quarkdriver` from its first commit, and aliased from `quark` only if
applications need to name it. If it names a type that has not moved yet, that
type moves with it, under the rule of part 2. That applies first to the
optional interfaces A11 `Q2` adds so the schema path asks the dialect for its
column types, its auto-increment key and its `IF NOT EXISTS`: adding them to
package `quark` would grow the contract that `Q3` then has to move.

### 4. The built-in dialects stay in package `quark`

`PostgreSQLDialect` and the other five are implementations, not contract, and
moving them is not needed for a driver to be written without the root. A
driver that reuses a built-in — a CockroachDB driver on PostgreSQL's dialect —
imports package `quark` and may: what `DRV-02` asks is that a driver *can* be
written without it, not that every driver is.

## Why this record is accepted without a new decision by the owner

Each part follows from something already decided or measured:

| Part | Follows from |
|---|---|
| The dialect contract has to leave package `quark` | `DRV-02`, and the arc gate (a kit a driver from outside passes without importing the root) |
| It goes to `quarkdriver`, not a new package | ADR-0023: `quarkdriver` is the driver contract |
| By alias, with no deprecation | QADR-0010 (additive only before the major) and ADR-0023's `EventListener` precedent |
| The schema model goes too | `DRV-04` and the arc's `Q2`: a third-party engine takes part in migrations through `SchemaIntrospector` |
| The registry keeps today's semantics | QADR-0010: a registry that started refusing a second registration would change what a call that works today does |

What the owner may still want to reopen is listed under "When to reopen"; none
of it blocks `Q2` or `Q3`.

## Consequences

- **No caller changes a line.** Aliases preserve identity, so the change is
  additive under QADR-0010 and ships in a minor of Quark.
- **What does change is the type's package path.** `%T`, `reflect.Type.PkgPath()`
  and `reflect.Type.String()` report `quarkdriver.Schema` where they reported
  `quark.Schema`. Code that branches on a type's printed name, or registers it
  with `encoding/gob` by name, sees the new one. 0023 accepted the same effect
  for `EventListener` in v1.9; no consumer in the suite branches on these names.
- **`go doc quark.Dialect` shows the alias**, and the method documentation is
  read in `quarkdriver`. The reference pages under `website/docs/reference/api/`
  point a driver author there (same PR as the move).
- **The API surface file moves members, it does not lose them.** The generator
  behind `acceptance/apisurface.json` records no methods of an alias (its own
  test pins that), so on regeneration the 21 `(Dialect).X` entries — and the 18
  methods of the other moved types — leave package `quark` and appear
  under `quarkdriver`. The diff reads as a removal plus an addition. `Q3`
  says so in its PR, and `Q6` (a surface frozen with signatures, `CON-02`)
  keys each member by the package that declares it, so a later move is
  visible as a move.
- **The bench's fixture has to change with the move.** `testdata/extdriver`
  wraps `quark.SQLite()` — that is what makes `DRV-04` sharp, SQLite's methods
  under another name. A driver that wraps a built-in imports package `quark`
  by part 4, so `DRV-02` cannot turn present on that fixture. `Q3` gives the
  fixture a dialect half written against `quarkdriver` alone, measured by
  `DRV-02`, and keeps the SQLite-methods arm for `DRV-04`.
- **`quarkdriver` gains `context`, `time` and the schema model**; it already
  imports `database/sql`. It stays a leaf: nothing it imports imports package
  `quark`.
- **The adapters stay where they are.** `sqlDBAdapter`, which turns a `*sql.DB`
  into a `DBConnector`, is unexported in package `quark` and keeps working
  against the moved interfaces.

## Alternatives considered

- **Change `LockSuffix` to take plain values** (`mode int, skipLocked, noWait bool`).
  Removes the one root type `Dialect` names with no move at all, and breaks
  every dialect implemented outside the repository: their `LockSuffix` no
  longer satisfies the interface. QADR-0010 rules it out before the major, and
  it leaves `RegisterDialect` and the optional interfaces in the root anyway.
- **A parallel interface in the leaf**, with its own `LockOptions`, and an
  adapter between the two. Two contracts that must stay identical by hand,
  and an implementation of one is not an implementation of the other. The
  alias gives the same reach with one definition.
- **A new leaf package for the dialect** (`quarkdialect`). Equivalent in reach;
  rejected for the reason in part 1 — one engine's driver would import two
  contract packages.
- **Leave the contract in package `quark` and accept `DRV-02` partial.** The
  arc's gate asks for a kit a driver from outside passes without importing the
  root; this is the piece that makes that impossible.

## When to reopen

- If the owner wants the root names deprecated after all — a single place to
  name the contract — the major that closes A12 is where the `quark` aliases
  would go, under QADR-0010 and the deprecation policy.
- If a driver outside the repository needs to reuse a built-in dialect without
  importing package `quark`, part 4 is the one to revisit: the built-ins would
  have to move as well, with what they import.
- If moving the schema model turns out to drag a dependency into `quarkdriver`
  that a leaf should not carry, split the introspection contract into its own
  leaf under `quarkdriver/` rather than leaving it in the root.
