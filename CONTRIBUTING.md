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

> **Version-neutral scopes:** commits that only touch the test-harness areas
> (`examples/superapp/`, `bugbash/`, `benchmarks/`, `TASKS.md`) are listed in
> `exclude-paths` of `release-please-config.json` — a `feat(superapp):` does
> **not** bump the library version nor appear in its CHANGELOG. The released
> version reflects library changes only.

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

### All engines via Docker Compose

```bash
docker compose -f docker-compose.test.yml up -d
go test ./... -tags integration
docker compose -f docker-compose.test.yml down
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
- **`golangci-lint`** — run `golangci-lint run ./...` before submitting. The config is at `.golangci.yml` (if missing, defaults apply).
- **No `interface{}` in public APIs** — Quark's core value proposition is type safety. Generics or concrete types only.
- **Error handling** — always wrap errors with `fmt.Errorf("...: %w", err)` for caller unwrapping.
- **No silent failures** — functions that can fail must return `error`.
- **Tests alongside code** — add `_test.go` in the same package. Integration tests (requiring external databases) must be guarded by env-var checks or build tags.
- **Comments on exported symbols** — every exported type and function must have a Go doc comment.
