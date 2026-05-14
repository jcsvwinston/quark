# Quark v0.7.0 — Release Notes

> **Date:** 2026-05-14
> **Status:** late-alpha. Not yet v1.0 production-ready.
> See [`docs/ANALISIS_MADUREZ.md`](ANALISIS_MADUREZ.md) for the honest gap analysis between the current state and a planned v1.0.

Minor release: per-column timezones. A single feature, closing the last
deferred type from Phase 1's Bloque B — `time.Time` columns can now
declare or inherit a timezone, with a UTC-always wire contract. No new
phase, no breaking changes, no migration guide. Fully additive: callers
that don't use the feature see no change from v0.6.

## What's in this release

### Per-column timezones (closes Phase 1 / Bloque B)

`time.Time` columns can now be timezone-aware through two opt-in knobs,
with precedence **column tag → client default → driver pass-through**:

- **`quark.WithDefaultTZ(loc *time.Location)`** — a Client-wide fallback
  for `time.Time` columns without their own tag.
- **`quark:"tz=Europe/Madrid"`** — a per-column override tag.

```go
client, _ := quark.New("pgx", dsn, quark.WithDefaultTZ(time.UTC))

type Event struct {
    ID        int64                     `db:"id" pk:"true"`
    CreatedAt time.Time                 `db:"created_at"`                          // client default
    LocalTime time.Time                 `db:"local_time" quark:"tz=Europe/Madrid"` // tag wins
    DeletedAt quark.Nullable[time.Time] `db:"deleted_at" quark:"tz=Europe/Madrid"`
}
```

The wire contract is **UTC-always**: when a column resolves to a
timezone, the `time.Time` is converted to UTC on the way to the driver
— every dialect stores the same instant — and to the configured location
in memory on scan. The tag affects only how the field reads in Go, never
what is persisted. It is honoured on `time.Time`, `*time.Time` and
`Nullable[time.Time]` fields, including when the model is loaded through
`Preload`.

An invalid IANA timezone name is rejected **fail-fast** by
`Client.RegisterModel` and `Client.Migrate` with the new
`ErrInvalidTimezone` sentinel — a typo breaks the app at startup, not on
the first query that touches the column.

A column with neither a tag nor a client default passes through to the
driver untouched (the historical v0.6 behaviour). The bind/scan hot
paths gate on an O(1) flag, so models and clients that don't use
timezones pay no overhead (ADR-0002 — no extra reflect in hot paths).

Design rationale and the decisions behind the hybrid strategy are in
[ADR-0010](https://github.com/jcsvwinston/quark/blob/main/docs/adr/0010-per-column-timezone-override.md).
([#63](https://github.com/jcsvwinston/quark/pull/63))

## Known limitations

- Quark is **late-alpha** (~v0.7). Not v1.0 production-ready. Phase 4
  (observability + cache stampede protection), Phase 5 (real RLS engine
  via `CREATE POLICY` + `SET LOCAL`) and Phase 6 (codegen) are not yet
  in scope.
- **Custom types via `RegisterTypeMapper` are not timezone-intercepted.**
  A user type that wraps `time.Time` with its own `Scanner` / `Valuer`
  owns its zone handling — the per-column timezone contract applies to
  `time.Time`, `*time.Time` and `Nullable[time.Time]` only.
- The carried-over v0.6 limitations are unchanged: Oracle has no CI
  coverage; MSSQL `JSON[T]` round-trip is broken pending the
  NVARCHAR(MAX) encoding fix; `OpAlterColumn` covers type changes only;
  SQLite `DropForeignKey` / `DropCheck` return `ErrUnsupportedFeature`.

## Upgrading

```bash
go get github.com/jcsvwinston/quark@v0.7.0
```

No source-code changes required — v0.7 is fully additive. The
`WithDefaultTZ` option and the `quark:"tz=..."` tag are opt-in; existing
`time.Time` columns keep their v0.6 pass-through behaviour until you
adopt one of the two knobs.

## Versioned docs

The page-versioned site for v0.7.0 is at `https://jcsvwinston.github.io/quark/docs/0.7.0/`.
