# Quark v0.11.0 — Release Notes

> **Date:** 2026-05-24
> **Status:** late-alpha. Not yet v1.0 production-ready.
> See [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) for the honest gap analysis between the current state and a planned v1.0.

The first cut of Phase 6 code generation, a reproducible benchmark
harness, and — importantly — the honest profiling finding that comes
with them. **No breaking changes.** Code generation is **opt-in**:
without it, every model uses the reflection path exactly as before, and
the public API (`quark.For[T]`) is identical either way.

## Added

### Opt-in code generation: `quark gen` (F6-1, F6-2, F6-3a)

A new `quark gen` subcommand of `cmd/quark` parses your model package
with `go/packages` + `go/types` (so the tool is `go install`-able and
`//go:generate`-friendly) and emits a `quark_gen.go` per package that
registers typed implementations with the runtime:

- **F6-1** — the registry contract: `reflect.Type`-keyed registries with
  a versioned generated-code contract (`//quark:gen vN`) and a model-hash
  drift check (`CheckGeneratedDrift`). Generated code from an
  incompatible contract version falls back to reflection.
- **F6-2** — generated typed row **scanners**: `List`/`First`/`Find` use
  the generated scanner when one is registered (the per-column timezone
  feature still uses reflection).
- **F6-3a** — a generated INSERT **binder** for single-integer-PK models:
  `Create` builds its columns and args without reflection.

The reflection path remains the permanent default (ADR-0002); generation
installs typed implementations behind the same API (ADR-0014). See the
[Code Generation guide](https://jcsvwinston.github.io/quark-docs).

### Reproducible benchmark harness (F6-8a)

A standalone `benchmarks/` module comparing Quark against a hand-written
`database/sql` baseline and GORM, with documented methodology. It
replaces the previous hand-recorded numbers and the estimated cross-ORM
table.

## Performance — read this before enabling codegen

Generating code **does not meaningfully speed Quark up today**, and we
say so plainly. Measured gains are small: scan codegen ~2–5%, the insert
binder ~1%.

Profiling (`benchmarks/PROFILING.md`) explains why: Quark's per-operation
CPU is dominated by the SQLite engine and `database/sql` — reflection
does not appear in the top CPU nodes. Quark's overhead versus raw
`database/sql` is allocation-driven and **architectural** (result
collection, the immutable-clone query builder, query-string building),
not reflective, so removing reflection from scan/bind recovers little.
This meets ADR-0002's own condition for re-evaluating whether code
generation should be pursued for speed. **Generate for correctness and
forward compatibility, not for a dramatic speedup** — and, in a future
release, for the compile-time column type-safety that does not depend on
the performance question.

## Notes

- The generated write path covers `Create` for single-integer-PK models;
  `Update`/`UpdateFields`, batch inserts, and composite/non-integer keys
  bind via reflection. Queries using the per-column timezone feature scan
  via reflection.
- Generated files carry a versioned contract; after changing a model,
  re-run `quark gen`. The runtime detects (and tolerates) stale or
  incompatible generated code by falling back to reflection.

[#99]: https://github.com/jcsvwinston/quark/pull/99
[#100]: https://github.com/jcsvwinston/quark/pull/100
[#101]: https://github.com/jcsvwinston/quark/pull/101
[#98]: https://github.com/jcsvwinston/quark/pull/98
[#102]: https://github.com/jcsvwinston/quark/pull/102
