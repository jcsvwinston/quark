# Release notes — v1.8.0

A minor release that closes the core and CLI findings of an end-to-end audit
of the library. Most of it is about queries and commands that were accepted
and then quietly did something other than what they said: a column name the
engine never had, a sort direction it ignored, a scaffolded project whose
first command failed. It also adds `NewWithDB`, which mounts Quark on a
`*sql.DB` the host application already owns.

### Added

- **Qualified column names under a join.** With a join in the query, filtering
  on a column whose name exists in both tables — the primary key included —
  could not be expressed: `SQLGuard` rejected the qualified `orders.id` as an
  invalid identifier, and the engine rejected the bare `id` as ambiguous.
  `Where`, `OrderBy`, `GroupBy`, `Select` and `Col` now accept the
  `table.column` form **when the query has joins**, validating each segment
  with the rules they always applied and quoting the two separately. Without
  joins nothing changes.

- **`WithStrictColumns()` — column names checked against the model.** The
  identifier guard is lexical, so `Where("agee", ">", 1)` passed it as
  well-formed; on SQLite an unknown identifier then degrades to a string
  literal, and the query returned every row of the table without reporting an
  error. This option validates each column against the registered model in
  `Where`, `WhereIn`, `WhereBetween`, `OrderBy`, `GroupBy`, `Select`, `Having`
  and the aggregates, failing with `ErrInvalidQuery` that names the columns
  the model does have. Joins, raw and AST fragments, and `SelectExpr` aliases
  are exempt. It is opt-in: the default stays as it was.

- **`WithoutAssociations()` — write only this entity's row.** `Update` saves
  the preloaded associations recursively, from the snapshot held in memory, so
  a change another writer made to one of those child rows between your read
  and your update is overwritten. There was no way to ask for a single-row
  update. There is now, on `Update` and on `Create` (`belongs_to` included),
  and when the recursive save does run `Update` logs a warning naming the
  associations it is about to rewrite. The default behaviour is unchanged.

- **`WhereInOf` and `DeleteBatchOf`.** `WhereIn` and `DeleteBatch` take
  `[]any`, so the ordinary case — a slice of ids returned by an earlier query
  — needed a conversion loop at every call site. These are package-level
  functions rather than methods because a Go method cannot introduce a second
  type parameter.

- **`NewWithDB(driver, db, opts…)` — Quark over a pool you already opened.**
  An application that already holds a `*sql.DB` can hand it to Quark instead
  of configuring a second DSN and a second pool. A borrowed handle stays the
  caller's: `Close` does not close it, and `WithOptions` derives clients over
  the same handle.

### Fixed

- **`OrderBy` ignored any direction it did not recognise.** Anything that was
  not `DESC` or `desc` — `"Desc"`, `"descending"`, a typo — was treated as
  ascending, silently. `ASC` and `DESC` now match case-insensitively, `""`
  still means ascending, and any other value is `ErrInvalidQuery`, the same
  contract the operator whitelist has always had.

- **`rel:"m2m"` was accepted by half the ORM.** Eager loading understood the
  short alias; `Migrate` and the recursive save did not, so the join table was
  never created and the mismatch only surfaced when a query hit the missing
  table. The schema parser now normalises the alias to `many_to_many` once, so
  every subsystem reads the same relation.

- **Preload queries were invisible to observability.** The batched `SELECT`s a
  preload issues went straight to the executor, reaching neither
  `QueryObserver` nor the slow-query log: the N+1 a preload is there to
  prevent was measurable, the preload replacing it was not. Those queries now
  emit a `QueryEvent` with operation `PRELOAD` through the same pipeline as
  the rest.

- **`quark init` outside a Go module scaffolded a project that could not
  run.** The generated runner imported a placeholder module path, and the
  closing message told you to run `go run ./cmd/<app> migrate up`, which
  failed immediately. `init` now walks up the tree for the module and, when
  there is none, writes a coherent `go.mod` (`--module` chooses the path); the
  steps it prints at the end are the ones that work.

- **`quark seed create` generated a seeder that nothing ever ran.** The
  generated file registered itself nowhere, so `seed run` had no seeders to
  find, and the function that would have registered it was not documented. The
  new `seed` package (symmetric to `migrate`) fixes the direction: generated
  files call `seed.Register` from an `init()`, so a blank import of the
  `seeders/` package — which `quark init` already wires — is enough.
  `commands.RegisterSeeder` remains as a shim.

- **CLI identifiers reached introspection SQL unvalidated.** The values of
  `--table`, `--model` and the positional argument were interpolated into
  statements such as `PRAGMA table_info(%s)` and `DESCRIBE …` as given. They
  now pass through `SQLGuard` in `inspect table`, `inspect sql`, `validate`
  and `model --from-table`, and a rejection wraps `ErrInvalidIdentifier`.

- **`migrate create --from-models` blamed the models for a build failure.** It
  answered "no model structs" whether the package had no tagged structs, could
  not be loaded for lack of a `go.mod`, or simply did not compile. The three
  are now distinct, and a compile error is reported with the compiler's own
  lines.

- **A missing database configuration did not say what was missing.** The
  error now names the configuration keys, the environment variables that set
  them, and `quark init`.

### Changed

- **The CLI stopped printing an INFO line on every command.** Each invocation
  logged `quark client initialized` before doing its work. The CLI now runs
  its logger at warning level; `--debug` restores the previous output.

- **Repository documentation.** The supported-versions table in `SECURITY.md`
  still listed v1.2.x and v1.1.x five minors after the fact; it now states the
  policy — the two most recent tagged minors — without pinning numbers that go
  stale, and a release check fails if a superseded version is marked supported
  again. `docs/ENGLISH_DOCS.md`, a standalone reference from the v0.x era that
  had long stopped tracking the API, is retired in favour of this site.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>
