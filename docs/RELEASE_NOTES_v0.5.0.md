# Quark v0.5.0 — Release Notes

> **Date:** 2026-05-13
> **Status:** late-alpha. Not yet v1.0 production-ready.
> See [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) for the honest gap analysis between the current state and a planned v1.0.

Phase 0 cleanup release. No new public API, no breaking changes. The release closes the F0-1 through F0-10 backlog — the infrastructure and documentation drift that had been carried through v0.3 and v0.4. The headline change is that the cross-engine integration matrix is finally **blocking** in CI on PostgreSQL, MySQL, MariaDB, and MSSQL (Oracle remains excluded pending an image issue on hosted runners), so the project's hard rule that "tests pass on the supported engines before merge" is finally enforced instead of honor-system.

## What's in this release

### Infrastructure (Phase 0)

- **F0-8 — Integration matrix via testcontainers-go.** `containers_test.go` (gated `//go:build integration`) boots PostgreSQL, MySQL, MariaDB, MSSQL, and Oracle through testcontainers. Per-engine `setup<Engine>Container` helpers expose a DSN; `resolve<Engine>DSN(t)` picks env var → container in that order. Default `go test -short` stays SQLite-only and doesn't import testcontainers-go. CI matrix runs in parallel to the existing Lint + SQLite jobs; Docker is pre-installed on `ubuntu-latest`. Oracle is excluded from the matrix until the `gvenzl/oracle-free` image issue on hosted runners is debugged with local access — the helper stays in place for `go test -tags=integration -run TestSuiteOracle ./...` on a workstation with Oracle running.

- **F0-9 — release-please workflow.** `.github/workflows/release-please.yml` runs on every push to `main` and keeps a rolling Release PR open. The PR contains the next semver bump (from Conventional Commits) and the CHANGELOG entries since the last tag. Merging the Release PR auto-creates the git tag and the GitHub Release. The Docusaurus `docs:version` snapshot is NOT in release-please's scope — that step still runs manually via the `/release` slash command before the Release PR is merged. Config in `release-please-config.json`; current-version state in `.release-please-manifest.json`.

- **F0-10 — Docs linter.** `scripts/lint-docs.sh` runs in the `Lint` CI job. Three checks:
  - **Anti-marketing language.** `production-ready`, `enterprise-grade`, `battle-tested` are rejected in user-facing docs unless explicitly negated (`Not v1.0 production-ready`, `isn't`, `todavía no`). Quark is late-alpha until v1.0; marketing language would re-create the discrepancy that the project's first audit caught.
  - **`RELEASE_NOTES_V1` leak.** References to the deleted v1.0-marketing release notes file may not appear in user-facing docs. Historical references in CLAUDE.md / TASKS / ADRs / blog posts / versioned snapshots are exempt.
  - **Broken relative links.** Every `](path)` in `.md`/`.mdx` must resolve. Docusaurus-aware: tries `<path>`, `<path>.md`, `<path>.mdx`, `<path>/index.{md,mdx}` and treats `/docs/...` baseUrl-rooted paths as `website/docs/...`. Versioned snapshots skipped.

- **CI matrix is now blocking** on PostgreSQL / MySQL / MariaDB / MSSQL. `continue-on-error: true` was removed after the F0-8 follow-up PRs (#29–#35) closed the 11 latent test-side bugs that the first cross-engine run surfaced.

### Documentation (Phase 0 cosmetics)

- **F0-1.** `RELEASE_NOTES_V1.md` (the v1.0 marketing file) was deleted earlier; this release confirms the cleanup is complete and there are no lingering references.
- **F0-2.** `examples/blog-api/` (referenced in two places in `README.md` but never created) is replaced with pointers to the per-dialect examples under `examples/<engine>/`.
- **F0-3.** `examples/README.md` switched from `go run pkg/quark/examples/<engine>/main.go` (monorepo heritage paths) to `go run ./examples/<engine>/main.go`.
- **F0-4.** `README.md` had two near-identical Quick Start sections — deduplicated to one.
- **F0-5.** Hardcoded `Coverage 87%` badge no longer appears. The remaining badges (Go Reference, CI, Go Version, License, Release) are dynamic.

### Test-side fixes surfaced by F0-8

The first cross-engine run of the integration matrix surfaced eleven test-side bugs that had been hidden under SQLite-only CI. Eight closed cleanly in follow-up PRs A–G; one (MSSQL JSON+NVARCHAR(MAX) encoding) is a real API bug whose fix needs MSSQL local access — interim skip in place. The full list:

1. Dialect-aware quoting in `expr_ast` / `cte` / `window` integration assertions ([#29](https://github.com/jcsvwinston/quark/pull/29)).
2. Same fix removed a literal `?` placeholder check in CTE tests (PG / MSSQL / Oracle use `$N` / `@pN` / `:N`).
3–4. `having_aggregate` + `join_on_security` tests used `SELECT *` over `GROUP BY` / `JOIN`, which strict-mode engines reject. Tests now use explicit `Select("status")` or `Count()` to pin the contract without projection ambiguity ([#30](https://github.com/jcsvwinston/quark/pull/30)).
5. Setop happy-path tests now skip on MySQL / MariaDB (where `Intersect` / `Except` correctly return `ErrUnsupportedFeature`) and a mirror-contract test asserts the rejection on those engines ([#31](https://github.com/jcsvwinston/quark/pull/31)).
6. `locking_test.go` was already correct — false positive reclassified as a non-bug.
7. Nullable float roundtrip on Postgres switched from strict equality to a `math.Abs(diff) > 1e-4` tolerance (Postgres maps Go `float64` to SQL `real` 32-bit by default) ([#32](https://github.com/jcsvwinston/quark/pull/32)).
8. **MSSQL `JSON[T].Scan` returns "invalid character 'â'"** — confirmed real API bug at the NVARCHAR(MAX) → driver → `json.Unmarshal` boundary. Interim skip on MSSQL ([#33](https://github.com/jcsvwinston/quark/pull/33)); fix candidates documented in TASKS § F0-8 followup bug 8 (most likely: change MSSQL JSON column mapping to `VARCHAR(MAX)`). Will land in a focused PR once MSSQL local access is available.
9. Oracle excluded from CI matrix — `gvenzl/oracle-free:23-slim` and `:23-slim-faststart` both exit with code 1 inside ~5 seconds on hosted runners with no startup log to diagnose against. Helper stays for local use ([#34](https://github.com/jcsvwinston/quark/pull/34)).
10. MSSQL setop + auto-LIMIT triggered OFFSET/FETCH that requires an ORDER BY of columns in every operand's SELECT list. Tests add explicit `OrderBy("email", "ASC")` on the base ([#35](https://github.com/jcsvwinston/quark/pull/35)).
11. MSSQL JoinBuilder hit ambiguous-`id` on `SELECT *` over a JOIN. Tests switched to `Count()` (same fix shape as bug 4) ([#35](https://github.com/jcsvwinston/quark/pull/35)).

## Known limitations

- Quark is **late-alpha** (~v0.5). Not v1.0 production-ready. Phases 3 (schema-diff migrations), 4 (observability/metrics depth), 5 (real RLS engine via `CREATE POLICY` + `SET LOCAL`), and 6 (codegen) are not yet in scope.
- **MSSQL `JSON[T]` is broken** on roundtrip — the test is skipped on MSSQL pending the column-type fix described above. If you use `JSON[T]` against MSSQL, you'll hit the same `invalid character 'â'` error on `Scan`. Workaround: use a `string` field and `json.Marshal`/`json.Unmarshal` by hand until the fix lands.
- **Oracle has no CI coverage.** The dialect-specific paths are exercised by code review and by manual `go test -tags=integration -run TestSuiteOracle ./...` runs on workstations with Oracle. The helper boots a `gvenzl/oracle-free` container locally without issue.
- The set-op surface on MySQL / MariaDB is limited to `UNION` / `UNION ALL`. `INTERSECT` / `EXCEPT` return `ErrUnsupportedFeature` (rewrite as a JOIN). Oracle uses `MINUS` for `EXCEPT`. SQLite rejects `INTERSECT ALL` / `EXCEPT ALL`.

## Upgrading

```bash
go get github.com/jcsvwinston/quark@v0.5.0
```

No source-code changes required — v0.5 is infrastructure-only. The integration matrix becoming blocking only affects contributors (CI) and not consumers of the library.

## Versioned docs

The page-versioned site for v0.5.0 is at `https://jcsvwinston.github.io/quark/docs/0.5.0/`.
