# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Documentation

- **Phase 3 formally opened** ([ADR-0009](docs/adr/0009-migrations-introspection-diff-not-versioned-files.md))
  with the decomposition into F3-1..F3-7 in `TASKS.md`. Strategy:
  code-first + diff bidireccional (introspection-based diff against
  the live DB, not only versioned files). Phase 3 closes when the
  seven items land; that release becomes v0.6.0.

### Added

- **`Client.IntrospectSchema(ctx)` — neutral schema introspection
  (F3-2 core)**: returns the current database's schema as a
  dialect-neutral `Schema{Tables[]Table{Name, Columns[]Column}}`.
  Foundation for the F3-3 diff comparator. New optional
  `SchemaIntrospector` interface on Dialect (same opt-in pattern as
  `MigrationLocker`). Implementations land for
  **SQLite** (`sqlite_master` + `PRAGMA table_info`),
  **PostgreSQL** (`information_schema.tables` /
  `information_schema.columns` with `current_schema()` scoping +
  type-parameter reassembly for `varchar(N)` / `numeric(P,S)`),
  **MySQL / MariaDB** (`INFORMATION_SCHEMA.{TABLES,COLUMNS}` scoped
  to `DATABASE()`, using `COLUMN_TYPE` for the full parameterised
  type string), and
  **MSSQL** (`sys.tables` / `sys.columns` / `sys.types` /
  `sys.default_constraints` with type reassembly from
  `max_length`, `precision`, `scale`; nvarchar/nchar
  byte-to-char halving; `MAX` for `max_length = -1`).
  Oracle still returns `ErrUnsupportedFeature` until F3-2-oracle
  (deferred — no CI coverage until the `gvenzl/oracle-free`
  image issue resolves). Foreign keys and check constraints are
  deferred to F3-2-{fks, checks} — `Table` ships with column +
  index metadata for now.

- **Per-Client model registry (F3-7)**: closes Phase 3. Adds three
  methods on `*Client` for managing which models the Client is
  responsible for, with convenience wrappers for the F3-3/F3-5
  workflows:

  - `Client.RegisterModel(models ...any) error` — appends models
    to the per-Client registry. Validates every model up front
    (must be struct or `*struct`, no untyped nil) and refuses
    partial registration on failure. Safe for concurrent use.
  - `Client.RegisteredModels() []any` — returns a snapshot of
    registered models in registration order. Mutations to the
    returned slice don't affect the internal registry.
  - `Client.MigrateRegistered(ctx)` — convenience for
    `Migrate(ctx, c.RegisteredModels()...)`. No-op (returns nil)
    when nothing is registered.
  - `Client.PlanMigrationRegistered(ctx)` — convenience for
    `PlanMigration(ctx, c.RegisteredModels()...)`. Returns an
    empty `Plan` when nothing is registered.

  Intentionally additive — the global type-meta cache in
  `internal/schema` is unchanged because it's correct as global
  state (deterministic per `reflect.Type`). F3-7's per-Client
  registry is about "which models this Client manages", NOT about
  the meta-computation cache. Multi-tenant deployments with
  multiple Clients (per ADR-0007) can now each track their own
  model set without cross-contamination.

  Calling `RegisterModel` multiple times APPENDS — it does NOT
  dedupe. Documented and pinned by a test
  (`TestClient_RegisterModel_DoesNotDeduplicate`) so a future
  "smart dedup" doesn't silently change behaviour.

- **`Client.Backfill` — orchestrated table backfill with resume
  tokens (F3-6)**: the data-ops counterpart to F3-3..F3-5's schema
  story. Iterates a table by primary key in batches, invokes a
  user callback per batch with the PK list, and persists the
  highest PK seen in a `quark_backfill_state(name, last_pk,
  updated_at)` table keyed by spec name. A process kill / callback
  error / deliberate retry resumes at `WHERE pk > last_pk` rather
  than re-running the entire table.

  Idempotent on completion: a re-invocation with the same Name
  after all batches were processed finds nothing to do and
  returns nil immediately.

  API:

      type BackfillSpec struct {
          Name      string                                              // resume key
          Table     string                                              // source table
          PKColumn  string                                              // default "id"
          BatchSize int                                                 // default 1000
          Process   func(ctx context.Context, batchPKs []int64) error
      }
      func (c *Client) Backfill(ctx context.Context, spec BackfillSpec) error

  Why the callback receives PKs (not row contents): backfill SQL
  is rarely "SELECT * + transform"; it's "UPDATE ... WHERE id IN
  (...)" or "INSERT ... SELECT ... WHERE id IN (...)" where the
  user already knows the relevant columns. Passing PKs keeps the
  helper out of the way and avoids a generics-or-reflect API
  expansion.

  Limitations: integer PKs only (text PKs and composite PKs out
  of scope for F3-6-core); positive PKs assumed for the
  `last_pk=0` fresh-start case (negative-PK tables need pre-seeded
  state). Concurrency follows the same pattern as ApplyPlan's
  resumable path — wrap with `AcquireMigrationLock` if you need
  cross-process serialisation.

  Per-dialect catalog tables created via the same pattern as
  `quark_migration_state` (MSSQL sys.tables guard, Oracle
  swallows ORA-00955). Filtered out of `IntrospectSchema` by the
  existing `quark_*` exclusion so the state table doesn't surface
  in user plans.

- **`quarkmigrate` package — plan/verify/apply CLI workflow (F3-5)**:
  a thin library helper that turns a configured `quark.Client` plus
  a set of Go model values into a three-action CLI workflow
  designed to be embedded in a user-side `migrations/main.go`:

  - `plan`: print the plan, exit 0 (informational).
  - `verify`: print the plan, exit 1 if non-empty (CI gate use).
  - `apply`: print the plan, run it if non-empty, exit 0 on success.

  Operational error (PlanMigration / ApplyPlan failure, unknown
  action) is exit 2 across all three actions. Exit codes are
  exposed as constants `ExitSuccess` (0) / `ExitDriftDetected` (1)
  / `ExitError` (2) for callers that want to assert on them.

  `quarkmigrate.Run(ctx, action, client, models...)` is the public
  entry point; `RunWithOutput` is the test-friendly variant that
  takes explicit writers. `ParseAction(s)` accepts the literal
  strings `"plan"`, `"verify"`, `"apply"`, plus `""` (defaults to
  `plan`).

  Plan output is prefixed with the short Plan.Hash() so users can
  correlate runs against the `quark_migration_state` resumable
  table when running on MySQL / MariaDB / Oracle.

  Example wrapper in `examples/migrations/main.go` — a complete
  user-side `main.go` showing how to read DSN/dialect from env,
  pass models, and route exit codes. Adapt to a real project by
  swapping in the user's model package.

  Why a library and not a binary: Go has no runtime model
  registration (the binary would need to import the user's
  models package, which only their code can do). The thin
  wrapper pattern is the idiomatic answer — users own a tiny
  `main.go` that imports both quarkmigrate and their models.

- **Resumable `ApplyPlan` on non-transactional engines (F3-4-resumable)**:
  closes F3-4 entirely. On MySQL, MariaDB, and Oracle (where DDL
  implicitly commits and the F3-4-tx wrapper has no effect),
  `ApplyPlan` now records each successfully applied op in a
  `quark_migration_state` table keyed by `(plan_hash, op_index)`.
  A re-invocation against the same plan (same `Plan.Hash()`) skips
  ops that were already recorded.

  Workflow on a non-tx engine when something goes wrong mid-plan:

  1. `ApplyPlan` runs ops 0..N, op N+1 fails. Ops 0..N are
     implicitly committed; state table records each.
  2. User addresses the underlying problem (missing referenced
     table, unique constraint conflict, etc.).
  3. User calls `ApplyPlan` again with the same plan. Resume path
     reads the state, sees ops 0..N applied, starts from op N+1.
     No re-applying earlier ops — no duplicate-key, no idempotency
     guesswork.

  Drift detection: the `plan_hash` (SHA-256 of the concatenated
  `op.String()` outputs) means two plans differing in any way
  produce independent state. A user who edits their models between
  runs starts a fresh sequence — no false "resume from op 3"
  against a plan whose op 3 means something different.

  New `Plan.Hash() string` method exposes the hash for users who
  want to inspect it (e.g. log the plan ID in CI gates).

  Transactional engines (PG / MSSQL / SQLite) skip the resumable
  path entirely — rollback handles failure cleanly, no state
  table needed. The `quark_migration_state` table is filtered out
  of `IntrospectSchema` by the existing `quark_*` exclusion, so it
  doesn't surface in user plans.

- **Transactional `ApplyPlan` (F3-4-tx)**: on engines with
  transactional DDL — **PostgreSQL, MSSQL, SQLite** — `Client.ApplyPlan`
  now wraps the op loop in `BEGIN ... COMMIT`. A mid-plan failure
  rolls back the whole plan, leaving the schema in its pre-plan
  state. This is the safety net users should rely on when running
  migrations against production on these engines.

  **MySQL, MariaDB, Oracle**: DDL implicitly commits on every
  statement, so wrapping is pointless. ApplyPlan on these engines
  retains the original no-tx behaviour — a mid-plan failure leaves
  the schema partially applied. The eventual F3-4-resumable
  follow-up adds a `quark_migration_state` checkpoint table for
  these engines so a manual resume can pick up where the plan
  left off.

  Internal refactor: `Client.CreateIndex` and `Client.AddForeignKey`
  now wrap private `createIndexOn` / `addForeignKeyOn` helpers
  that take an `Executor`. Public API unchanged; the tx path
  routes its DDL through the underlying `*sql.Tx` while the public
  helpers continue to use `c.db`. All per-dialect drop / add /
  alter helpers in the executor follow the same pattern.

  Integration contract: new `ApplyPlan_TransactionalRollback`
  test in SharedSuite asserts the right behaviour per dialect
  (rollback erases the probe table on PG/MSSQL/SQLite; probe
  persists on MySQL/MariaDB because of implicit commits — the
  test pins both, so future improvements have a clear contract
  to flip).

- **Cross-dialect type + default normalisation (F3-3-types)**: the
  diff's `columnsEqual` now normalises both type strings AND
  default values before comparing, so the migrator's canonical forms
  compare equal to what each engine's catalog actually stores.

  Type normalisation (`normalizeType`):
  - Case-fold + trim.
  - PG alias `character varying` → `varchar` (PG's information_schema
    returns the SQL-standard form while the migrator emits the
    engine alias).
  - MySQL display-width strip (`int(11)` → `int`) for old MySQL 5.7 /
    mixed-version clusters.
  - `int` ≡ `integer` collapse. The migrator emits `INTEGER` (SQL
    standard); MySQL / MariaDB / MSSQL catalogs return `int`; PG
    catalog returns `integer`. Without this, an `int64` field on
    any of those engines produced a perpetual spurious
    `OpAlterColumn`.

  Default normalisation (`defaultsEqual`):
  - PG `nextval(...)` ≡ nil. PG SERIAL / IDENTITY columns expose
    their autoincrement sequence via the DEFAULT clause
    (`nextval('table_col_seq'::regclass)`); the Go-side desired
    Schema has `Default=nil` because models don't declare nextval
    as a default. Treating these as equal closes the loop for any
    PG model with an int PK. MySQL / MSSQL / SQLite use other
    mechanisms (EXTRA field, IDENTITY property, AUTOINCREMENT
    keyword) that don't produce a COLUMN_DEFAULT row, so they need
    no normalisation.

  Headline contract: **`PlanMigration` round-trip is now empty on
  all 5 motors** after `Migrate(model)`. Integration test
  `PlanMigration_RoundTripScopedToFixture` runs on PG / MySQL /
  MariaDB / MSSQL / SQLite via SharedSuite (scoped to its own
  fixture because the SharedSuite leaves unrelated tables behind
  that the diff legitimately wants to drop). The CLI plan command
  (F3-5) can now be built on this without producing noisy plans on
  production engines.

  Not yet normalised: PG `int8`/`int4`/`int2` ↔ `bigint`/`integer`/
  `smallint` (information_schema returns SQL-standard names so this
  never arises from introspection; only relevant for hand-constructed
  Schemas).

- **`Client.ApplyPlan(ctx, plan)` — Plan executor (F3-3-execute)**:
  walks the operations in a [Plan] in order and dispatches each to
  the appropriate per-dialect DDL. Closes the F3-3 trio: with
  `IntrospectSchema` + `Diff` + `PlanMigration` + `ApplyPlan`,
  users can now do the full round-trip (model → plan → apply →
  verify) without writing DDL by hand. Dispatch per op type:
  CreateTable rebuilds DDL from the neutral `Table` struct;
  DropTable / AddColumn / DropColumn / AlterColumn (type only)
  use the dialect helpers from F3-2; CreateIndex / AddForeignKey
  reuse the existing F2-era helpers; DropIndex / DropForeignKey /
  AddCheck / DropCheck have new per-dialect dispatch inline.

  Surface limitations documented:
  - **OpAlterColumn**: only emits DDL for type changes today.
    Nullable / Default deltas are no-ops (TODO F3-3-execute-alter).
  - **SQLite + DropForeignKey / DropCheck**: returns
    `ErrUnsupportedFeature` — SQLite has no `ALTER TABLE DROP
    CONSTRAINT`, the workaround is the 12-step table-rebuild
    procedure, which is its own follow-up (F3-3-execute-sqlite-
    rebuild).
  - **MySQL/MariaDB <8.0.16 / <10.2.1 + AddCheck**: same Error
    1146 path as F3-2-checks would surface; not specifically
    handled here since the catalog state would prevent the diff
    from emitting the AddCheck op in the first place.

  Not transactional in this PR — F3-4 (resumable migrations) adds
  the BEGIN/COMMIT wrapper. Today a mid-plan failure leaves the
  schema partially applied; the returned error carries the op
  index + the op's String() so the caller can identify the
  failure point.

- **`Client.PlanMigration(ctx, models...)` — models-to-plan
  pipeline (F3-3-plan)**: takes one or more Go model structs and
  returns a `Plan{Ops []Operation}` describing what the database
  would need to change to align with the models. The pipeline is
  models → desired Schema (reflect on the cached ModelMeta /
  FieldMeta, reusing the migrator's `SQLTypeWithOpts` for type
  strings) → `IntrospectSchema` for the current state →
  `Diff(desired, current)` → `Plan`. The Plan is **inert** — no
  side effects; F3-3-execute is the follow-up that adds Apply.
  `Plan.IsEmpty()` and `Plan.String()` make the result trivially
  consumable by health endpoints, CI checks, and the F3-5 CLI.

  Round-trip identity is the headline contract: after
  `Migrate(model)`, `PlanMigration(model)` returns an empty Plan
  on SQLite. The contract test is in `migrate_plan_test.go`.
  Cross-dialect type-string drift (PG `bigint` vs migrator
  `BIGINT`) is documented as a known gap with a normalisation
  follow-up planned; spurious OpAlterColumn ops on PG/MySQL/MSSQL
  are expected today.

  PlanMigration intentionally **copies** the index / FK / check
  surface from the current schema into the desired one before
  diffing, because struct tags don't yet declare schema-level
  objects beyond columns. That keeps the plan honest until
  F3-3-plan-indexes lets tags drive them.

- **SQLite introspector fix — PK columns now report Nullable=false**:
  the PRAGMA `notnull` field is 0 for `INTEGER PRIMARY KEY`
  columns even though they're implicitly NOT NULL in SQLite. The
  fix ORs in the PRAGMA's `pk` field so the introspector output
  is symmetric cross-dialect (PG/MySQL/MSSQL already report
  is_nullable=false for PKs via their catalog). Visible to F3-3-plan
  callers because without this fix the round-trip diff would emit
  a spurious `nullable true→false` alter on every PK column.

- **Pure-Go schema diff algorithm (F3-3-core)**: `Diff(desired,
  current Schema) []Operation` returns the ordered list of changes
  needed to bring `current` into alignment with `desired`. Operations
  are dialect-neutral sealed types (`OpCreateTable`, `OpDropTable`,
  `OpAddColumn`, `OpDropColumn`, `OpAlterColumn`, `OpCreateIndex`,
  `OpDropIndex`, `OpAddForeignKey`, `OpDropForeignKey`,
  `OpAddCheck`, `OpDropCheck`) — each carries the neutral shape
  needed to render DDL via the per-dialect helpers in F3-3-execute
  (follow-up PR). The diff is **pure and deterministic** (same
  input → same output, stable sort) and **conservatively-typed**
  (matches columns / indexes / checks by name; matches FKs by name
  or by composite `(columns, ref_table, ref_columns)` key when the
  catalog returned an empty name — the SQLite inline-FK case).

  Cross-dialect awareness baked into the equality functions:
  the MariaDB `RESTRICT` vs MySQL `NO ACTION` FK-action divergence
  (documented in `ForeignKey` godoc) is treated as semantically
  equivalent so no spurious DROP+ADD ops appear on every plan.
  SQLite's `Checks=nil` contract is respected: when either side
  has `Checks=nil` for a table, the check comparison is skipped
  rather than treating `nil` as "no checks" (which would emit
  DropCheck for every check on the other side).

  Op ordering follows dependency rules: CREATE TABLE first; per
  shared table, ADD COLUMN → ALTER COLUMN → DROP CHECK → DROP FK
  → DROP INDEX → DROP COLUMN → CREATE INDEX → ADD FK → ADD CHECK;
  DROP TABLE last. The full algorithm is documented on the [Diff]
  godoc. Index shape changes (columns or unique flag) are modelled
  as DROP+CREATE since no engine supports altering an index in
  place.

  Follow-up F3-3-plan PR will add `Client.PlanMigration(ctx, models...)`
  to drive this from Go-side model types.

- **CHECK constraint introspection on the 4 CI dialects (F3-2-checks)**:
  `Table.Checks` is now populated with `Check{Name, Expression}`.
  Per-dialect catalogs: **PostgreSQL** `pg_constraint` (contype='c')
  with `pg_get_constraintdef(oid, true)` for the canonical expression
  text (the leading `CHECK ` keyword is stripped so `Expression`
  carries the predicate only);
  **MySQL / MariaDB** `INFORMATION_SCHEMA.TABLE_CONSTRAINTS` joined
  with `INFORMATION_SCHEMA.CHECK_CONSTRAINTS` (MySQL 8.0.16+,
  MariaDB 10.2.1+). Older versions don't have the
  `CHECK_CONSTRAINTS` catalog at all — the query would return
  `Error 1146: Table … doesn't exist`. `mysqlListChecks` detects
  that specific error and degrades to an empty result, keeping
  `IntrospectSchema` usable on older engines (which never
  enforced CHECK anyway, so "empty" is semantically correct);
  **MSSQL** `sys.check_constraints` filtered by parent table
  `OBJECT_ID`. The expression is passed through raw per dialect
  (each engine has its own canonical form — `((age > 0))` on PG,
  `` (`age` > 0) `` on MariaDB, `([age]>(0))` on MSSQL); F3-3 handles
  expression equivalence at the AST level.
  **SQLite intentionally deferred**: SQLite has no catalog for CHECK
  constraints, the only path is parsing `sqlite_master.sql` DDL —
  brittle and out of scope for the catalog-reader layer.
  `Schema.Tables[i].Checks` is `nil` on SQLite (intentionally not
  populated, NOT "no CHECK constraints"); a future
  `F3-2-checks-sqlite` follow-up could add DDL parsing if user
  demand justifies it.

- **Foreign-key introspection across the 4 CI dialects + SQLite
  (F3-2-fks)**: `Table.ForeignKeys` is now populated with
  `ForeignKey{Name, Columns, RefTable, RefColumns, OnDelete, OnUpdate}`.
  Per-dialect catalogs: **SQLite** `PRAGMA foreign_key_list`
  (groups rows by synthetic `id`; constraint Name comes back `""`
  since the PRAGMA doesn't preserve names — the diff layer matches
  on column-tuple instead);
  **PostgreSQL** `pg_constraint` (contype='f') with
  `unnest(conkey/confkey) WITH ORDINALITY` for stable composite-FK
  column matching; `confdeltype`/`confupdtype` single-char codes
  translated to verbose form;
  **MySQL / MariaDB** `INFORMATION_SCHEMA.KEY_COLUMN_USAGE`
  joined with `REFERENTIAL_CONSTRAINTS` (UPDATE_RULE / DELETE_RULE
  passthrough);
  **MSSQL** `sys.foreign_keys` joined with `sys.foreign_key_columns`
  / `sys.tables` / `sys.columns` ×2; underscored
  `*_referential_action_desc` strings (`NO_ACTION`, `SET_NULL`,
  `SET_DEFAULT`) normalised to SQL-standard spaces.
  All dialects emit `OnDelete`/`OnUpdate` as the SQL-standard
  verbose form (`CASCADE`, `SET NULL`, `SET DEFAULT`, `RESTRICT`,
  `NO ACTION`).

- **Index introspection across the 4 CI dialects + SQLite
  (F3-2-indexes)**: `Table.Indexes` is now populated with
  non-primary-key indexes (`Index{Name, Columns, Unique}`).
  Per-dialect catalogs: **SQLite** `PRAGMA index_list` /
  `PRAGMA index_info` (origin=`pk` filtered);
  **PostgreSQL** `pg_index` / `pg_class` / `pg_attribute` with
  `unnest(indkey) WITH ORDINALITY` for stable column order
  (filter `NOT indisprimary`);
  **MySQL / MariaDB** `INFORMATION_SCHEMA.STATISTICS` grouped
  by `INDEX_NAME` ordered by `SEQ_IN_INDEX` (filter `INDEX_NAME
  != 'PRIMARY'`);
  **MSSQL** `sys.indexes` / `sys.index_columns` / `sys.columns`
  with `is_primary_key = 0 AND type > 0` and
  `is_included_column = 0` to exclude INCLUDE columns.
  Expression / functional indexes surface their expression
  slot as `""` — the diff layer (F3-3) decides whether to
  treat them as opaque.

- **`Client.AcquireMigrationLock(ctx, name, timeout)` — distributed
  migration lock (F3-1)**: cluster-wide advisory lock for migration
  operations. First caller wins; subsequent callers block up to
  `timeout` or receive `ErrLockTimeout`. The lock is held by a
  dedicated connection for its lifetime; `Release` returns it to the
  pool. New optional `MigrationLocker` interface on Dialect — kept
  optional so custom dialects don't break.
  Per-dialect implementation: PG uses session-level
  `pg_advisory_lock(hashtext)` + `SET lock_timeout` (SQLSTATE
  `55P03` → `ErrLockTimeout`); MySQL/MariaDB use `GET_LOCK` +
  `RELEASE_LOCK` (return 0 → `ErrLockTimeout`); MSSQL uses
  `sp_getapplock @LockOwner='Session'` (status -1 →
  `ErrLockTimeout`). SQLite and Oracle return
  `ErrUnsupportedFeature` — SQLite has no distributed primitive,
  Oracle's `DBMS_LOCK` needs PL/SQL plumbing tracked as F3-1
  follow-up. First F3 deliverable closed.

- **`Array[T]` generic** — typed wrapper for SQL columns holding a list of `T`.
  Round-trips through JSON regardless of dialect (same wire format as
  `JSON[T]`; migrate maps to the per-dialect JSON column type). Helpers
  `Len()` / `Slice()` over the underlying `[]T`. Semantically clearer than
  `JSON[[]T]` for list-shaped columns and gives the project a single
  upgrade path if PG-native `INT[]` / `TEXT[]` support lands later.
  Intentionally **not** tied to `pgx`/`pgtype` — neutral-wrapper design
  per TASKS § Bloque B. Inherits the MSSQL JSON Scan skip until the
  NVARCHAR(MAX) encoding bug (F0-8 followup E) is resolved.

## [0.5.0] - 2026-05-13

Phase 0 cleanup release. No new public API — every change is
infrastructure or test-side. Closes the F0-1 through F0-10 backlog
that had been carried since the project's first audit, including the
integration matrix that finally enforces the "tests pass on 6 engines
before merge" rule that was honor-system through v0.4. Full release
notes in [`docs/RELEASE_NOTES_v0.5.0.md`](docs/RELEASE_NOTES_v0.5.0.md).

### Added

- **Integration test matrix via testcontainers-go (F0-8)** — per-engine
  helpers in `containers_test.go` (gated `//go:build integration`) boot
  PostgreSQL, MySQL, MariaDB, MSSQL, and Oracle through testcontainers
  and resolve a DSN with the precedence env var → container. Each
  suite file delegates DSN resolution to `resolve<Engine>DSN(t)`
  instead of reading `os.Getenv` directly. Default
  (`go test -short`) path stays SQLite-only and doesn't import
  testcontainers-go. CI gains an `integration` job with a 4-engine
  matrix (PG / MySQL / MariaDB / MSSQL — Oracle excluded pending the
  image issue; the helper stays for local use) that runs in parallel
  to Lint + SQLite jobs. Docker is pre-installed on `ubuntu-latest`.
  Closes the honor-system state of the "6 motores verdes antes de
  mergear" hard rule — now enforced on 4/5 engines via CI. ([#28],
  [#36])
- **release-please workflow (F0-9)** —
  `.github/workflows/release-please.yml` runs on every push to `main`
  and keeps a rolling Release PR open with the next semver bump
  derived from Conventional Commits and the CHANGELOG entries since
  the last tag. Does NOT automate the Docusaurus `docs:version`
  snapshot — that stays manual via the `/release` slash command
  before merging the release PR. Config in
  `release-please-config.json` + manifest in
  `.release-please-manifest.json`. ([#38])
- **Docs linter (F0-10)** — `scripts/lint-docs.sh` runs in the
  `Lint` CI job. Three checks: anti-marketing language
  (`production-ready` / `enterprise-grade` / `battle-tested`
  rejected unless negated), `RELEASE_NOTES_V1` leak (the deleted
  file may not be referenced), and broken relative links in `*.md` /
  `*.mdx` (Docusaurus-aware: tries `<path>`, `<path>.md`,
  `<path>.mdx`, `<path>/index.{md,mdx}`, and resolves `/docs/...`
  baseUrl-rooted paths). Meta files (CLAUDE.md, TASKS, ADRs, blog,
  versioned_docs) exempt. ([#39])

### Fixed

- **MSSQL setop ORDER BY** — `List()` over a `Union` / `Intersect` /
  `Except` triggered MSSQL's "ORDER BY items must appear in the
  select list" because the auto-injected ORDER BY for OFFSET/FETCH
  referenced the PK column, which isn't in the operand SELECT. The
  fix is test-side: an explicit `OrderBy("email", "ASC")` on the
  base. The Quark SQL was always correct; the assertion was
  SQLite-biased. ([#35])
- **MSSQL JoinBuilder ambiguous id** — `List()` over a `Join`
  between two tables that both expose `id` triggered MSSQL's
  "Ambiguous column name 'id'" on the implicit `SELECT *`. Tests
  switched to `Count()`, which exercises the same ON-clause path
  without projection ambiguity. ([#30], [#35])
- **`having_aggregate` portable shape** — `SELECT * ... GROUP BY`
  is rejected by Postgres / MySQL strict / MSSQL when non-grouped
  columns aren't aggregated. Tests now use explicit
  `Select("status")` to match the GROUP BY clause. ([#30])
- **Float precision in nullable roundtrip** — Postgres maps Go
  `float64` to SQL `real` (32-bit) by default, so the 98.6 fixture
  round-trips to 98.5999984741211. Test switched to a
  `math.Abs(diff) > 1e-4` tolerance. ([#32])
- **Outdated `quark.New(db, ...)` examples on the docs site** —
  the verbose form never existed in the public API. All snippets
  migrated to the real `quark.New(driver, dsn, opts...)` signature
  across `website/docs/`. ([#27])

### Changed

- **CI matrix is now blocking on PG / MySQL / MariaDB / MSSQL** —
  `continue-on-error: true` removed after the F0-8 follow-ups
  closed the 11 test-side bugs the first cross-engine run
  surfaced. A red light on any of those 4 engines now fails the
  PR. Oracle remains excluded until the `gvenzl/oracle-free` image
  issue on hosted runners is resolved. ([#36])

### Documentation

- README cosmetic cleanup (F0-1 through F0-5): outdated
  `examples/blog-api/` references removed; `pkg/quark/examples/`
  heritage paths in `examples/README.md` fixed; duplicate Quick
  Start section deduplicated; coverage badge no longer hardcoded;
  versioned `RELEASE_NOTES_V1.md` no longer referenced. ([#37])
- TASKS header reconciled with the actual state of Phase 0
  (F0-1..F0-10 fully closed, not just the P0 subset). ([#40])

### Tests

- Dialect-aware quote assertions in `expr_ast` / `cte` / `window`
  integration tests via new `q(client, ident)` helper — replaces
  hardcoded `"col"` literals that match SQLite/Postgres quoting
  but not MySQL / MariaDB / MSSQL. ([#29])
- Dialect-skip + mirror-contract assertions in setop tests for
  MySQL / MariaDB where `Intersect` / `Except` return
  `ErrUnsupportedFeature` by design. ([#31])
- Interim skip of `JSON[T]` roundtrip on MSSQL with diagnosis —
  NVARCHAR(MAX) encoding bug; the fix (migrate to
  `VARCHAR(MAX)`) is deferred to a future PR with MSSQL local
  access. ([#33])

[#27]: https://github.com/jcsvwinston/quark/pull/27
[#28]: https://github.com/jcsvwinston/quark/pull/28
[#29]: https://github.com/jcsvwinston/quark/pull/29
[#30]: https://github.com/jcsvwinston/quark/pull/30
[#31]: https://github.com/jcsvwinston/quark/pull/31
[#32]: https://github.com/jcsvwinston/quark/pull/32
[#33]: https://github.com/jcsvwinston/quark/pull/33
[#35]: https://github.com/jcsvwinston/quark/pull/35
[#36]: https://github.com/jcsvwinston/quark/pull/36
[#37]: https://github.com/jcsvwinston/quark/pull/37
[#38]: https://github.com/jcsvwinston/quark/pull/38
[#39]: https://github.com/jcsvwinston/quark/pull/39
[#40]: https://github.com/jcsvwinston/quark/pull/40

## [0.4.0] - 2026-05-10

Phase 2 release: composable query builder. Introduces a typed expression
AST and the structured query primitives (subqueries, CTEs, window
functions, set operators) that build on it, plus a structured Join
builder that retires the v0.3.x string-raw deprecation. Full release
notes in [`docs/RELEASE_NOTES_v0.4.0.md`](docs/RELEASE_NOTES_v0.4.0.md);
breaking-change migration in
[`docs/MIGRATION_v0.4.0.md`](docs/MIGRATION_v0.4.0.md).

### Changed (BREAKING)

- **`Join` / `LeftJoin` / `RightJoin` now return a `*JoinBuilder[T]`**:
  the v0.3.x string-raw form `q.Join(table, onClause)` is replaced by
  the structured `q.Join(table).On(left, op, right)` (or
  `.OnRaw(onClause)` for compound ON clauses that need the legacy
  free-form). Both new methods route through the same
  `guard.ValidateJoinOn` grammar the old form used, so the validation
  surface is identical — only the call shape changed. See
  [`docs/MIGRATION_v0.4.0.md`](docs/MIGRATION_v0.4.0.md) for the
  mechanical rewrite (a `gofmt -r` rule covers it). Closes the v0.2
  deprecation notice.

### Added

- **Set operators via `Union` / `UnionAll` / `Intersect` / `Except`
  (Phase 2)**: any `Query[T]` can be combined with another `Query[T]`
  through the standard SQL compound-select form. Renders flat (no
  parens around operands) — `SELECT ... UNION ALL SELECT ...` — which
  is the only shape SQLite accepts and is portable across PG, MySQL,
  MariaDB, MSSQL, Oracle, SQLite. Dialect-keyword translation lives
  in a package-level `setOpKeyword` helper (kept out of the Dialect
  interface to avoid breaking custom implementations downstream):
  Oracle maps `EXCEPT` to `MINUS`; MySQL/MariaDB return
  `ErrUnsupportedFeature` for `INTERSECT`/`EXCEPT`; SQLite rejects
  `INTERSECT ALL`/`EXCEPT ALL`. Operand restrictions enforced at
  attach time (each surfaces as `ErrUnsupportedFeature`):
  - Operand cannot have `ORDER BY`, `LIMIT`, `OFFSET`, lock options,
    its own CTEs, or nested set-ops.
  - Base cannot have pessimistic locks (the dialect-specific lock
    suffix would bind to the combined result).
  Outer ORDER BY / LIMIT on the base apply to the combined result.

- **Window functions via `SelectExpr` + `Over` / `Window` / `RowNumber` /
  `Rank` / `DenseRank` / `Lag` / `Lead` (Phase 2)**: a typed surface for
  windowed projections that fits inside the AST. `Window` is a
  partition / order specification (`NewWindow().PartitionBy(Col("status")).
  OrderBy(Col("amount"), true)`) — immutable, chain-style. `Over(inner,
  w)` wraps any AST Expr with the OVER clause; the dedicated leaves
  `RowNumber`, `Rank`, `DenseRank`, `Lag(col, offset)`, and `Lead(col,
  offset)` cover the most-used window functions and bypass the function
  whitelist (their syntax is restricted to OVER (...) contexts the
  whitelist doesn't model). The Lag/Lead offset is bound as a parameter,
  not interpolated, so the bind path stays uniform.

  The new `Query[T].SelectExpr(alias, e)` method projects an arbitrary
  AST expression into the SELECT list aliased as `alias` (validated
  through `SQLGuard.ValidateIdentifier`):
  ```go
  q := quark.For[Sale](ctx, c).
      Select("id", "region", "amount").
      SelectExpr("rk", quark.Over(quark.Rank(),
          quark.NewWindow().
              PartitionBy(quark.Col("region")).
              OrderBy(quark.Col("amount"), true)))
  // SELECT "id", "region", "amount",
  //        RANK() OVER (PARTITION BY "region" ORDER BY "amount" DESC) AS "rk"
  // FROM "sales"
  ```
  AST projections compose with regular `Select(cols...)` (comma-joined
  in order). Their bind args land in the args slice between any CTE
  args and the WHERE args, matching the SQL-surface order.

- **CTE support via `With` / `WithRecursive` (Phase 2)**: any
  `*Subquery` can be attached to an outer query as a named CTE. The
  outer SELECT is prefixed with `WITH "name" AS (<inner>)` (or
  `WITH RECURSIVE ...` if any attached entry is recursive), the inner
  args are substituted and prepended to the args slice, and the outer
  WHERE / HAVING argIndex shifts accordingly so dialect placeholders
  ($N / @pN / :N) line up across the CTE-prefix → outer-WHERE
  boundary. The outer query references the CTE by name in JOIN
  clauses (the existing JoinOn grammar already accepts the
  `cte_name.col = parent.col` shape).

  ```go
  topOrders, _ := quark.For[Order](ctx, c).
      Where("amount", ">", 100).
      Select("user_id").
      AsSubquery()

  users, _ := quark.For[User](ctx, c).
      With("top_orders", topOrders).
      Join("top_orders", "users.id = top_orders.user_id").
      List()
  // WITH "top_orders" AS (SELECT "user_id" FROM "orders" WHERE "amount" > $1)
  // SELECT * FROM "users" INNER JOIN "top_orders" ON ...
  ```

  CTE names go through `SQLGuard.ValidateIdentifier`. Recursive CTEs
  emit the dialect-portable `WITH RECURSIVE` keyword; the recursive
  body itself currently requires the user to express the
  `UNION ALL`-shape — full UNION / INTERSECT / EXCEPT support arrives
  in F2-set.

- **Subqueries via `AsSubquery` + `Sub` / `Exists` / `NotExists` /
  `InSub` / `NotInSub` (Phase 2)**: any `Query[T]` can be captured as a
  `*Subquery` and embedded in the AST. The capture eagerly renders the
  inner SELECT (identifier validation, soft-delete predicate, JOINs,
  GROUP BY, HAVING, ORDER BY, LIMIT, lock suffix) using the active
  dialect's identifier quoting but with `?` as the bind marker, so the
  outer query's `buildWhereClause` swaps each `?` for the dialect's
  placeholder syntax at the correct argIndex when the wrapping Expr is
  rendered. Supports the canonical shapes:
  ```go
  // WHERE "id" IN (SELECT "user_id" FROM "orders" WHERE "amount" > ?)
  q.WhereExpr(quark.InSub(quark.Col("id"), sub))
  // WHERE "id" = (SELECT MAX("user_id") FROM "orders")
  q.WhereExpr(quark.Eq(quark.Col("id"), quark.Sub(sub)))
  // WHERE EXISTS (SELECT 1 FROM "orders" WHERE ...)
  q.WhereExpr(quark.Exists(sub))
  ```
  Internally the renderer wraps the active dialect in a `qmarkDialect`
  that delegates everything except `Placeholder`, which always returns
  `?`. So Quote, LimitOffset, and JSONExtract stay dialect-correct.
  Errors during `AsSubquery` (invalid identifier in the inner SELECT,
  or any pessimistic-lock option set on the inner query) propagate to
  the caller; `MustAsSubquery` is the panic-on-error variant for use
  inside expression composition. Pessimistic locks on the inner query
  are rejected with `ErrUnsupportedFeature` because MSSQL emits
  `WITH (UPDLOCK)` inline in the FROM clause — illegal inside an
  `IN (SELECT ...)` context — and the safe pattern is to acquire locks
  on the outer query.

- **Composable expression AST + `WhereExpr` / `HavingExpr` (Phase 2)**: a
  typed expression tree (`Expr` interface, `Col`, `Lit`, `And`, `Or`,
  `Not`, `Cmp`, `Eq`/`Ne`/`Lt`/`Gt`/`Lte`/`Gte`, `In`, `NotIn`, `Func`)
  rendered into the existing where-clause pipeline through `WhereExpr`
  and `HavingExpr`. Identifiers go through `SQLGuard.ValidateIdentifier`
  at every leaf, operators through `SQLGuard.ValidateOperator`, and
  function names against a conservative 10-name whitelist (`COUNT`,
  `SUM`, `AVG`, `MIN`, `MAX`, `LOWER`, `UPPER`, `LENGTH`, `COALESCE`,
  `ABS`). The AST emits `?` as a neutral bind marker; the existing
  `substitutePathMarkers` helper swaps each `?` for the dialect's
  placeholder syntax at render time, so the same AST renders correctly
  against PostgreSQL `$N`, MSSQL `@pN`, Oracle `:N`, MySQL/SQLite `?`
  without per-dialect indexing arithmetic in user code. Closes the gap
  where deep `(A OR (B AND C))` predicates required `RawQuery`.

- **Nested Preload via dotted paths (Phase 2)**: `Preload("Orders.Items.Product")`
  now walks the dotted path and loads each level in a single eager-loading
  pass. Multiple paths sharing a prefix are merged via `parsePreloads` so
  `Preload("Posts", "Posts.Comments")` only loads `Posts` once. Internally
  the per-relation loaders moved from `Query[T]` to `BaseQuery` and now
  accept the parent slice as a `reflect.Value`, so the recursive descent
  doesn't need a generic instantiation per level.

- **`HavingAggregate(fn, column, op, value)` (Phase 2)**: structured way to
  write `HAVING COUNT(*) > 5` / `HAVING SUM(amount) >= 100` / `HAVING
  AVG(price) < ?` etc. without falling back to `RawQuery`. Closes the
  historical limitation where the existing `Having(column, op, value)`
  validated `column` through `SQLGuard.ValidateIdentifier` and therefore
  rejected anything containing parentheses (i.e. every aggregate). The
  function name is whitelisted (`COUNT`, `SUM`, `AVG`, `MIN`, `MAX`,
  case-insensitive); the column is validated through the guard, except
  for `*` which is only allowed with `COUNT`. The fully composable form
  `Having(Func("count", Col("*")), ">", 5)` arrives with the rest of the
  Phase 2 AST.

- **Pessimistic locking (Phase 2)**: `Query[T].ForUpdate()`, `ForShare()`,
  `SkipLocked()`, `NoWait()` modifiers. The dialect emits the right shape:
  `FOR UPDATE [SKIP LOCKED|NOWAIT]` / `FOR SHARE` for PG, MySQL, MariaDB,
  and Oracle (Oracle has no `FOR SHARE` and returns `ErrUnsupportedFeature`
  for it); MSSQL emits table hints (`WITH (UPDLOCK, ROWLOCK [, READPAST])`)
  in the FROM clause; SQLite returns `ErrUnsupportedFeature` for any
  non-zero lock option (use `BEGIN IMMEDIATE` in the transaction instead).
  New error sentinel `ErrUnsupportedFeature` for these dialect-feature
  gates.

- **`Dialect.LockSuffix(LockOptions) (tableHint, suffix string, err error)`**:
  new interface method consumed by `buildSelect` to attach pessimistic-lock
  fragments to the SELECT in the right placement per dialect. Custom
  dialects must implement it.

### Fixed

- **Eager-loading paths now chunk parent keys (Phase 2)**: `Preload` over a
  large parent set used to assemble a single `IN(...)` clause with one
  bind per parent — silently broken on Oracle (1000-IN cap) and at risk on
  SQL Server (~2100 bind ceiling). The three relation loaders
  (`loadStandardRelation`, `loadM2MRelation`, `loadPolymorphicRelation`)
  now chunk at 1000 keys per query and aggregate results across chunks
  via a new internal `chunkParentKeys` helper. Tenant predicates and
  polymorphic-type discriminators are re-applied per chunk so the
  invariant survives the iteration.

## [0.3.0] - 2026-05-10

First proper tag since `v0.1.1`. Bundles Phase 0 P0 fixes (security, correctness)
with the Phase 1 deliverables (rich types, dirty tracking, optimistic locking,
soft-delete scopes). Full release notes in
[`docs/RELEASE_NOTES_v0.3.0.md`](docs/RELEASE_NOTES_v0.3.0.md). Migration
steps for breaking changes in
[`docs/MIGRATION_v0.3.0.md`](docs/MIGRATION_v0.3.0.md).

### Added

- **`JSON[T any]` generic + `[]byte` BLOB mapping (Phase 1 F1-2)**:
  `quark.JSON[T]` is a typed wrapper that round-trips a Go value through a
  SQL JSON column via `encoding/json`. It implements
  [`sql.Scanner`](https://pkg.go.dev/database/sql#Scanner) and
  [`driver.Valuer`](https://pkg.go.dev/database/sql/driver#Valuer) directly,
  so the round-trip uses the standard library's plumbing — no extra reflect
  in Quark's hot paths. The migrate layer detects `JSON[T]` and emits the
  dialect-native column type:
  Postgres `JSONB`; MySQL/MariaDB `JSON`; SQLite `TEXT` (with `json_*`
  functions still available); SQL Server `NVARCHAR(MAX)`; Oracle `CLOB`.
  Pair with `Nullable[JSON[T]]` when you need to distinguish SQL NULL from
  an empty payload. The migrate layer also learned to map `[]byte` to the
  dialect-native binary column (`BYTEA` on Postgres, `VARBINARY(MAX)` on
  SQL Server, `BLOB` elsewhere) instead of the previous `TEXT` fallback.

- **`Nullable[T]` generic (Phase 1 F1-3)**: re-export of `database/sql.Null[T]`
  under a Quark-friendly name, plus the constructors `SomeOf(v)` /
  `NullOf[T]()`. Replaces the long-standing `*time.Time` / `sql.NullString`
  pointer-as-nullable hacks with `Nullable[time.Time]` / `Nullable[string]`
  while keeping the same Scanner+Valuer round-trip plumbing the standard
  library already provides. The migrate layer detects `Nullable[T]` and
  emits T's SQL type for the column, so a model that previously needed a
  custom mapper now Just Works (`Nullable[int64]` → BIGINT,
  `Nullable[time.Time]` → TIMESTAMP / DATETIME / DATETIME2 per dialect).

- **Soft-delete scopes `WithTrashed()` / `OnlyTrashed()` and `Restore` (Phase 1 F1-5)**:
  the existing automatic `deleted_at IS NULL` filter now has two named
  escape hatches: `WithTrashed()` returns both live and trashed rows
  (alias of `Unscoped`, kept for backward compatibility), and
  `OnlyTrashed()` flips the predicate to `deleted_at IS NOT NULL` so
  callers can list only the trash. Both modifiers propagate through
  `clone()`. New `Query[T].Restore(entity)` method clears `deleted_at`
  on the row identified by the entity's PK; the SQL includes
  `AND deleted_at IS NOT NULL` so a Restore on a live row is a 0-row
  no-op (no stealth NULL write). Tenant predicate from the loading
  query is preserved on `Restore`. The default scope, `Count`, and
  aggregates all consult a new centralised `softDeletePredicate`
  helper so the three call sites stay in lock-step.

- **Optimistic locking via `quark:"version"` (Phase 1 F1-6)**: tagging a numeric
  field with `quark:"version"` enables row-level optimistic-locking on
  `Update` / `UpdateFields` / `Tracked.Save`. Each successful update emits
  `SET ..., version = version + 1 WHERE pk = ? AND version = <loaded>` and
  bumps the entity's in-memory version. When the predicate doesn't match
  (another writer already advanced the column) the call returns the new
  sentinel `ErrStaleEntity` without writing. Pairs naturally with the
  Phase-1 dirty-tracking pipeline: a `Tracked.Save` after a no-op mutation
  is still a no-op (the version is not bumped on its own).

- **`ErrStaleEntity`** sentinel for optimistic-locking conflicts (F1-6).

- **`RegisterTypeMapper(reflect.Type, TypeMapper)` (Phase 1 F1-4)**: extensible
  Go-type → SQL-type mapping for `client.Migrate` and `client.Sync`. Custom
  types (decimal.Decimal, uuid.UUID, IP addresses, vector types, …) can plug
  their own DDL emission without forking Quark. Pointer types are stripped
  before registration so registering for `time.Duration` also covers
  `*time.Duration`. The migrate layer also accepts new sizing options on the
  db tag — `db:"name,size=512"`, `db:"price,precision=18,scale=4"` — that
  flow into `TypeOptions` and are propagated to mappers and to the built-in
  VARCHAR/DECIMAL emitters. As the canonical example, Quark now ships with
  `time.Duration` registered to `BIGINT` (or `NUMBER(19)` on Oracle) so
  `Duration` columns stop falling back to `TEXT`.

- **Dirty tracking ligero (Phase 1)**: new `Query[T].Track()` modifier returns
  a `*TrackedQuery[T]` whose `Find` / `First` / `List` yield `*Tracked[T]`
  wrappers carrying a column-value snapshot taken at load time. Calling
  `Tracked.Save(ctx)` emits an UPDATE that touches only the columns whose
  values actually differ from the snapshot — and writes them whether they
  are zero or non-zero. This is the permanent fix for the P0-4 zero-value
  trap: `tracked.Entity.Active = false; tracked.Save(ctx)` writes `false`
  to the database without the caller resorting to `UpdateFields` or
  `UpdateMap`. `Tracked.Changed()` exposes the changed column list for
  tests and observability. The snapshot lives on the wrapper, not in the
  Client, so there is no shared map to grow or evict; tenant predicates
  from the loading query are propagated to Save's WHERE clause; the
  primary-key column and the configured tenant column are never written
  even if the caller mutates them on the entity.

### Security

- **`JOIN ... ON` clause concatenated raw (P0-5)**: `Join`/`LeftJoin`/
  `RightJoin` accepted the `on` argument as an opaque string and emitted it
  verbatim into the SELECT/Count SQL with no validation — an inconsistency
  with `WHERE` (which already validated identifiers) and an injection vector
  if the `on` came from dynamic input. Fixed: `internal/guard.ValidateJoinOn`
  enforces the minimal grammar `[ident.]ident OP [ident.]ident
  ((AND|OR) [ident.]ident OP [ident.]ident)*` (operators
  `=`, `!=`, `<>`, `<`, `<=`, `>`, `>=`; max 512 chars). Both call sites
  (`buildSelect` and `Count`) now reject malformed clauses with the new
  sentinel `ErrInvalidJoin`. The string-raw signature is marked deprecated
  in godoc; the structured `Join(table).On(col, op, otherCol)` builder is
  scheduled for v0.4 (Phase 2 AST). Regression: `testJoinOnSecurity` wired
  into the shared suite — valid identifier joins, valid AND-joined clauses,
  8 injection vectors rejected, and a Count-path check.

### Added

- **`ErrInvalidJoin`** sentinel for malformed `Join`/`LeftJoin`/`RightJoin`
  ON clauses (P0-5).

- **`UpdateFields(entity, fields ...string)` API (P0-4 escape hatch)**:
  explicit partial-update method that writes only the named fields, bypassing
  the zero-value filter `Update(entity)` applies. Recommended path for
  writing `false`, `0`, `""`, or `nil` until dirty tracking lands in Phase 1.
  Refuses to overwrite the primary key, errors on unknown field names or an
  empty list. Hooks `BeforeUpdate`/`AfterUpdate` still run.

### Changed

- **`Update(entity)` logs a WARN when it skips zero-value fields**, listing
  the skipped column names. Lets users see the P0-4 trap instead of having
  values silently disappear. The behaviour itself does not change.

### Fixed

- **Silent error swallowing in `linkM2M` (P0-3)**: when Quark inserted into a
  many-to-many join table, every driver error was returned as `nil` under the
  comment `// Ignore duplicate key errors - already linked`. The intent was
  to keep re-linking idempotent for unique-key violations, but the
  implementation masked foreign-key violations, missing tables, broken
  connections, and any other failure as success. Fixed: only real unique-key
  violations (PG SQLSTATE 23505, MySQL 1062, MSSQL 2627/2601, Oracle ORA-00001,
  SQLite extended codes 2067/1555 — both mattn and modernc drivers) are now
  treated as idempotent; everything else is wrapped with `wrapDBError` and
  propagated. Added `testM2MLinkErrors` to the shared suite (idempotent
  re-link + missing-join-table propagation). No public API change.

### Security

- **`WhereJSON` SQL injection via path interpolation (P0-2)**: every dialect's
  `JSONExtract` was building the SQL with `fmt.Sprintf("'%s'", path)` (or the
  Postgres `->>'%s'` equivalent), so a path containing a single quote either
  broke the SQL or could be weaponised when the path came from user input.
  Fixed in two layers: (1) the path is now bound as a parameter in every
  dialect — Postgres uses `jsonb_extract_path_text(col, VARIADIC text)` with
  one bind per segment, the rest use `JSON_EXTRACT`/`JSON_VALUE(col, ?)` with
  the `$.path` form; (2) `internal/guard.ValidateJSONPath` enforces the
  grammar `^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$` (max 256
  chars) and is called from each dialect before the bind. Invalid paths now
  return `ErrInvalidJSONPath` (new sentinel) at execution time.
  **Breaking**: `Dialect.JSONExtract` signature changed from
  `(column, path string) string` to
  `(column, path string) (sql string, args []any, err error)`. Custom
  dialects registered via `RegisterDialect` must update.
  Regression test `testJSONPathSecurity` wired into the shared suite covers
  valid paths (asserts the path is in bind args, never in the SQL surface)
  and 8 injection vectors (quotes, semicolons, comments, leading `$`, dashes,
  whitespace, empty).

- **Tenant isolation leak in `Or()` under `RowLevelSecurity` (P0-1)**: an `Or(...)`
  group used to be built on a fresh `BaseQuery` that did not carry the active
  `tenantID` / `tenantCol`. Combined with SQL operator precedence
  (`A AND B OR C` parses as `(A AND B) OR C`), the OR branch escaped the outer
  `tenant_id = ?` predicate and could return rows from other tenants. Fixed by
  introducing an internal `BaseQuery.cloneForGroup()` helper that propagates
  isolation/context state to the callback's blank query and pre-injects the
  tenant predicate into the OR group, so the rendered SQL becomes
  `WHERE tenant_id=? AND ... OR (tenant_id=? AND ...)`. Added a regression
  test (`testOrRLSLeak`) wired into the shared multi-engine suite that fails
  before the fix and passes after, including a nested-`Or` variant.
  No public API change.

### Changed

- **`Dialect.JSONExtract` signature** is now
  `(column, path string) (sql string, args []any, err error)` (was
  `(column, path string) string`). Required to bind the path as a parameter
  for P0-2. Custom dialects registered via `RegisterDialect` must update.

## [0.1.1] - 2026-05-06

### Breaking Changes

- **Client Creation API**: Changed `quark.New()` signature from `New(db *sql.DB, opts ...Option)` to `New(driverName, dataSource string, opts ...any)`
  - The function now accepts a driver name and data source string instead of a `*sql.DB` instance
  - `sql.Open()` is now called internally by `New()`
  - Dialect is now auto-detected from the driver name, removing the need for explicit `WithDialect()` option
  - Connection pool options (`WithMaxOpenConns`, `WithMaxIdleConns`, `WithConnMaxLifetime`, `WithConnMaxIdleTime`) are now applied during client creation

- **Removed Options**: 
  - `WithDialect()` option is no longer needed as dialect is auto-detected from driver name
  - Passing `*sql.DB` directly to `New()` is no longer supported

### Added

- **New Client Method**: Added `WithOptions(opts ...any) (*Client, error)` method to `Client` for recreating clients with different options without exposing the underlying `*sql.DB`
- **Connection Pool Options**: Added pool configuration options:
  - `WithMaxOpenConns(maxOpenConns int)` - Sets maximum number of open connections
  - `WithMaxIdleConns(maxIdleConns int)` - Sets maximum number of idle connections
  - `WithConnMaxLifetime(d time.Duration)` - Sets maximum connection lifetime
  - `WithConnMaxIdleTime(d time.Duration)` - Sets maximum idle connection time

### Migration Guide

**Old API:**
```go
db, err := sql.Open("sqlite", ":memory:")
if err != nil {
    log.Fatal(err)
}
defer db.Close()

client, err := quark.New(db, quark.WithDialect(quark.SQLite()))
```

**New API:**
```go
client, err := quark.New("sqlite", ":memory:")
if err != nil {
    log.Fatal(err)
}
defer client.Close()
```

**Recreating client with different options:**

**Old API:**
```go
newClient, err := quark.New(client.Raw(), quark.WithLimits(newLimits))
```

**New API:**
```go
newClient, err := client.WithOptions(quark.WithLimits(newLimits))
```

### Supported Drivers for Auto-Detection

- `"sqlite"`, `"sqlite3"`, `"modernc"` → SQLite
- `"postgres"`, `"pgx"`, `"pgx/v5"`, `"pq"` → PostgreSQL
- `"mysql"` → MySQL
- `"mariadb"` → MariaDB
- `"mssql"`, `"sqlserver"`, `"azuresql"` → MSSQL
- `"oracle"`, `"godror"`, `"oci8"` → Oracle

## [0.1.0] - Previous Release

Initial release
