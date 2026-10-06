---
id: 0029
title: Within v1 an extension point gains no method; a plumbing interface may
status: accepted
date: 2026-10-06
implemented: v1.17 (arc A11 follow-up — website/docs/reference/extension-contract.mdx and website/docs/operations/upgrade.mdx state the rule; internal/extbench/frozen_points_test.go and testdata/extension-points.txt enforce it)
deciders: jcsvwinston
related: [0008, 0020, 0023, 0026]
supersedes: null
tags: [compatibility, versioning, api, extensibility, drivers, dialects]
---

# 0029 — Within v1 an extension point gains no method; a plumbing interface may

> **Decided by the owner on 2026-10-06.** The extension contract page
> (`website/docs/reference/extension-contract.mdx`, arc A11, session Q6)
> published this policy as a proposal and said it needed an architecture
> decision record before it bound anyone. This is that record. It narrows the
> versioning promise of `website/docs/operations/upgrade.mdx` in one place,
> for one kind of code, and the two pages now say so.

## Context

The versioning promise reads: "Patch and minor releases (`v1.x.y`) keep API
compatibility. Code that compiles against `v1.3` compiles against every later
`v1.x`." For an interface, that sentence covers two kinds of code — code that
calls it and code that implements it — and the two pull in opposite
directions. Adding a method to an interface breaks nothing that calls it and
every type outside the package that implements it. Read literally, the promise
froze the method set of every exported interface of Quark for all of `v1`.

Not every exported interface is there to be implemented. The A11 extension
bench computes, from the type-checked API, every exported type of `quark` and
`quarkdriver` that code outside them **can** implement — an interface with no
unexported method, a function type, or a record of functions. On 2026-10-06
that census holds 53 types: 40 interfaces, 12 function types and one record
(`quarkdriver.Classifier`). Some exist so that a third party implements them
and Quark calls the implementation: a dialect, a model hook, a cache store, a
middleware. Others go the other way: Quark, `database/sql` or Quark's own
constructors implement them, and the caller receives the value — the
`Executor` a dialect reads the catalog through, the `Event` an `EventBus` is
handed, the `Expr` that `Eq` and `And` build. A third party can implement those
too, since nothing seals them, but nothing in Quark asks it to; in practice
the implementations outside Quark are test doubles and wrappers.

Under the literal promise both kinds are frozen alike. Quark could not add a
method to `Event` — a value Quark itself builds — before `v2`, although the
only code that method could break is code that builds its own `Event`.

Quark has grown its extension points for a long time without adding methods
to them, by the pattern the standard library uses for the same reason
(`io.WriterTo`, `http.Flusher`): a new capability is a separate, optional
interface that Quark checks for with a type assertion, with a default for a
value that does not implement it. `SavepointDialect`, `ColumnTypeMapper`,
`SchemaIntrospector` and `MigrationLocker` extend `Dialect` that way;
`CacheLocker` extends `CacheStore` ([ADR-0020](0020-cross-instance-cache-stampede-coordination.md));
the six schema-path questions of A11 Q2 (`ColumnTyper`, `AutoIncrementer`,
`IdempotentDDL`, `ColumnAlterer`, `ObjectDropper`, `TableRebuilder`) and
`ReferentialActioner` (quark#457) extend `Dialect` the same way. The pattern
was a habit, not a rule.

A11 Q6 published the contract page: one table with a row per implementable
type, each marked as an extension point or not (*yes* / *no*) and with a
stability (*stable*, *experimental*, *internal-use*). It described the promise
those two columns imply as a proposal, and said that until an ADR adopted it,
the literal promise stood for every exported type. The owner adopted it on
2026-10-06.

## Decision

### 1. Two categories, decided per type, on the contract page

- **Extension point** — an exported type of `quark` or `quarkdriver` that a
  third party implements and hands to Quark, and that Quark then calls: an
  interface whose methods Quark calls, a function type whose values Quark
  calls, or the record of functions `quarkdriver.Classifier`, whose fields
  Quark calls. The page writes *yes* in its *Extension point* column.
- **Plumbing** — an exported type that code outside the package could
  implement, but whose purpose is the other direction: Quark, the standard
  library or Quark's own constructors implement it, and the caller receives
  it, calls its methods, or passes along the values Quark gives it. The page
  writes *no*.

The category belongs to the type, not to a name: `quark.Dialect` is an alias
of `quarkdriver.Dialect`, the same type, with one row and one category. The
page is the living list — the extension bench's control `CON-01` fails when an
implementable type has no row, has two, or carries a value outside the page's
vocabulary — and a type added later is classified in the pull request that
adds it. On the date of this decision the page classifies the 53 types of the
census as follows.

**Extension points — 41.**

| Kind | Types | Stability |
| --- | --- | --- |
| Interfaces of `quark` (17) | `TableNamer`, `Validator`, `BeforeCreateHook`, `AfterCreateHook`, `BeforeUpdateHook`, `AfterUpdateHook`, `BeforeDeleteHook`, `AfterDeleteHook`, `BeforeFindHook`, `AfterFindHook`, `ShardKeyer`, `ClientProvider`, `Middleware`, `QueryObserver`, `CacheStore`, `CacheLocker`, `EventBus` | stable |
| Interfaces of `quarkdriver` (8 stable) | `SQLStater`, `Listener` (alias `quark.EventListener`), `Dialect` (`quark.Dialect`), `SavepointDialect` (`quark.SavepointDialect`), `ColumnTypeMapper` (`quark.ColumnTypeMapper`), `SchemaIntrospector` (`quark.SchemaIntrospector`), `MigrationLocker` (`quark.MigrationLocker`), `MigrationLock` (`quark.MigrationLock`) | stable |
| Interfaces of `quarkdriver` (7 experimental) | `ColumnTyper`, `AutoIncrementer`, `IdempotentDDL`, `ColumnAlterer`, `ObjectDropper`, `ReferentialActioner`, `TableRebuilder` | experimental |
| Function types (8) | `quark.Scope`, `quark.ExecFunc`, `quark.QueryFunc`, `quark.QueryRowFunc`, `quark.TypeMapper`, `quark.ShardFunc`, `quark.ShardResolver`, `quarkdriver.ListenerFactory` | stable |
| Record of functions (1) | `quarkdriver.Classifier` | stable |

**Plumbing — 12.**

| Kind | Types | Stability |
| --- | --- | --- |
| Interfaces of `quark` (2) | `Expr`, `Event` | stable |
| Interfaces of `quarkdriver` (6) | `IdentifierValidator`, `Executor` (`quark.Executor`), `DBConnector` (`quark.DBConnector`), `DBConn` (`quark.DBConn`), `Result` (`quark.Result`), `Row` (`quark.Row`) | stable |
| Function types (4) | `quark.Option` | stable |
| | `quark.TypedScanner`, `quark.TypedBinder` (written by `quark gen`), `quarkdriver.NewListenerFunc` (the listener contract before `ListenerFactory`, deprecated) | internal-use |

In all: 43 stable, 7 experimental, 3 internal-use.

### 2. An extension point does not change shape within v1

In every `v1.x.y` release, minor or patch:

- An extension-point interface gains no method. None of its methods is
  removed or changes its signature.
- An extension-point function type keeps its signature.
- `quarkdriver.Classifier` gains no field, loses none, and changes none. Its
  three predicates are all required — `Register` refuses a nil one, because a
  missing predicate does not fail, it answers `false` and Quark acts on that
  answer ([ADR-0023](0023-driver-modules.md)) — so a fourth field would make
  every driver written before it fail to register. A field added to the
  record is a method added to an interface by other means.
- A type marked *yes* stays an extension point for all of `v1`. Moving it to
  *no* would let it gain methods, so it is a change of the same kind and waits
  for `v2`. The other direction — plumbing declared an extension point — only
  adds a promise, and is allowed.
- The rule holds whatever the type's stability: an *experimental* extension
  point does not change within `v1` either. A new extension point is under the
  rule from the first release that contains it.

### 3. How an extension point gains a capability instead

A new capability of an extension point is a new interface, optional, beside it:

1. It is declared where the extension point is declared — in `quarkdriver`
   for anything a driver or a dialect implements, naming no type of package
   `quark` ([ADR-0026](0026-dialect-contract-in-quarkdriver.md): new dialect
   contract is born in the leaf).
2. Quark checks for it with a type assertion on the value it was handed, at
   the point where it needs the answer:
   `ra, asks := d.(quarkdriver.ReferentialActioner)`.
3. A value that does not implement it gets a documented default, chosen so
   that an implementation written before the interface existed keeps
   compiling and keeps working. As a rule the default is what Quark did
   before; where Quark did nothing before, it is `ErrUnsupportedFeature` or a
   refusal before any statement is sent. A default that differs from what
   Quark did before is a behaviour change, and § 5 says how that is made.
4. Its godoc says what Quark asks, when, and the default. Optional interfaces
   are not promoted through an embedded interface value, so a family of them
   says that a wrapper must forward them (`quarkdriver/schema.go` does).
5. In the same pull request it gets its row on the contract page — *yes*, and
   *experimental* until an implementation outside the repository has used it
   — and its lines in the guard's record (§ 7).

A function type gains a capability the same way: a new option, or a new
function type beside it, never a parameter added. A new question about a
driver's errors is a registration of its own with a default for a driver that
registers nothing, not a field of `Classifier`.

### 4. A plumbing interface may gain a method in a minor release

- A plumbing interface may gain a method in a minor release — never in a
  patch, which adds no API. The release notes of that minor name the
  interface and the method.
- Its existing methods are neither removed nor changed within `v1`: that
  breaks the code that calls them, and calling them is what the versioning
  promise covers for every exported type.
- The plumbing function types keep their signatures within `v1`. This
  decision gives them no new freedom: a function type has no methods to gain,
  and a changed signature breaks the code that builds or calls its values —
  for `TypedScanner` and `TypedBinder`, the code `quark gen` has already
  written in an application's repository.
- What is no longer covered is an implementation of a plumbing interface
  written outside Quark: a test double of `Executor`, an `Event` built by
  hand, an executor a middleware substitutes for the one it was handed. It may
  stop compiling in a minor release. The way to write one that keeps
  compiling is to embed the interface in the type, so that a method added
  later is promoted from the value embedded:

  ```go
  type countingExecutor struct {
      quark.Executor // the value Quark handed over
      statements atomic.Int64
  }

  func (c *countingExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
      c.statements.Add(1)
      return c.Executor.ExecContext(ctx, query, args...)
  }
  ```

### 5. What counts as breaking

| Change | Extension point | Plumbing |
| --- | --- | --- |
| A method added to an interface | breaking — `v2` | allowed in a minor; the notes name it |
| A method removed, or its signature changed | breaking — `v2` | breaking — `v2` |
| A function type's signature changed | breaking — `v2` | breaking — `v2` |
| A field added to, removed from or changed in `Classifier` | breaking — `v2` | — |
| The type moved to the other category | breaking — `v2` (*yes* → *no*) | allowed (*no* → *yes*) |
| An optional interface made required — folded into the one it extends | breaking — `v2` | — |
| A new optional interface, with a default | allowed in a minor | — |
| A new type, of either category | allowed in a minor, with its row | allowed in a minor, with its row |

The table is about the shape a compiler checks. What Quark passes to an
extension point and when it calls it — the last column of the contract page,
and the godoc — is behaviour, and changes under the promise's existing rule
for behaviour: a fix that changes observable behaviour is called out in the
release notes. The default an optional interface's absence gets is behaviour
in that sense.

A breaking change is a major of Quark and, by the suite's QADR-0002, of the
whole suite: it carries `!` or `BREAKING CHANGE:` and is decided before it is
merged.

### 6. What each stability promises, and what changes at v2

- **stable** — Within `v1` it changes only as §§ 2–4 allow. A change that
  would break an implementation waits for `v2` and is announced first: a `v1`
  release deprecates it (`Deprecated:` in the godoc, and the release notes)
  before `v2` changes it.
- **experimental** — Within `v1`, exactly as stable: nothing breaks. No
  implementation outside the repository had used it when it was published, so
  `v2` may change its shape without a deprecation in `v1`. At `v2` each
  experimental type is either declared stable or reshaped.
- **internal-use** — Exported because Quark's own code needs it; not to be
  implemented by hand. Within `v1` it keeps its shape, so code an earlier
  `quark gen` wrote keeps compiling. At `v2`, `NewListenerFunc` is removed, as
  its deprecation already says, and `TypedScanner` and `TypedBinder` may
  change together with `quark gen`, whose output is regenerated rather than
  edited.

`v2` — module path `github.com/jcsvwinston/quark/v2` — is the one window in
which an extension point may gain, lose or change a member, an optional
interface may be folded into the one it extends, `Classifier` may change, and
a type may leave the extension points. The policy itself does not end there:
within `v2.x` the same two categories and the same rules hold for the `v2`
contract, and the contract page and the guard's record are cut again for it
at the major.

### 7. Enforcement

- **The classification** — `CON-01` in `internal/extbench`: every
  implementable type has exactly one row, with *yes* / *no* and a stability.
- **The signatures** — `acceptance/apisurface.json` records the signature of
  every func, method and type, and `CON-02` checks that it holds the
  compiler's signature for every member an implementation depends on, so a
  change to any member shows up in the diff of `make regen`.
- **The rule** — `TestExtensionPointsFrozen`
  (`internal/extbench/frozen_points_test.go`) compares the shape of every type
  the page marks *yes* — an interface's methods, a function type's signature,
  `Classifier`'s fields, rendered as `apisurface.json` renders them — with the
  record `internal/extbench/testdata/extension-points.txt`: 41 types and 76
  members on the date of this decision. It fails when an extension point
  gained, lost or changed a member, and when a recorded type is no longer
  marked *yes* or no longer exists. A type marked *yes* that the record lacks
  — a new extension point, the way § 3 adds capability — fails with the lines
  to add. `apisurface.json` alone cannot enforce the rule, because
  `make regen` rewrites it and a method added together with the regenerated
  file passes; the record has no generator, so an entry of a type already in
  it changes only by hand, in a diff that edits what this ADR froze.
  `TestExtensionPointsFrozenBites` runs each forbidden change against a small
  record and checks that it is reported. Like `CON-01`, the guard reads the
  compiler's export data with a child `go` command, so it runs in the full
  lane (`go test ./...`, which `make check` and CI's *Test (SQLite)* job run)
  and is skipped under `-short`.
- **Not enforced by a test** — that a method added to a plumbing interface is
  named in the release notes, and the behaviour rules of § 5. Those are
  review's.

## Consequences

- For code that implements an extension point, the promise is the one it had,
  and it is now checked by a test instead of by a reading of a sentence.
- For code that implements a plumbing interface, the promise is narrower than
  the literal reading of `upgrade.mdx`: such an implementation may stop
  compiling in a minor release. The pages say so, and say how to write one
  that does not (embed the interface). No plumbing interface has gained a
  method yet, so nothing breaks on the day of the decision; the first time one
  does, the release notes name it. The narrowing itself is announced in the
  release notes of the first release after this decision.
- Quark can grow `Event`, `Expr`, `Executor`, `IdentifierValidator` and the
  four migration-lock adapters in a minor release instead of in `v2`.
- Capability keeps accumulating as small optional interfaces: `Dialect`
  already has eleven (`SavepointDialect`, `ColumnTypeMapper`,
  `SchemaIntrospector`, `MigrationLocker` and the seven experimental ones), and
  a wrapper of a built-in dialect has to forward each one it wants
  (`website/docs/reference/api/dialects.mdx`, *Wrapping a built-in
  dialect*). Whether to
  fold some of them into `Dialect` is a `v2` question.
- Adding an extension point costs one more file in the pull request: the
  record's lines, which the guard's failure prints.

## Alternatives considered

- **Keep the literal promise for every exported type.** It froze the
  interfaces Quark implements and hands out as hard as the ones it calls, so
  a capability Quark wanted to offer through `Event`, `Expr` or `Executor`
  waited for `v2` or went through a side channel. What it protected was code
  that implements a type meant to be received — test doubles, mostly — and
  that code can embed the interface and keep compiling.
- **Let extension points grow as well, through a base type to embed** (the
  `Unimplemented…Server` pattern of gRPC, or a `quarkdriver.BaseDialect`
  whose methods return the defaults). It works only for implementations that
  embed the base; none outside Quark does today, so within `v1` a method
  added would still break all of them. It is an option for the `v2` contract.
- **Let *experimental* types break in a minor release.** The page had already
  said that nothing breaks within `v1`, *experimental* included, and a label
  that allows a break in a minor would make every driver author pin exact
  versions of Quark to be safe.
- **Rely on `acceptance/apisurface.json` as the freeze.** It records shapes,
  not which shapes are promised, and `make regen` rewrites it: a method added
  to `Dialect` with the file regenerated in the same pull request passes every
  check the file feeds.
- **Decide each case in the release notes.** An implementer cannot plan on a
  rule that is written after the change.

## When to reopen

- At `v2`: the categories, the record and the page are cut again for the `v2`
  contract. This decision carries over unless a successor ADR says otherwise.
- If evidence shows a type is in the wrong category — a type marked *yes*
  that nothing outside Quark implements and Quark needs to grow — moving it
  to *no* is a `v2` change and a successor ADR, not an edit of the page.
