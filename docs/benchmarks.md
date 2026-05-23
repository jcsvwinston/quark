# Quark ORM — Performance Benchmarks

> **This page is a pointer.** The benchmark numbers and methodology are
> public-facing and live in the Docusaurus site, so they are not duplicated
> here (see the docs-in-`website/` rule in `CLAUDE.md`).

- **Published results + methodology:**
  [`website/docs/reference/benchmarks.mdx`](../website/docs/reference/benchmarks.mdx)
- **The reproducible harness:** [`benchmarks/`](../benchmarks/README.md) — a
  standalone module with `go test -bench` functions comparing Quark, raw
  `database/sql`, and GORM on the same model, schema, and operations.

## Run it

```bash
cd benchmarks
go test -run=^$ -bench=. -benchmem ./...
```

## Status

This replaces the earlier hand-recorded numbers and the estimated cross-ORM
table (F6-8). The harness measures ORM/driver overhead in isolation using
in-memory SQLite; the gap between Quark's reflect path and the hand-written
`database/sql` floor is the baseline the code-generation work (F6-2/F6-3) is
measured against, per
[ADR-0002](adr/0002-reflect-default-codegen-fase-6.md). The codegen-tier
comparison (ent, sqlc) is tracked as F6-8b in [`TASKS.md`](../TASKS.md).
