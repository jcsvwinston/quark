# Release notes — v1.5.0

**The DX minor.** v1.5.0 executes the quark half of the 2026-08-16 DX audit
backlog (DX-1/5/6/7/8/9/10/18/19/20 plus the §4.A4/A5 exit-0 cases): the
schema gap is closed, tag typos die loudly, and the constructor stops
accepting garbage in silence. No API breaking changes; the deliberate
behaviour changes are called out below.

Docs (1.5.0 is the current version): <https://jcsvwinston.github.io/quark/docs/>

## Added

- **Domain DDL from your models — `quark migrate create <name>
  --from-models <dir> --dialect <d>` (DX-18).** The audit's number-one gap:
  three paths to a schema and none produced PostgreSQL-usable DDL. The new
  flag loads the model structs statically (go/packages), renders
  dialect-correct DDL through the SAME type mapping the runtime migrator
  uses (`internal/migrate.SQLTypeWithOpts`/`PKColumnSQL`), orders tables
  topologically, emits FOREIGN KEYs and indexes, and writes a Down that
  drops in reverse dependency order. Verified end-to-end against PostgreSQL:
  the migration applies clean and `quark validate` accepts the result.

- **Rich model vocabulary — `quark model generate` (DX-19).** The
  `name:type:modifier` grammar now speaks `nullable<T>`
  (`quark.Nullable[T]`), `array<T>`, `json<T>`, and `belongs_to<Model>`
  (emits the FK column plus the `rel:"belongs_to" join:"..."` pair) — the
  415 hand-written lines the audit measured are now generatable.

- **Automatic timestamps (DX-20).** `Create` fills zero
  `created_at`/`updated_at` and `Update` refreshes `updated_at`, honouring
  the promise `.quark.yml` (`features.timestamps: true`) already made. The
  18 hand-written timestamp hooks in the reference app can be deleted.

- **`quark init` writes the embedded runner (DX-10).** The CLI used to
  *dictate* the six-line runner every project needs (migrations register via
  `init()`); now `quark init` writes `cmd/<app>/main.go` with the blank
  imports and `commands.Main()`, plus the doc stubs.

- **`Example:` on 19 subcommands (DX-7).**

## Fixed

- **Struct-tag typos fail fast (DX-8).** Five plausible typos
  (`quark:"notnull"`, `db:"price,lenght=10"`, `db:"qty,size=abc"`,
  `column:"extra"`) used to survive `RegisterModel` and `Migrate` without a
  single warning — producing DDL with no NOT NULL, no UNIQUE and a missing
  column. They now die at registration with `ErrInvalidTag` and a
  did-you-mean hint (`notnull` → `not_null`). `pk:"True"` (any case) is
  accepted: `FindPKs` and the codegen registry now agree.

- **No-PK models get an actionable error (DX-9).** `Find`/`Create` on a
  model without a declared primary key now name the model and the fix
  (`tag a field with pk:"true" or name a column db:"id"`) instead of
  surfacing `sql: no rows in result set` three layers later.

- **`quark.New` rejects invalid options (A4).** A string, a number, or an
  uncalled option constructor passed into the `...any` variadic used to be
  silently discarded. New fails naming each offending position and type.

- **Unknown driver without `WithDialect` is an error (A5).**
  *Deliberate behaviour change.* The old behaviour logged
  `WARN could not auto-detect dialect, defaulting to generic` and silently
  used the PostgreSQL dialect — emitting SQL for the wrong engine from then
  on. `New` now returns `ErrDialectNotSupported` pointing at
  `quark.WithDialect(...)` / `RegisterDialect`. An explicit `WithDialect`
  keeps working exactly as before.

- **Static builds build (DX-1).** The mattn/go-sqlite3 error classifier
  lives behind a `//go:build cgo` tag with a no-op stub for `!cgo`, so
  `CGO_ENABLED=0` and cross-compilation work again; a `static-build` CI lane
  keeps it that way.

- **CLI limits without the scary WARN (DX-6).** The CLI builds its `Limits`
  from `DefaultLimits()`, so every invocation no longer logged the
  partial-literal `SafeMigrations=false` warning.

## Upgrade notes

Drop-in for `v1.x`, with two loud-by-design changes: models whose tags carry
previously-silent typos will now fail `RegisterModel`/`Migrate` (fix the tag
— the error names it), and `quark.New` against a driver name quark does not
know requires `quark.WithDialect(...)` (previously it silently assumed
PostgreSQL).
