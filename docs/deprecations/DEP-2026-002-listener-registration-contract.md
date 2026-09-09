# Deprecation Notice: the previous listener-registration shape in `quarkdriver`

- ID: `DEP-2026-002`
- Status: `active`
- Announced in: `v1.11.0` (2026-09-05)
- Earliest removal: `v2.0.0`, no earlier than `2026-12-08`
- Scope: `api`
- Affected lifecycle tag: `stable`
- Owner: `@jcsvwinston`

## Summary

`quarkdriver` is the package a driver module implements to plug an engine into
Quark. Its first listener-registration shape typed the constructor as:

```go
type NewListenerFunc func(db *sql.DB, g *guard.SQLGuard) (Listener, error)
```

where `guard.SQLGuard` is `github.com/jcsvwinston/quark/internal/guard`. Naming
an internal type in a public signature made the contract unimplementable from
outside: Go's `internal` rule is path-based, so only packages under
`github.com/jcsvwinston/quark/...` can write that parameter type. A driver
module published by anyone else could not register a listener at all — the
public entry point existed, and no third party could reach it.

`v1.11.0` re-signed the contract against an interface that states the one
thing a listener actually needs from the ORM — validating a channel name as a
SQL identifier:

```go
type IdentifierValidator interface{ ValidateIdentifier(name string) error }
type ListenerFactory func(db *sql.DB, v IdentifierValidator) (Listener, error)
```

`*quark.SQLGuard` satisfies `IdentifierValidator`, so nothing changed for the
ORM, and a driver module now implements the contract without importing an
internal package. The four members of the previous shape stayed behind as
adapters so a driver module already built against them kept registering.

This notice is one deprecation, not four. A single decision — re-signing the
registration contract — deprecates one type and three functions that only
exist to serve it; they migrate together and they are removed together.

## Affected surfaces

| Deprecated | Replacement |
|---|---|
| `quarkdriver.NewListenerFunc` | `quarkdriver.ListenerFactory` |
| `quarkdriver.RegisterListener` | `quarkdriver.RegisterListenerFactory` |
| `quarkdriver.MustRegisterListener` | `quarkdriver.MustRegisterListenerFactory` |
| `quarkdriver.LookupListener` | `quarkdriver.LookupListenerFactory` |

Not affected: `Listener`, `EventPayload`, `ErrListenerClosed` and
`ErrNoSubscription` are unchanged, and package `quark` keeps aliasing them, so
`errors.Is(err, quark.ErrListenerClosed)` reads the same before and after.

## Migration path

- **Replacement:** change the constructor's second parameter from
  `*guard.SQLGuard` to `quarkdriver.IdentifierValidator`, and register through
  the `...Factory` name:

  ```go
  // before
  func init() {
      quarkdriver.MustRegisterListener("postgres",
          func(db *sql.DB, g *guard.SQLGuard) (quarkdriver.Listener, error) {
              return newListener(db, g)
          })
  }

  // after
  func init() {
      quarkdriver.MustRegisterListenerFactory("postgres",
          func(db *sql.DB, v quarkdriver.IdentifierValidator) (quarkdriver.Listener, error) {
              return newListener(db, v)
          })
  }
  ```

  The listener's own code changes only where it called
  `g.ValidateIdentifier(channel)`; that method is exactly what the interface
  carries, so the call site is identical.

- **Behaviour differences:** none for a driver that migrates. Both names write
  into the same registry, so a duplicate registration is still rejected with
  `a listener for <engine> is already registered`, and the ORM resolves the
  listener the same way either name registered it.
- **Behaviour differences while adapting:** `RegisterListener` wraps the old
  constructor in a factory that type-asserts the validator back to
  `*guard.SQLGuard`. The ORM always hands the factory its own guard, so the
  assertion holds in normal use; a caller that supplies a different
  `IdentifierValidator` gets an error naming the concrete type, not a panic.
  `LookupListener` returns an adapter closure rather than the function that
  was registered, so the returned value is never identity-comparable with what
  the caller passed to `RegisterListener`.
- **Required app changes:** none. This is a driver-module contract; it is
  called from a driver's `init()`, not from application code. An application
  that imports `drivers/postgres` for its side effect is unaffected either
  way.

## Detection

- Source scan, in a driver module:

  ```sh
  grep -rn 'quarkdriver\.\(New\|Register\|MustRegister\|Lookup\)Listener\b' --include='*.go' .
  ```

  The `\b` matters: `RegisterListenerFactory` and its siblings share the
  prefix, and those are the names to keep.
- Tooling: staticcheck reports each use as `SA1019` once the module builds
  against `v1.11.0` or later.
- Compile-time: a module outside `github.com/jcsvwinston/quark/...` cannot be
  affected, because it was never able to name `*guard.SQLGuard` and therefore
  never able to call the deprecated registrars.

## What breaks if this is ignored

At `v2.0.0` the four names are gone. A driver module that still registers
through them fails to compile (`undefined: quarkdriver.MustRegisterListener`),
and because registration happens in `init()`, the failure is at build time —
never at runtime, and never as an engine that silently stops delivering
events. A consumer that only imports a driver for its side effect sees
nothing: the fix belongs to whoever ships the driver module.

## Removal window

- **Why `v2.0.0`:** the four names are part of the stable `v1.x` surface, and
  a stable surface is not removed in `v1.x`. Removals are major-only, and a
  Quark major moves the whole suite, so the removal lands with the majors.
- **Why `2026-12-08`:** the suite's support policy sets a minimum of 90
  calendar days of notice before a deprecated surface becomes eligible for
  removal. This notice is dated 2026-09-09; 90 days later is 2026-12-08.
- The date opens the window; the version is where the removal can land.
  Whichever comes second governs.

> The 90-day figure comes from the suite-wide support policy, which is drafted
> but not yet approved at the umbrella. If the owner settles on a different
> number, this notice and the four markers in `quarkdriver/listener.go` are
> re-dated together.

## Validation

- **In-repo consumers are already migrated.** `drivers/postgres` registers
  through `MustRegisterListenerFactory` as of `v0.1.3`, so no driver this
  repository publishes depends on the deprecated shape.
- **Backward compatibility is exercised.** `drivers/postgres/postgres_test.go`
  still calls `quarkdriver.LookupListener("postgres")` and asserts it resolves
  a listener registered through the new contract — which is precisely the
  adapter path this notice covers. That assertion is removed in the same PR
  that removes the symbols.
- **Exported-surface manifest:** all four names carry their reason in
  `examples/superapp/allowlist.json`, so the acceptance gate does not demand
  that a driver-module contract be exercised as an application entry point.
- **Release note updated:** no. This is a documentation and godoc change; it
  does not cut a release, and there is no `Unreleased` changelog section to
  write into. The contract change itself was announced in the `v1.11.0` notes.
- **Rollback plan:** not applicable — no symbol is removed and no behaviour
  changes.

## Timeline

- Announced: `2026-09-05` with `v1.11.0`, which shipped the replacement
  contract and the adapters.
- Dated: `2026-09-09` — this notice. The original markers named a replacement
  but no removal version and no date, so they read as permanent labels rather
  than as a promise with an end.
- Review checkpoint: `2026-12-08`, when the window opens. Confirm no driver
  module still registers through the previous shape.
- Removal: at `v2.0.0` planning. The removal PR deletes the type, the three
  registrars and the compatibility assertion in
  `drivers/postgres/postgres_test.go` together.

## Notes

Background on why the listener contract lives in `quarkdriver` rather than in
package `quark`, and on the dedicated-connection design it serves:
[ADR-0019](../adr/0019-inbound-listen-notify-dedicated-conn.md) and
[ADR-0023](../adr/0023-driver-modules.md).
