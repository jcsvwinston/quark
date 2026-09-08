# Deprecation Notice: the `RowLevelSecurity` tenant-strategy alias

- ID: `DEP-2026-001`
- Status: `active`
- Announced in: `v0.9.0` (2026-05-21), re-dated by this notice
- Earliest removal: `v2.0.0`, no earlier than `2026-12-08`
- Scope: `api`
- Affected lifecycle tag: `stable`
- Owner: `@jcsvwinston`

## Summary

`quark.RowLevelSecurity` is the pre-`v0.9.0` spelling of the tenant strategy
now called `quark.RowLevelSecurityClient`. `v0.9.0` renamed it because the old
name claimed more than the strategy delivers: it is client-side `WHERE`
injection performed by the query builder, so `client.Raw()` and
`client.Exec()` bypass the predicate. The engine-enforced variant that the
name suggested arrived later and is a different constant,
`RowLevelSecurityNative` (PostgreSQL only). The rename kept both readings
apart; the alias kept the old spelling compiling.

This notice exists because the marker made a promise that expired without
anyone noticing. Its wording was:

> Deprecated: use RowLevelSecurityClient. The alias is scheduled for removal
> in v1.0.

Quark reached `v1.0.0` with the alias in place and is on `v1.12.0` with the
alias still in place. The sentence had been false for twelve minor releases:
a reader who believed it would have concluded the symbol was already gone.
Nothing checked it, because until now nothing read these paragraphs.

The alias is *not* being removed here, and re-dating it is not a delay
imposed for convenience. Removing it in `v1.x` would contradict the README's
own promise (`v1.x` keeps API compatibility), and the original `v1.0` target
was written before that promise existed. `v2.0.0` is the first release where
the removal is permitted at all.

## Affected surfaces

- `quark.RowLevelSecurity` — a constant of type `quark.TenantStrategy`,
  declared in `tenant_router.go` as
  `const RowLevelSecurity = RowLevelSecurityClient`.

That is the entire surface. `TenantConfig.Strategy`, the router's dispatch and
every dialect path are reached through `RowLevelSecurityClient`; the alias
only exists at the call site.

## Migration path

- **Replacement:** `quark.RowLevelSecurityClient`.
- **Behaviour differences:** none. The two names are the same constant with
  the same value, so a program that renames its call sites behaves
  identically before and after.
- **Required app changes:** a textual rename wherever the old spelling is
  written, most often in a `TenantConfig` literal:

  ```go
  // before
  cfg := quark.TenantConfig{Strategy: quark.RowLevelSecurity, BaseClient: c}
  // after
  cfg := quark.TenantConfig{Strategy: quark.RowLevelSecurityClient, BaseClient: c}
  ```

- **Serialized configuration is unaffected.** What a config file or a database
  column holds is the constant's *value*, not its Go identifier, and the value
  does not change. Nothing needs migrating on disk.

## Detection

- Source scan: `RowLevelSecurity` not followed by `Client` or `Native`. The
  canonical names share the prefix, so a bare `grep RowLevelSecurity` reports
  every correct call site too:

  ```sh
  grep -rn 'RowLevelSecurity\([^CN]\|$\)' --include='*.go' .
  ```

- Tooling: staticcheck reports the alias as `SA1019`, and gopls marks it
  struck through in an editor, as soon as the consumer builds against a Quark
  that carries the marker.
- There is no runtime or log signature. The alias compiles to the same value,
  so a running program cannot tell which spelling produced it — the source
  scan is the only detection.

## What breaks if this is ignored

At `v2.0.0` the constant is gone and a program that still writes
`quark.RowLevelSecurity` fails to compile with `undefined: quark.RowLevelSecurity`.
That is a build-time failure, not a silent behaviour change: no query starts
returning different rows, and no tenant boundary moves. The cost of ignoring
this notice is a compile error at upgrade time, and the fix is the same
one-word rename described above.

## Removal window

- **Why `v2.0.0`:** a stable surface is not removed in `v1.x`. Removals are
  major-only, and a Quark major moves the whole suite, so the removal lands
  where the majors land and nowhere earlier.
- **Why `2026-12-08`:** the suite's support policy sets a minimum of 90
  calendar days of notice before a deprecated surface becomes eligible for
  removal. This notice is dated 2026-09-09; 90 days later is 2026-12-08.
- The two are independent. The date says when removal becomes *permitted*;
  the version says where it can actually land. Whichever comes second
  governs.

> The 90-day figure comes from the suite-wide support policy, which is drafted
> but not yet approved at the umbrella. If the owner settles on a different
> number, this notice and the marker in `tenant_router.go` are re-dated
> together — they must not drift apart, and the suite guard is what keeps
> them honest.

## Validation

- **Compatibility test:** `TestRowLevelSecurityAliasBackwardCompat` in
  `tenant_router_test.go` asserts that both spellings name the same value and
  that the alias still type-checks inside a `TenantConfig`. It carries a
  sunset comment pointing at the alias declaration; that comment was re-dated
  alongside the marker.
- **Exported-surface manifest:** the alias is recorded in
  `examples/superapp/allowlist.json` with its reason, so the acceptance gate
  does not demand that a deprecated symbol be exercised.
- **Release note updated:** no. This is a documentation and godoc change; it
  does not cut a release, and there is no `Unreleased` changelog section to
  write into.
- **Rollback plan:** not applicable — no symbol is removed and no behaviour
  changes.

## Timeline

- First announced: `2026-05-21` with `v0.9.0`, against a `v1.0` removal that
  did not happen.
- Re-dated: `2026-09-09` — this notice.
- Review checkpoint: `2026-12-08`, when the window opens. Confirm the alias
  has no remaining in-repo callers beyond the compatibility test.
- Removal: at `v2.0.0` planning. The removal PR deletes the constant and
  `TestRowLevelSecurityAliasBackwardCompat` together; leaving the test behind
  would not compile.

## Notes

Two historical documents still describe the old `v1.0` target:
`docs/MIGRATION_v0.9.0.md` and `docs/RELEASE_NOTES_v0.9.0.md`. They are frozen
records of what was said at the time and are deliberately left alone —
correcting them would falsify the record. This notice is the current answer;
those files are the history.
