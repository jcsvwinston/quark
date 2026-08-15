# Release notes — v1.4.1

**The CLI keeps its promises.** v1.4.1 is a patch release that repairs three
CLI defects surfaced by an external coverage exercise over the public module
surface (tracked as QCD-CLI-1/2/3), plus a round of CLI papercuts. No API
breaking changes; two deliberate behaviour changes are called out below.

Docs (1.4.1 is the current version): <https://jcsvwinston.github.io/quark/docs/>

## Fixed

- **QCD-CLI-1 — `model generate --fields` now compiles and declares its PK.**
  The definition path never computed the import flags, so a `time.Time` or
  `json.RawMessage` field produced Go that failed with `undefined: time` —
  while the command exited 0. It also computed a `pk,auto` tag the template
  never rendered, leaving the struct with no declared primary key. Now: the
  import block is derived from the fields on every path, `id` gets `pk:"true"`
  (the tag the ORM actually parses), and the template renders the computed
  `quark:"..."` tag — which now only carries vocabulary the ORM understands
  (`not_null` for NOT NULL non-PK columns on the from-table path).

- **QCD-CLI-2 — the embed recipe no longer swallows errors.** The recipe the
  CLI itself printed (`func main() { commands.Execute() }`) discarded the
  returned error; with `SilenceErrors` on every subcommand, a runner built
  from the documentation exited 0 in silence on ANY failure — exactly where
  CI gates look. New entry point **`commands.Main()`** (execute, print to
  stderr, exit 1) is now what every recipe prescribes, and the standalone
  binary delegates to it. `commands.Execute()` keeps its signature and
  behaviour for mains that handle the error themselves.

- **QCD-CLI-3 — `tenant provision` completes under `schema_per_tenant` and
  retries are safe.** Provision used to chain `tenant migrate`
  unconditionally; that step rejects `schema_per_tenant` by design, so the
  command could never finish and left a partial provision (schema + registry
  row) whose retry crashed on duplicate `CREATE SCHEMA`. Now provision
  creates the schema and the `quark_tenants` row and **explicitly skips**
  migrations with a pointer at the TenantRouter runner (exit 0 — that is the
  complete effect), and an id already present in `quark_tenants` is rejected
  with a clear "already provisioned" error **before any DDL runs**. The
  registry insert is now a hard error instead of a warning (the registry is
  the idempotency source of truth).

## Papercuts

- `migrate status` (and `version`) on a fresh database now report zero
  applied migrations instead of a raw `relation "quark_migrations" does not
  exist` error, and `status` finally lists **pending** migrations (sorted;
  with an empty registry it says the pending set is unknown in this binary
  rather than implying "none").
- `seed run` without `--name` (and `seed list`) now honour registration
  order, as promised — they iterated a Go map before, so every run used a
  different order.
- `quark init` reads the directory's `go.mod` and fills in
  `project.module`/`project.name`; the `github.com/user/myapp` placeholder
  remains only as a fallback.
- **Behaviour change:** `quarktenant.InstallRLSPolicies` is now re-runnable —
  the rendered DDL drops the deterministic policy (`DROP POLICY IF EXISTS`)
  before recreating it, inside the same transaction. Re-running after adding
  a model converges to one policy per table instead of failing with SQLSTATE
  42710. If you depended on the duplicate-object failure as a guard, gate the
  call yourself.
- The `With(...)` docs example used a two-argument `Join(table, on)`; the
  real signature is `Join(table).On(left, op, right)`.

## Security

- Go toolchain to **go1.26.6**: closes GO-2026-6090 (crypto/tls),
  GO-2026-6088 (encoding/xml) and GO-2026-5972 (encoding/asn1), all
  reachable from Quark code paths per govulncheck.

## Upgrade

```bash
go get github.com/jcsvwinston/quark@v1.4.1
go install github.com/jcsvwinston/quark/cmd/quark@v1.4.1
```

If you embed the CLI, switch your runner's `main` to `commands.Main()` — a
bare `commands.Execute()` compiles but silently discards failures.
