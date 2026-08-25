# Release notes — v1.6.1

**The row-level security preflight now reads the policy, not just its name.**
v1.6.1 is a security patch for the guardrail that shipped in v1.6.0: it
certified as correct a real cross-tenant leak.

Docs (1.6.1 is the current version): <https://jcsvwinston.github.io/quantum/quark/intro/>

Origin: re-verification of the external `quantum-coverage-demo` against the
published suite. Findings QCD-QK-1, QCD-QK-2 and QCD-QK-3, all in
`quarktenant`.

## Fixed

- **QCD-QK-2 (security, high) — `verify-rls-policies` certified a real
  cross-tenant leak as correct.** The package presents itself as the
  preflight that makes the silent Native-RLS leak loud. Its actual check
  was `EXISTS (SELECT 1 FROM pg_policies WHERE policyname = $2)`: the
  policy's NAME, never `qual` nor `with_check`.

  Measured repro: a table with `org_id`, RLS enabled, FORCE set, and
  `CREATE POLICY … USING (true) WITH CHECK (true)` → the preflight exited
  0 with "every registered model's table is enforced", while a
  non-superuser role reading through the Native router with
  `app.tenant_id=1` saw the other organisation's rows.

  The check now reads both expressions and requires the two things that
  make them isolate at all: a reference to the tenant column and a read of
  the expected session variable. It also catches a policy narrowed to a
  single command by a later `ALTER POLICY`, and one whose write path is
  left open.

  One trap the tests pin: the default variable `app.tenant_id` CONTAINS the
  default column `tenant_id` as a substring, so looking for the column
  naively accepts `status = current_setting('app.tenant_id', true)`, which
  isolates nothing. The `current_setting` literal is stripped before the
  column is looked for.

  Scope, stated so the guarantee is not overread: it verifies THE policy
  this package installs (one policy, `FOR ALL`). A deployment isolating
  through several hand-written policies, or restricting by role, is
  reported as a deviation rather than guessed at.

- **QCD-QK-1 (operational, high) — the preflight demanded an undocumented
  `AllowRawQueries`.** It used `client.RawQuery`, disabled by
  `DefaultLimits`, so the client `run.go` itself prescribes —
  `quark.New("pgx", dsn)` with models registered — could not run it. It
  failed closed (no false OK), but a boot or CI guardrail failed
  indistinguishably from a real outage, and the remedy appeared in no
  godoc. It now reads the catalog through `client.Raw()`, the same path
  `InstallRLSPolicies` already uses for the same reason: the query is built
  from registered model metadata, never from caller input.

  The doubled `quarktenant: quarktenant: …` prefix is fixed as well.

- **QCD-QK-3 (medium) — `verify` accepted install flags and ignored them.**
  Both actions shared one `FlagSet` and verify read only `ForceRLS`, so
  verifying with the default `--tenant-col` against an installation made
  with another column returned an OK that had not checked what the caller
  believed. `--tenant-col` and `--native-rls-var` now genuinely change the
  verdict, because the predicate check needs them. Flags that cannot change
  a read-only verdict (`--dry-run`, `--cast`, `--lock-name`,
  `--lock-timeout`) are refused for this action rather than dropped.

## Upgrading

No API changes. A deployment whose policies were already correct verifies
clean as before. A deployment that was passing the preflight with a
permissive or misdirected policy will now FAIL it — which is the point, and
worth reading carefully before dismissing.
