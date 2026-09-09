# What `go.mod` lists and what a binary links

Quark's root `go.mod` requires six database driver modules — one per engine,
and two for SQLite — and five `testcontainers-go` modules that no application
importing the library ever links. They are there because two things share the module with the library:
`cmd/quark`, a CLI that talks to whatever database it is pointed at and
therefore links every engine, and the engine test suites, which start real
databases in containers.

The v1.9.0 minor already moved the drivers out of the library's *import*
graph ([ADR-0023](adr/0023-driver-modules.md)); what is left is the *module*
graph. This page measures how large that difference is, with the command
behind every figure, so anyone can redo the measurement instead of taking the
number on trust.

Measured on 2026-09-09 against the published `v1.12.0`, with go1.26.0 and
govulncheck v1.7.0 (vulnerability database of 2026-09-02). The toolchain
matters for one row only — the standard-library advisories below are attributed
to it — and it is the same on both sides of every comparison.

## The consumer under test

A module in an empty directory that imports `github.com/jcsvwinston/quark` and
nothing else — no driver module, no test harness — resolved from the module
proxy:

```bash
mkdir /tmp/quarkconsumer && cd /tmp/quarkconsumer
export GOWORK=off GOFLAGS=-mod=mod
go mod init example.com/quarkconsumer
go get github.com/jcsvwinston/quark@v1.12.0
# main.go: the README's Quick Start program, minus the driver import
go mod tidy
```

Its own `go.mod` ends up with eleven requirements. Its build list has a
hundred and twenty-eight.

## The gap

| | consumer of `quark` v1.12.0 | split approximation |
|---|---:|---:|
| modules in the build list — `go list -m all`, not counting the consumer itself | 128 | 28 |
| modules the binary links — `go list -deps -f '{{with .Module}}{{.Path}}{{end}}' . \| sort -u` | 11 | 11 |
| modules in the build list the binary never links | 117 (91%) | 17 (61%) |
| packages the binary links — `go list -deps . \| wc -l` | 159 (26 outside the standard library) | 159, the same set |
| directories holding non-test Go sources inside modules the binary never links | 1,366 | 491 |
| binary — `go build -o app . && stat -f%z app` | 7,204,994 bytes | 7,204,994 bytes |

The last row is the point of the whole page. **The split changes nothing a
binary contains.** Both consumers link the same 159 packages from the same 12
modules and produce a binary of the same size; the drivers and the container
harness are already absent from it. What the split changes is what the module
graph says, and the graph is what an SBOM, a dependency dashboard and
`go mod download all` read.

At package level there is no gap to speak of: `go list all` returns the same
159 packages as `go list -deps .`, because module graph pruning already keeps
the *package* loader inside what is imported. The gap is entirely at module
level, which is exactly where the tools that report supply-chain surface look.

## What it costs to download

Each figure comes from a fresh module cache — `GOMODCACHE` pointed at an empty
directory — measured with `du -sk`:

| | consumer of `quark` v1.12.0 | split approximation |
|---|---:|---:|
| `go build ./...` | 136,644 KB | 116,688 KB |
| `go mod download` (no arguments) | 136,644 KB | — |
| `go mod download all` | 933,180 KB | 163,336 KB |

Every row but the last reproduced to the kilobyte across runs; two runs of
`go mod download all` on the published consumer differed by about 4 MB, so read
that cell as ~911 MiB rather than an exact count. The zip totals further down
are exact.

Two things to read off this table.

First, `go build` and `go mod download` cost the same, and neither fetches the
117 modules the binary does not link: with a pruned module graph, a plain build
already ignores them. The 19,956 KB by which the split column is *lower* on the
build row is Quark's own source, which the approximation consumes through a
local `replace` instead of downloading — corrected for that, the two build
rows are identical to the kilobyte, and the `go mod download all` row is
183,292 KB against 933,180 KB, a difference of about 730 MiB.

Second, `go mod download all` is not an exotic command. It is what a
`go mod tidy`, a vendoring step, a cold CI cache primed for the whole graph, or
any tool that walks `go list -m all` ends up paying — about 911 MiB against
the 133 MiB the build itself needs, on the same consumer, today.

The same figure per module zip, from `go mod download -json all` and the size
of each reported `Zip` file:

| | bytes | share |
|---|---:|---:|
| all 128 modules | 176,577,468 | 100% |
| the 11 the binary links | 26,846,168 | 15% |
| the 117 it never links | 149,731,300 | **85%** |

Two modules account for more than half of the never-linked bytes on their own:
`modernc.org/sqlite` (49.8 MiB) and `github.com/klauspost/compress` (37.4 MiB,
reached through testcontainers).

## What a vulnerability scanner sees

This is the part with a consequence beyond disk, and the honest answer today
is: **no advisory reaches this consumer from a module Quark requires but the
consumer never links.**

`govulncheck ./...` on the consumer reports 37 advisories: 3 the code actually
calls, 7 in packages it imports without calling, and 27 in modules it requires
without importing. Every one of the 37 is attributed either to the standard
library (33 of them, against the toolchain govulncheck itself reports —
go1.26.0 — not to anything in Quark's graph) or to `golang.org/x/crypto` (4),
which this consumer *does* link. The split approximation returns byte for byte
the same advisory set.

The 117 never-linked modules were also scanned directly, by writing them into
a module of their own at the versions the build list selects and running
`govulncheck -scan module`: 33 advisories, all of them the same standard-library
ones, none attributed to any of the 117 modules.

So the finding is not "the drivers are shipping a vulnerability". It is that
those 117 modules are 117 chances for one, on a surface no binary uses:

- `govulncheck` in its default source mode is not fooled — reachability
  analysis is what keeps the 27 "in modules you require" out of the affected
  count. Tools that read the module graph instead of the call graph have no
  such filter, and `-scan module` is precisely the mode CI pipelines pick when
  they want a fast answer.
- An advisory in `testcontainers-go` or in one of the engine drivers today
  would land in the dependency dashboard of every application that imports
  Quark, and each of those teams would have to establish by hand what this page
  establishes once: their binary does not contain it.
- The version Quark requires is the *floor* for every consumer's build list.
  Raising `modernc.org/sqlite` here to answer an advisory raises it for
  applications that never call it.

## The split approximation, and what it cannot tell you

The right-hand column is not a real split. It is the `v1.12.0` tree with
everything that pulls an engine driver removed, tidied, and consumed through a
local `replace`:

```bash
git archive v1.12.0 | tar -x -C /tmp/quark-split
cd /tmp/quark-split
rm -rf cmd examples/superapp internal/driverclassify
find . -name '*_test.go' -delete
GOWORK=off GOFLAGS=-mod=mod go mod tidy
```

That leaves a root `go.mod` with 19 requirements (6 direct) and not one driver
or testcontainers module.

Three things this approximation cannot tell you:

1. **It does not isolate `cmd/quark`.** The drivers have three non-test
   dependents in the root module — the CLI, `examples/superapp`, and
   `internal/driverclassify`, which holds the classifier bodies the CLI and the
   test binary share — and the container modules come from the test files. The
   column measures the ceiling of moving all of them out together. Measured the
   same way, each half on its own falls well short:

   | tree | build list | driver requires | testcontainers requires |
   |---|---:|---:|---:|
   | as published | 128 | 6 | 5 |
   | without `cmd/` and `examples/superapp` | 106 | 6 | 5 |
   | without the `*_test.go` files | 77 | 5 | 0 |
   | without either | 28 | 0 | 0 |

   Moving the CLI out and leaving the engine suites behind removes not one
   driver from the root `go.mod`: the test files still require them, and so
   does `internal/driverclassify`.
2. **Deleting every `*_test.go` overstates the reduction a little.** A real
   library module would keep its unit tests, and their dependencies with them.
3. **It says nothing about the published shape.** A real `cmd/quark` module
   would carry its own `go.mod` requiring the library and the five driver
   modules, its own tags, and its own place in the release machinery — none of
   which a local `replace` exercises. That cost, and the decision, are in
   [ADR-0024](adr/0024-cli-en-modulo-propio.md).
