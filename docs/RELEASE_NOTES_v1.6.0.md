# Release notes — v1.6.0

A minor release about the sharpest edge in multi-tenancy, plus the test kit
the ORM never shipped.

## Added

- **`quarktenant.VerifyRLSPolicies` — a preflight for native row-level
  security.** Configure `RowLevelSecurityNative` and forget the policy DDL,
  and Quark emits no tenant predicate at all: every tenant reads every row,
  with no error anywhere. Nothing checked for it. The new call verifies, per
  registered model, that row security is enabled, that `FORCE` is set (unless
  you opted out — without it the table owner bypasses the policy, and the
  application role usually *is* the owner), and that the policy exists. Call
  it at startup and fail the boot, or gate a deploy with the
  `verify-rls-policies` action of your tenant runner, which exits `1` when a
  table is unenforced — distinct from `2` for operational errors.
- **`quarktest` — the test kit.** `SQLite(tb)` opens a client on a temporary
  file (not `:memory:`, where every pooled connection would get its own empty
  database), `Migrate(tb, client, models…)` brings the schema up in one line
  and surfaces tag typos immediately, and `Tx(tb, client, fn)` runs a test
  inside a transaction that always rolls back. A new testing guide walks
  through it, including when a real engine is still required.

## Fixed

- **`quark migrate up` applied only the first pending migration.** Without
  `--steps`, `up` and `down` shared one flag variable, so `up` inherited
  `down`'s default of 1: three pending migrations in a pipeline, one applied,
  exit code 0. `--dry-run` previewed a single migration for the same reason.
- **The documentation site did not build.** The version marker was written as
  an HTML comment in an MDX page, which aborts compilation. It now uses MDX
  comment syntax, and a lint rule rejects HTML comments in `.mdx`.

## Changed

- **The documentation archive resumes.** The site keeps a snapshot per
  published minor so readers pinned to an older release get the matching
  docs; that archive had frozen at 1.2.2. It resumes with this release, and a
  check now fails when a minor ships without its snapshot.
- **Editorial pass over the published documentation** — shorter sentences,
  the conclusion first, and no wording that assumes knowledge of the
  project's internal history.


Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>
