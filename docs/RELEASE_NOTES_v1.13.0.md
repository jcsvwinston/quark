# Release notes — v1.13.0

A packaging release: the library's module graph loses two thirds of its
entries and the binary it produces does not change. The CLI becomes a module
of its own, with its own tag series, which is the one thing here that changes
what an installed command reports about itself.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>

## Added

- **The CLI, the acceptance harness and the engine suites move to modules of
  their own** ([#377](https://github.com/jcsvwinston/quark/issues/377),
  [ADR-0024](https://github.com/jcsvwinston/quark/blob/main/docs/adr/0024-cli-en-modulo-propio.md)).
  What shared a module with the library were the two things that need every
  engine: `cmd/quark`, which points at whatever database it is given, and the
  per-engine suites, which start real databases in containers. Their
  requirements sat in the root `go.mod`, so they sat in the build list of
  every program that imported Quark. Measured on the same consumer, with the
  same commands, before and after: **123 → 39** modules in the build list,
  **6 → 1** driver requirements, **5 → 0** `testcontainers-go` requirements.
  The binary links the same 11 modules and the same 162 packages as before,
  and is the same size.

  This is graph hygiene, not a faster or smaller program. What it changes is
  what an SBOM, a dependency dashboard, `go mod download all` and any scanner
  reading `go list -m all` say about code that was never linked into anything.
  The driver that stays is `modernc.org/sqlite`: the library keeps white-box
  tests that open a real database through unexported methods, which no test
  outside the package can reach.

- **Six fuzz targets over the surfaces that read untrusted input**, plus a
  short seeded lane that runs them on every pull request
  ([#365](https://github.com/jcsvwinston/quark/issues/365)). The identifier
  guard, the `ON`-clause guard, the JSON-path guard, the raw-query masker, the
  `db` tag reader and the six dialects' quoting each assert a property rather
  than merely not crashing — a hole in the first three is an injection, not a
  panic. The seed corpora are the existing table-test inputs plus the shapes
  that broke before, committed under `testdata/fuzz/` so they also run as
  ordinary subtests on every `go test`.

## Fixed

- **A `db` tag's column name is read the same way on every path**
  ([#365](https://github.com/jcsvwinston/quark/issues/365)).
  `ColumnFromDBTag` trimmed the name only when the tag carried options after a
  comma, while `parseDBTag` trimmed unconditionally. On a tag with no options
  the two readers of that one string gave two answers, so which column a field
  mapped to depended on which path reached the tag. Found by the first seed of
  the new `FuzzColumnNaming` target, whose property is that the two agree.

## Upgrading

**Upgrade the driver modules together with the library.** `drivers/mssql`,
`drivers/mysql`, `drivers/oracle` and `drivers/sqlite` at v0.1.x import
`internal/driverclassify`, which left the root module with the CLI in this
release. Holding one of them while moving the library to v1.13.0 does not
build:

```
module github.com/jcsvwinston/quark@v1.13.0 found, but does not contain
package github.com/jcsvwinston/quark/internal/driverclassify
```

Moving them to v0.2.0 fixes it, and is worth doing for its own sake: those
predicates covered all six engines, so requiring any one driver used to pull
the other five into your module graph. `drivers/postgres` was never affected —
it classifies through the public `SQLState()` method.

## Installing the CLI

`go install github.com/jcsvwinston/quark/cmd/quark@latest` keeps working and
installs the same program; the import path does not change. What changes is
what it resolves against.

- `@latest` now resolves the `.../cmd/quark` module, whose tags are
  `cmd/quark/vX.Y.Z`. **This release publishes `cmd/quark` v1.0.0.** The
  number restarts because the series is new, not the program: it is the same
  CLI, under its own version from here on, and `quark version` reports that
  series rather than the library's.
- A selector naming a **root** version stops working for releases after this
  one. A nested module's directory is outside its parent, so `v1.13.0` and
  every later root tag no longer contain the package, and
  `go install .../cmd/quark@v1.13.0` fails. Root tags up to `v1.12.0` still
  work, which makes the failure read as intermittent — old versions install,
  new ones do not. Pin `cmd/quark/v1.0.0` instead.

## Modules

- `cmd/quark` **v1.0.0** — first release of the CLI as its own module. It
  requires the library at v1.13.0 and the five driver modules at v0.2.0, and
  carries no `replace` directive, which is what lets `go install` accept it.
- `drivers/postgres`, `drivers/mysql`, `drivers/sqlite`, `drivers/mssql` and
  `drivers/oracle` **v0.2.0** — nothing changes in what they register; they
  follow the root out of the split.
- `examples/superapp` and `internal/enginesuite` are modules now too, and are
  deliberately **not** published: the repository consumes them through a local
  `replace`, the way `benchmarks` and `bugbash` are consumed.
