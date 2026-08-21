# Release notes — v1.5.2

**A bare `quark migrate up` applies every pending migration again.** v1.5.2
is a single-fix CLI patch for the "exit 0 with no effect" class.

Docs (1.5.2 is the current version): <https://jcsvwinston.github.io/quantum/quark/intro/>

## Fixed

- **`migrate up` without `--steps` used to apply only the FIRST pending
  migration and exit 0.** The `--steps` flags of `migrate up` (default 0 =
  all pending) and `migrate down` (default 1) were registered on the same
  package-level variable, and pflag writes each default into the bound
  variable at registration time — so after `init()` the shared variable
  held down's `1`. With three pending migrations in a CI pipeline, one was
  applied and the job went green. The flags now use separate variables;
  `--dry-run` was affected the same way (it previewed a single migration)
  and is covered by the same fix.
- The regression test drives the real command line (`migrate up` with no
  flags) with **two** registered pending migrations — every earlier test
  used exactly one, where the truncation is invisible — and also pins the
  inverse contract: a bare `migrate down` still reverts only the latest
  migration.
