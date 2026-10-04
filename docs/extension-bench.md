# Extension bench — what a third party can build on Quark today

This is the numerator of the A11 gate ("Extensibility and catalog"). The arc
promises three things on Quark's side — a published plugin contract, a driver
template with a conformance kit, and official integrations for chi, Echo,
Gin, gRPC and Nucleus — and this page measures where each of them starts.

**Measured on 2026-10-04 against v1.15.0, and kept current as the arc closes
its gaps: the numbers below are what the suite produced on its last run.** Run
it with:

```bash
go test ./internal/extbench/ -run TestExtensionBench -v
go test ./internal/extbench/ -run TestExtensionBenchSummary -v   # the totals
QUARK_BENCH_TABLE=1 go test ./internal/extbench/ -run TestExtensionBenchTable  # regenerates the table below
```

The bench is not prose. Every control is a Go probe in `internal/extbench/`.
A control about an extension point implements it from outside package quark
and drives it through the public API. A control about the driver contract
builds a driver module whose path is not this repository's, with no
workspace, and runs it — or asks the build graph what that module had to
import. A control about a race runs the registry under the race detector in a
child process. A control about an integration looks for a module that
compiles against the framework and runs its tests. `TestExtensionBench`
asserts the **recorded verdict** rather than success, so closing a gap turns
the suite red with "this one moved, update the verdict".

## What the measurement found that the arc did not know

1. **The query path is open to an outside dialect; the schema path was not.**
   SQLite's own dialect methods under another name passed every query step of
   the battery and failed every schema step (`DRV-04`): Migrate wrote a
   primary key the engine did not fill, a second Migrate failed, ApplyPlan
   refused even an added column. The migration code decided by
   `Dialect.Name()`, and ApplyPlan never asked the `SupportsTransactionalDDL()`
   the interface already requires. A driver template with a kit is not
   enough for A11: a third-party engine cannot migrate until the schema path
   asks its dialect instead of its name. A11 `Q2` does that in two halves; the
   first — Migrate, PlanMigration, Sync and the bookkeeping tables asking
   `quarkdriver.ColumnTyper`, `AutoIncrementer` and `IdempotentDDL` — leaves
   three of the nine schema steps diverging, all in ApplyPlan.
2. **The two observation points miss different things, and both miss schema
   work.** Under a recording driver, 27 statements reached the engine; the
   middleware saw 10 and the observer 11 (`CON-04`; 23 before Sync read its
   table through the dialect's introspector). DDL, introspection and
   savepoints reach neither; `CreateBatch` skips the observer; `client.Exec`
   and `client.RawQuery` skip the middleware. OpenTelemetry and Orbit's SQL
   feed are both middlewares.
3. **`RegisterDialect` raced** (`DRV-01`); the other four registries did not
   (`CON-08`). Closed in A11 `Q1`: the dialect registry is behind a
   `sync.RWMutex` like the other four, and `DRV-01` is present.
4. **The conformance kit never sees a dialect** (`DRV-05`), the engine suite
   is unreachable from outside the repository (`DRV-07`), and PostgreSQL is
   the one in-repo driver that does not run the kit (`DRV-06`).
5. **The frameworks guide points at examples that no longer exist.** Its chi,
   Echo and Gin sections describe "the example" that builds the router; the
   examples left the tree on 2026-09-12 and no module of the repository
   requires any of those frameworks (`INT-01`…`INT-03`).
6. **The API surface CI freezes records names, not signatures** (`CON-02`),
   and three conventions Quark calls on a caller's type — `TableName()`,
   `Validate(ctx)`, `SQLState()` — have no exported interface (`CON-07`).

## The verdicts

| verdict | meaning |
|---|---|
| **present** | the control exists, a third party can reach it, and its probe exercised it end to end |
| **partial** | a piece exists; the case records exactly what is missing |
| **absent** | nothing a third party can use — the probe measures the absence (a build that fails, a race the detector reports, a module graph with no module in it), never the lack of a grep hit |

## Why a bench of its own

`internal/enterprisebench` is the numerator of A8's gate, and the suite's
posture guard counts its controls and checks its page's figure. Adding A11's
controls there would move A8's published figure with work that is not A8's,
and the guard could no longer say which arc a number belongs to. The two
benches share their shape — `control{id, family, title, want, note, probe}`,
a test that asserts the recorded verdict, a summary, a generated table — and
nothing else.

## What this bench can and cannot see

Its in-process probes run on SQLite in the root module, so `go test ./...`
exercises them on every change. The probes that run the go command — the
race detector, the standalone driver module, the driver modules of this
repository, the type-checked API — are skipped under `-short` (the race lane)
and measured in the full lane; they need the module cache, or the network to
fill it. Nothing here certifies a dialect against a live engine other than
SQLite: that is `internal/enginesuite`'s job, and `DRV-07` records that a
driver outside this repository cannot reach it.

The external driver the bench builds is SQLite under another name, so it
needs no database server. That choice is what makes `DRV-04` sharp: with the
engine and the dialect methods held equal, any difference between the two
arms is behaviour keyed on the dialect's name.

## The result

**6 of 22 controls present. 7 partial. 9 absent.**

### contract — 4 present · 3 partial · 1 absent

| id | control | verdict | what is missing |
|---|---|---|---|
| `CON-01` | A published page declares, for every type a third party can implement and hand to Quark, whether it is an extension point and at what stability | **absent** | Measured on the type-checked API: 43 exported types of quark and quarkdriver can be implemented outside the package — 30 interfaces with no unexported method, 12 function types and one record of functions (quarkdriver.Classifier). No table on any page under website/docs carries a stability column, so none of the 43 is declared; the page that names the most of them in code spans (reference/api/observability.mdx) names 9. The census mixes invitations (Dialect, Middleware, CacheStore, EventBus, the hooks) with plumbing a caller is not meant to implement (Option, Scope, Executor, Expr, Result, Row): drawing that line is the contract's job, and nothing draws it today. |
| `CON-02` | The extension surface is frozen by a test that fails when a member a third party depends on changes | **partial** | Measured against acceptance/apisurface.json, the surface CI regenerates and diffs on every pull request: 79 of the 82 members a third-party implementation depends on are recorded by name — every method of every implementable interface, every function type — and quarkdriver.Classifier's three predicates are not recorded at all (the generator lists a struct's methods, not its fields). No entry carries a signature: each holds pkg, name and kind, so changing a parameter of Dialect.UpsertSQL leaves the file byte-identical and every check green. Adding or removing a method does move the file, which CI reports as stale until it is regenerated — a freeze of names, not of the contract. |
| `CON-03` | The eight model hooks (Before/After Create, Update, Delete, Find) fire on a model declared outside package quark, and a Before hook's error aborts the write | **present** | — |
| `CON-04` | Every statement Quark sends to the engine passes through the Middleware chain and reaches the QueryObserver | **partial** | Measured with a recording database/sql driver underneath Quark, on a battery of what an application does in its first week (migrate, CRUD, upsert, a batch insert, a transaction with a savepoint, client.Exec and client.RawQuery, PlanMigration, Sync): 27 statements reached the engine, 10 passed through the middleware and 11 reached the observer. Neither sees schema work — the CREATE TABLE of Migrate and Sync, the ALTER TABLE of Sync, the introspection queries PlanMigration and Sync run (sqlite_master and four PRAGMAs; since A11 Q2 Sync reads the table through the dialect's SchemaIntrospector, which is four statements more than the one PRAGMA it chose by the dialect's name) — nor the SAVEPOINT and ROLLBACK TO SAVEPOINT of a transaction. CreateBatch passes the middleware and never reaches the observer; client.Exec and client.RawQuery reach the observer and skip the middleware. Two middlewares do compose, the first registered outermost — the order the godoc's "applied in the order they are added" leaves to the reader. The OpenTelemetry package and Orbit's SQL bridge are both middlewares, so what the middleware does not see, neither a trace nor Orbit's feed shows. |
| `CON-05` | A third-party CacheStore serves cached reads and is invalidated by a write; a third-party EventBus hears committed writes only, after the commit | **present** | — |
| `CON-06` | A registered TypeMapper decides the column type Migrate writes for a Go type Quark does not know, and is handed the dialect's name | **present** | — |
| `CON-07` | Every method Quark calls on a caller's type has an exported interface to implement and assert against | **partial** | Measured: three conventions Quark honours have no exported interface in quark or quarkdriver — a model's TableName() string, a model's Validate(context.Context) error, and an error's SQLState() string, which classifies any driver's error by its PostgreSQL code. Each was exercised: the table took the name, the validation error aborted the insert, a 23505 classified as a unique violation and a 40P01 as a deadlock. None has a name a third party can write `var _ quark.X = (*T)(nil)` against; Quark asserts them against an interface in an internal package (internal/schema.TableNamer) and two anonymous ones (validator.go, db_errors.go). The hooks of CON-03 show the shape the other three lack. |
| `CON-08` | The global registries other than the dialect's — classifiers, listener factories, type mappers, generated scanners and binders — are race-free under the race detector | **present** | — |

### drivers — 2 present · 3 partial · 3 absent

| id | control | verdict | what is missing |
|---|---|---|---|
| `DRV-01` | RegisterDialect is safe to call while dialects are being resolved | **present** | Measured in a child process under -race: RegisterDialect writing while DetectDialect and DetectDialectByName read is clean since A11 Q1, which put the registry behind a sync.RWMutex — the same guard the other four registries already had (CON-08). Before it, the registry was a plain map and the detector reported a DATA RACE: a driver that registers from init() is serialised by Go, but a registration after start-up (a test registering its own dialect, a module loaded late) raced with quark.New, which calls DetectDialect. Removing the lock turns this control back to absent. |
| `DRV-02` | A driver module outside this repository registers its dialect and its classifier without importing package quark | **partial** | Measured with go list -deps on a driver module built standalone (GOWORK=off) as example.com/extdriver: the half that registers the classifier imports quarkdriver and not package quark. The half that registers the dialect cannot avoid it — RegisterDialect lives in package quark, and the Dialect interface names quark.LockOptions (in LockSuffix), so implementing it means importing the library: 208 packages against the classifier half's 163. The listener is already root-free (quarkdriver.ListenerFactory); the dialect is the one piece of a driver that is not. |
| `DRV-03` | A driver module outside this repository, built standalone against this tree, opens its engine by name, reads and writes, classifies its duplicate key and passes drivertest.Verify | **present** | The fixture creates its table by hand: what Migrate writes for an engine name Quark does not know is DRV-04's subject, and the kit it passes checks classifiers only (DRV-05). |
| `DRV-04` | A dialect from outside is a full participant: SQLite's own dialect methods under another name behave as SQLite's do | **partial** | Measured with a battery of 20 steps on two arms — the same engine and the same dialect methods, one named sqlite and one extlite. The 11 query steps agree (CRUD, upsert, batch, LIKE escaping, savepoints, the locking refusal, keyset pagination, classification): the query path follows the Dialect interface. Of the 9 schema steps, 6 agree since the first half of A11 Q2: Migrate, PlanMigration and Sync ask the dialect for its column types (quarkdriver.ColumnTyper), its auto-increment key (quarkdriver.AutoIncrementer), how it creates a table or an index only when it is missing (quarkdriver.IdempotentDDL) and, for Sync, the current columns (SchemaIntrospector), and Quark's bookkeeping tables are one template the dialect answers. Against v1.15.0 all 9 diverged: Migrate wrote BIGINT PRIMARY KEY, so the first insert left the key NULL; a second Migrate failed; PlanMigration proposed six changes against SQLite's own table; Sync failed; ApplyPlan refused even an added column. Three still diverge, all in ApplyPlan: it decides transactional DDL from the name and never calls Dialect.SupportsTransactionalDDL(), so a plan whose second op fails keeps its first op on extlite and is rolled back on sqlite; and it routes a column change and a new foreign key to SQLite's table rebuild by the name, so extlite gets ErrUnsupportedFeature and an error. ApplyPlan adds and drops a column on both arms, but on extlite through the resumable path, which writes a checkpoint table instead of rolling back — the step that fails half-way is the one that tells them apart. |
| `DRV-05` | The conformance kit checks a driver's dialect: placeholders, quoting, upsert, limit and savepoints | **absent** | Measured on the kit's type-checked API: no field of drivertest.Case and no function of drivertest takes a quark.Dialect, so a dialect that quotes identifiers unsafely or numbers its placeholders wrong passes the kit — it is never shown one. The kit checks the three classifier predicates and nothing else. When the kit gains a way to take a dialect, this probe stops and asks to be extended to run it against a dialect that is wrong on purpose. |
| `DRV-06` | Every driver module of this repository runs the conformance kit | **partial** | Measured by running each driver module's tests against this tree (a go.work, as CI's driver lane builds) and looking for the kit's subtests: mssql, mysql, oracle and sqlite run drivertest.Verify; postgres does not. Its module registers no classifier by design — PostgreSQL errors are classified through the SQLState() method every PostgreSQL driver exposes (CON-07) — and the kit has nothing else to check, so the engine the kit never sees is PostgreSQL. |
| `DRV-07` | A driver module outside this repository can run the engine conformance suite the in-repo engines run | **absent** | Measured: the suite is internal/enginesuite, whose only non-test file is doc.go — SharedSuite and the per-engine suites live in its 100 _test.go files, which no importer can reach — and the go command refuses a module outside the repository that imports an internal package of quark ("use of internal package … not allowed"). An external driver can prove its classifier (DRV-03) and nothing about the SQL its dialect writes. |
| `DRV-08` | A driver template module builds standalone (GOWORK=off) and passes the kit | **absent** | Measured over every go.mod of the repository: 11 modules — the library, the CLI, the five drivers, the acceptance harness, the benchmarks, the bug-bash harness and the engine suites — and no other module requires the library, so nothing is a template for a driver someone else writes. The nearest things are the five drivers, each one engine's module pinned to a published quark, and the fixture this bench builds for DRV-03, which lives in testdata. |

### integrations — 0 present · 1 partial · 5 absent

| id | control | verdict | what is missing |
|---|---|---|---|
| `INT-01` | chi: a module of the repository serves Quark behind a chi router, and its tests pass standalone | **absent** | Measured: no module of the repository requires github.com/go-chi/chi/v5, directly or indirectly. The frameworks guide shows 9 lines of chi code and points the reader at "the example" that builds the router in newRouter(client) — examples/ left the tree on 2026-09-12, so that example does not exist and the code compiles nowhere. |
| `INT-02` | Echo: a module of the repository serves Quark behind an Echo server, and its tests pass standalone | **absent** | Measured: no module of the repository requires github.com/labstack/echo/v4. The frameworks guide shows 22 lines of Echo code and points at "the example" that builds newServer(client); none exists, and the code compiles nowhere. |
| `INT-03` | Gin: a module of the repository serves Quark behind a Gin engine, and its tests pass standalone | **absent** | Measured: no module of the repository requires github.com/gin-gonic/gin. The frameworks guide shows 26 lines of Gin code and points at "the example" that builds newEngine(client); none exists, and the code compiles nowhere. |
| `INT-04` | gRPC: a module of the repository serves Quark behind a gRPC service, and its tests pass standalone | **absent** | Measured: no module of the repository requires google.golang.org/grpc directly — the acceptance harness and the engine suites carry it only as an indirect requirement, which no package of theirs imports — and the frameworks guide has no gRPC section. Of the five integrations it is the only one with no text to start from. |
| `INT-05` | Nucleus: a module of the repository serves Quark from a Nucleus module, and its tests pass standalone | **absent** | Measured: no module of the repository requires github.com/jcsvwinston/nucleus. The library does not depend on Nucleus by decision (QADR-0001, QADR-0006), so the fixture has to be a module of its own that requires both — the shape of the acceptance harness — or live on Nucleus's side. The guide's Nucleus section shows 6 lines and points at quark init --with nucleus, whose output nothing in this repository compiles (INT-06). |
| `INT-06` | quark init --with writes each official integration, and what it writes is compiled by a module of the repository | **partial** | Measured on the CLI's flag validation — the CLI is a module of its own (ADR-0024) the bench cannot import, so it reads the initWithTargets literal the validation consults, with go/parser: --with accepts nucleus and refuses chi, echo, gin and grpc. What --with nucleus writes is source for a framework no module of this repository requires, so nothing here compiles it. |
