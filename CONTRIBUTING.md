# Contributing to Quark

Thank you for considering a contribution to Quark! This document explains how to get started, what conventions we follow, and how to run the test suite.

---

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [How to Report Issues](#how-to-report-issues)
- [How to Submit a Pull Request](#how-to-submit-a-pull-request)
- [Commit Conventions](#commit-conventions)
- [Development Setup](#development-setup)
- [Running the Test Suite](#running-the-test-suite)
- [Coding Style](#coding-style)

---

## Code of Conduct

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). By participating you agree to abide by its terms.

---

## How to Report Issues

Before opening an issue, please search existing issues to avoid duplicates.

Use the appropriate [issue template](.github/ISSUE_TEMPLATE/):
- **Bug** — unexpected behaviour, panics, wrong SQL generated.
- **Feature** — new capability you would like to see.
- **Question** — usage questions not answered by the docs.

Include the Go version (`go version`), Quark version, database engine, and a minimal reproducer.

---

## How to Submit a Pull Request

1. **Open an issue first** for any non-trivial change so we can discuss direction before you invest time coding.
2. Fork the repository and create a branch:
   ```bash
   git checkout -b feat/my-feature   # for new features
   git checkout -b fix/issue-123     # for bug fixes
   ```
3. Make your changes following the [Coding Style](#coding-style) section.
4. Add or update tests — PRs that reduce coverage without a documented reason will not be merged.
5. Run the full test suite locally (see below) and ensure it passes.
6. Open a Pull Request against `main` using the [PR template](.github/PULL_REQUEST_TEMPLATE.md).
7. At least one maintainer review is required before merge.

---

## Commit Conventions

Quark uses **Conventional Commits** (`<type>(<scope>): <subject>`).

| Type | When to use |
|------|-------------|
| `feat` | New feature or behaviour |
| `fix` | Bug fix |
| `perf` | Performance improvement |
| `refactor` | Code change that is neither a fix nor a feature |
| `test` | Adding or improving tests |
| `docs` | Documentation only |
| `ci` | CI/CD configuration |
| `chore` | Dependency updates, tooling |

Examples:

```
feat(dialect): add MariaDB RETURNING clause support
fix(batch): prevent off-by-one in chunk size calculation
docs: add comparison table justifications
```

> **Version-neutral scopes:** PRs that only touch the test-harness areas
> (`examples/superapp/`, `bugbash/`, `benchmarks/`, `TASKS.md`) must use the
> `test` or `chore` types — a `feat`/`fix` there bumps the library version and
> enters the library CHANGELOG, which records library-level changes only.
> (`release-please-config.json` also lists these paths under `exclude-paths`
> as a second barrier, but the bundled release-please does not currently apply
> it to the root package, so the type convention is the effective one.)

Breaking changes must include `BREAKING CHANGE:` in the commit body and a `!` after the type:

```
feat(query)!: rename WhereSubquery to WhereRaw

BREAKING CHANGE: WhereSubquery has been renamed to WhereRaw for clarity.
```

---

## Development Setup

```bash
git clone https://github.com/jcsvwinston/quark.git
cd quark
go mod download
```

SQLite tests run with no external dependencies:

```bash
go test ./... -run TestSQLite
```

---

## Running the Test Suite

### SQLite (no external dependencies)

```bash
go test ./...
```

### PostgreSQL

```bash
export QUARK_TEST_POSTGRES_DSN="postgres://quark:quark@localhost:5432/quark_test?sslmode=disable"
go test ./... -tags integration
```

### MySQL / MariaDB

```bash
export QUARK_TEST_MYSQL_DSN="quark:quark@tcp(localhost:3306)/quark_test?parseTime=true"
go test ./... -tags integration
```

### MSSQL

```bash
export QUARK_TEST_MSSQL_DSN="sqlserver://quark:Quark1234!@localhost:1433?database=quark_test"
go test ./... -tags integration
```

### Oracle

```bash
export QUARK_TEST_ORACLE_DSN="oracle://quark:quark@localhost:1521/ORCLPDB1"
go test ./... -tags integration
```

### All engines

Postgres, MySQL, MariaDB and MSSQL boot in-process via testcontainers when
you run with `-tags integration`; Oracle needs the shared bootstrap script
(the same one CI uses — readiness wait + `GRANT EXECUTE ON DBMS_LOCK`):

```bash
make oracle-up
export QUARK_TEST_ORACLE_DSN=oracle://quark:quark@localhost:1521/FREEPDB1
make test-all
```

### Benchmarks

```bash
# SQLite only (fast, no external deps):
go test -run TestBenchmarkEngines -v

# All engines (requires DSN env vars set above):
go test -run TestBenchmarkEngines -v -timeout 10m
```

---

## Coding Style

- **`gofmt`** — all code must be formatted with `gofmt`.
- **`go vet` + `gofmt`** — what CI's Lint lane actually runs; `make lint` reproduces it. (There is no golangci-lint config in this repo.)
- **No `interface{}` in public APIs** — Quark's core value proposition is type safety. Generics or concrete types only.
- **Error handling** — always wrap errors with `fmt.Errorf("...: %w", err)` for caller unwrapping.
- **No silent failures** — functions that can fail must return `error`.
- **Tests alongside code** — add `_test.go` in the same package. Integration tests (requiring external databases) must be guarded by env-var checks or build tags.
- **Comments on exported symbols** — every exported type and function must have a Go doc comment.

---

## Before opening a PR: `make check`

`make check` reproduces CI's cheap lanes locally so the PR does not go red
on things you could have caught in seconds: vet+gofmt, the three docs
guards (product voice, docs lint, roadmap), version coherence, apisurface/
allowlist freshness, the static builds (`CGO_ENABLED=0` and cross-compile),
and the unit tests. The expensive lanes have their own targets — `make
test-race`, `make test-all` (engine matrix), `make superapp` — and
`make help` lists everything.

Two guards you WILL meet on your first API change:

- **apisurface/allowlist freshness**: any new exported symbol requires
  regenerating both files in the same change — `make regen` (order
  matters: the allowlist reads the surface). If the symbol cannot be
  exercised by the superapp (needs a live engine, or takes `testing.TB`),
  add a REASONED entry to `examples/superapp/cmd/gen-allowlist/main.go`
  and regenerate — an unclassified symbol fails the strict gate.
- **version coherence** (release PRs only): `scripts/check-version-coherence.sh`
  demands the docs bump in the same PR; release-please handles the version
  mentions and `scripts/release/gen_release_notes_skeleton.sh` writes the
  release-notes skeletons.
