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
compiles against the framework, runs the tests of the packages that import
it, and asks whether one of them held the guide's section to that code.
`TestExtensionBench` asserts the **recorded verdict** rather than success, so
closing a gap turns the suite red with "this one moved, update the verdict".

## What the measurement found that the arc did not know

1. **The query path is open to an outside dialect; the schema path was not.**
   SQLite's own dialect methods under another name passed every query step of
   the battery and failed every schema step (`DRV-04`): Migrate wrote a
   primary key the engine did not fill, a second Migrate failed, ApplyPlan
   refused even an added column. The migration code decided by
   `Dialect.Name()`, and ApplyPlan never asked the `SupportsTransactionalDDL()`
   the interface already requires. A driver template with a kit is not
   enough for A11: a third-party engine cannot migrate until the schema path
   asks its dialect instead of its name. A11 `Q2` did that in two halves —
   Migrate, PlanMigration, Sync and the bookkeeping tables asking
   `quarkdriver.ColumnTyper`, `AutoIncrementer` and `IdempotentDDL`; then
   ApplyPlan asking `SupportsTransactionalDDL()`, `ColumnAlterer`,
   `ObjectDropper` and `TableRebuilder` — and `DRV-04` is present. Asking
   showed one more thing: Oracle's dialect answered `true` to
   `SupportsTransactionalDDL()`, which nothing had acted on.
2. **The two observation points miss different things, and both miss schema
   work.** Under a recording driver, 27 statements reached the engine; the
   middleware saw 10 and the observer 11 (`CON-04`; 23 before Sync read its
   table through the dialect's introspector). DDL, introspection and
   savepoints reach neither; `CreateBatch` skips the observer; `client.Exec`
   and `client.RawQuery` skip the middleware. OpenTelemetry and Orbit's SQL
   feed are both middlewares. *Closed in A11 Q7:* one execution seam
   composes the chain around the call that reaches the engine and reports
   the event, and everything that sends a statement goes through it. That
   includes the schema paths, and a dialect's introspector, `ColumnAlterer`
   and `TableRebuilder` through an `Executor` they are handed. It also
   includes savepoints, raw SQL, the migration lock, `Notify`, routines,
   audit rows, and the `migrate` and `quarktenant` packages. The middleware
   and the observer each see the 27 statements, in the engine's order and
   with the same kind. `QueryEvent.Kind` and `StatementKindFromContext` name
   the kind (query, exec, ddl, introspection, savepoint, raw), and the
   OpenTelemetry middleware puts it on spans and metrics as
   `quark.statement.kind`. A wider battery in the root package (67
   statements, ApplyPlan through SQLite's table rebuild and Backfill
   included) holds the same, with no middleware registered as well. A
   type-checked guard fails on code that sends a statement past the seam.
   Writing the seam turned up two more statements neither point saw. A
   `List` whose rows failed to scan reached the engine and reported nothing,
   and a failed read of `List` or `Cursor` was never reported; the seam now
   reports both. What stays outside is listed on the observability
   reference: transaction control, which the driver sends; what your code
   sends on `Client.Raw()` or on the `*sql.Tx` an `UpTx` receives; the
   `set_config` that `RowLevelSecurityNative` runs inside the statement it
   confines; and the `LISTEN` connection.
3. **`RegisterDialect` raced** (`DRV-01`); the other four registries did not
   (`CON-08`). Closed in A11 `Q1`: the dialect registry is behind a
   `sync.RWMutex` like the other four, and `DRV-01` is present.
4. **The conformance kit never saw a dialect** (`DRV-05`), the engine suite
   was unreachable from outside the repository (`DRV-07`), and PostgreSQL was
   the one in-repo driver that did not run the kit (`DRV-06`). *Closed in A11
   Q4:* `drivertest.VerifyDialect` takes a dialect and a live `*sql.DB` the
   driver's own test opens, and checks every method of `quarkdriver.Dialect`
   and every optional interface the dialect implements, on queries and on the
   schema path, by what the engine then holds or refuses; the bench runs it
   against six dialects wrong on purpose and each fails naming the method.
   `quarkdriver/drivertest/suite` is the engine suite in a package a driver
   imports — the 43 engine-generic subtests of the shared suite, moved out of
   `internal/enginesuite`, which now runs them through the same `suite.Run`
   (165 packages from outside against the library's 160: no container library,
   no driver). All five driver modules run the dialect kit; PostgreSQL's,
   MySQL's and MariaDB's against a server in CI's driver lane. Run against the
   six built-in dialects, the kit found two of them wrong: MariaDB dropped a
   `CHECK` with MySQL's `DROP CHECK` (Error 1064), and SQL Server accepted a
   shared lock that skips locked rows, which the engine refuses (error 650).
5. **The frameworks guide points at examples that no longer exist.** Its chi,
   Echo and Gin sections describe "the example" that builds the router; the
   examples left the tree on 2026-09-12 and no module of the repository
   requires any of those frameworks (`INT-01`…`INT-03`). *Closed in A11 Q8:*
   `internal/integrations` is a module of its own, never published, whose
   net/http, chi, Echo and Gin packages serve the same notes API on a
   `*quark.Client` and pass one HTTP battery against SQLite; each package's
   `TestGuideMatchesFixture` fails when a Go block of its section of the guide
   is not, line for line, its code, and the bench asks for that test by name.
   *A11 Q9* added gRPC (the same API as a service, codes mapped from Quark's
   errors by one interceptor, the guide's protobuf block held to the
   `.proto`) and Nucleus (a module wrapping the client, mounted on an
   application booted in-process): `INT-04`, `INT-05`. The fixtures module is
   the one module of the repository that requires Nucleus; the library still
   does not. Writing the Nucleus fixture found that Nucleus's `BindJSON`
   answered 400 to any JSON array (NU-107, fixed in nucleus#591): it
   validated what it decoded as a struct. *A11 Q10* made `quark init --with`
   write those packages for chi, Echo, Gin, gRPC and Nucleus, each beside
   `internal/notes` and, for the first four, a server main; before it `--with`
   knew Nucleus alone, and what it wrote — a module of its own shape, not the
   fixture's — was built by nothing (it did compile, against Nucleus v1.30.1,
   when this session first built it). The CLI now embeds byte-for-byte copies
   of the fixtures, which a CLI test compares with them, and `INT-06` runs the
   CLI test that writes a project per target and builds, vets and tests it
   against this tree. Building the gRPC scaffold found the one rewrite a
   string substitution gets wrong: the generated code embeds the fixture's
   `go_package` in a length-prefixed descriptor, and replacing the path in
   the text compiles and then panics when the protobuf runtime loads it. The
   CLI re-encodes the descriptor, and the build test reads it back through
   the protobuf runtime.
6. **The API surface CI freezes records names, not signatures** (`CON-02`),
   and three conventions Quark calls on a caller's type — `TableName()`,
   `Validate(ctx)`, `SQLState()` — have no exported interface (`CON-07`).
   *Closed in A11 Q6*, together with the page that was missing altogether
   (`CON-01`): every func, method and type of `acceptance/apisurface.json`
   carries a `sig`, so changing a parameter of `Dialect.UpsertSQL` moves the
   file and CI's freshness check prints the symbol with both signatures; the
   probe renders the 94 symbols an implementation depends on from the
   compiler's export data and finds each one in the file with that signature.
   `quark.TableNamer`, `quark.Validator` and `quarkdriver.SQLStater` name the
   three conventions, and Quark asserts against them. The contract page
   (`website/docs/reference/extension-contract.mdx`) holds one table with all
   52 implementable types, and drawing the line the census could not draw
   took a decision per type: 40 are extension points and 12 are plumbing
   (`Option`, `Expr`, `Event`, `Executor`, the four adapters Quark hands a
   migration lock, `IdentifierValidator`, the two function types `quark gen`
   writes and the deprecated `NewListenerFunc`); 43 are stable, 3 are for
   internal use, and the six schema-path interfaces of `Q2` are experimental
   — nothing outside the repository has implemented them yet. The promise
   those columns describe is published as a proposal pending the owner's
   decision, to be recorded in an ADR: until then the page says the v1
   promise of `upgrade.mdx` stands for every exported type. *Adopted on
   2026-10-06* ([ADR-0029](adr/0029-extension-points-do-not-grow-in-v1.md)):
   those columns are now the v1 compatibility policy, which the page and
   `upgrade.mdx` state — an extension point gains no method within v1, a
   plumbing interface may in a minor release — and `TestExtensionPointsFrozen`
   holds every extension point to its recorded shape. Writing the
   rows found two things the code did not say: `Client.Validate`'s godoc
   promised validation before `Update`, which does not validate (the
   modelling guide had it right); and an error that implements `SQLState()`
   is answered as PostgreSQL for unique violations and deadlocks without
   consulting the registered classifiers, so a driver for another engine
   must not implement it with codes of its own — now said on
   `quarkdriver.SQLStater` and on the page.
7. **A dialect could not be written without package quark** (`DRV-02`):
   `RegisterDialect` lived there and `Dialect.LockSuffix` named
   `quark.LockOptions`, so the half of a driver that writes SQL imported the
   library (208 packages against the classifier half's 163). *Closed in A11
   Q3* (ADR-0026): the dialect contract — `Dialect` and the types it names,
   the optional interfaces Quark asserts on a dialect, the schema model
   `SchemaIntrospector` answers in, `ErrUnsupportedFeature`, `ErrLockTimeout`
   and the registry — is declared in `quarkdriver`, and every name stays in
   package quark as an alias of the same type. The fixture driver's dialect
   is now its own, written against `quarkdriver` alone (72 packages; the
   whole driver 165), and a program written against v1.15.2's names compiles
   unchanged (`dialect_contract_v115_test.go`). The surface file records the
   type an alias names (`alias_of`), so the 36 members that left package
   quark read as a move, not as a removal.
8. **There was no driver to start from** (`DRV-08`): the twelve modules of
   the repository held the five drivers, each one engine's module pinned to a
   published quark, and the fixture this bench builds lived in testdata, where
   no build but the bench's reaches it. *Closed in A11 Q5:*
   `internal/drivertemplate` is a driver for SQLite through
   `modernc.org/sqlite`, registered under a name of its own, in a module whose
   path is not this repository's, written against `quarkdriver` alone — the
   probe checks on the build graph that its code reaches no package quark.
   Its tests run the classifier kit, both halves of the dialect kit and the
   engine suite's 43 subtests, and CI's driver-template lane runs them with no
   workspace. The guide "Writing a driver"
   (`website/docs/guides/writing-a-driver.mdx`) walks from an empty module to
   that driver, and every Go block on it is held to the template's code by the
   template's own test. It also says what the dialect contract cannot express
   yet: the branches on the names `mssql` and `oracle` in the insert, upsert
   and pagination paths, the `LIKE` escape clause chosen by name, referential
   actions and the unit of a length that reach the DDL unasked, and the
   features tied to PostgreSQL.

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
SQLite: the dialect kit and the engine suite it measures (`DRV-05`, `DRV-07`)
run against the other five in CI's driver and integration lanes.

The external driver the bench builds is SQLite under another name, so it
needs no database server; since A11 Q3 its dialect is its own, written
against `quarkdriver` alone, because that is what `DRV-02` asks of it.
`DRV-04` keeps a different arm on purpose — SQLite's own dialect methods
under another name, in process: with the engine and the dialect methods held
equal, any difference between the two arms is behaviour keyed on the
dialect's name.

The driver template `DRV-08` measures is the same engine under a third name
(`templite`), in a module of the repository rather than in testdata: the
bench builds and tests it where it lives, as CI does, and keeps the testdata
fixture for the controls that break a dialect on purpose (`DRV-05`).

## The result

**22 of 22 controls present. 0 partial. 0 absent.**

### contract — 8 present · 0 partial · 0 absent

| id | control | verdict | what is missing |
|---|---|---|---|
| `CON-01` | A published page declares, for every type a third party can implement and hand to Quark, whether it is an extension point and at what stability | **present** | Measured on the type-checked API: 53 exported types of quark and quarkdriver can be implemented outside the package — 40 interfaces with no unexported method, 12 function types and one record of functions (quarkdriver.Classifier). Since A11 Q6 website/docs/reference/extension-contract.mdx carries one table with a row for each: the type (by the package that declares it, its quark alias beside it), whether it is an extension point (yes/no), its stability (stable/experimental/internal-use) and how it reaches Quark. The probe finds the tables with a stability column that name a census type, requires exactly one, and checks it covers the census exactly: no type missing, none twice (an alias and its target are one type), no row naming something outside the census, no value outside the vocabulary. Before it no page declared any of the 49 types then in the census. Dropping a row, adding a dangling one, repeating a type under its alias or writing a fourth stability each turns this control back to partial, and so does a new implementable type without a row. The page draws the line the census could not, and since the owner's decision of 2026-10-06 (ADR-0029) that line is the v1 compatibility policy, which the page and upgrade.mdx state: an extension point gains no method within v1, a plumbing interface may in a minor release; TestExtensionPointsFrozen holds every extension point to its recorded shape. 41 extension points and 12 plumbing (Option, Expr, Event, IdentifierValidator, Executor, the four migration-lock adapters, the two codegen function types and the deprecated NewListenerFunc); 43 stable, 7 experimental (the schema-path interfaces of A11 Q2 and ReferentialActioner) and 3 internal-use (the codegen function types and NewListenerFunc). |
| `CON-02` | The extension surface is frozen by a test that fails when a member a third party depends on changes | **present** | Measured against acceptance/apisurface.json, the surface CI regenerates and diffs on every pull request. Since A11 Q6 every func, method and type in it carries a sig — parameter and result types without names, a struct's exported fields, a func type's signature, and for an alias of an internal type (quark.TypeMapper, quark.TableNamer) the whole shape — and the probe renders, from the compiler's export data and by the generator's rules, the sig of each of the 95 symbols that fix what a third-party implementation depends on (every method of every implementable interface, every function type, quarkdriver.Classifier's fields): all 95 are in the file with the compiler's signature. Before it each entry held pkg, name and kind, Classifier's predicates were not recorded at all, and changing a parameter of Dialect.UpsertSQL left the file byte-identical. Changing that parameter from int to int64 now moves seven lines (the interface's UpsertSQL and the six built-ins', which CI's freshness check prints with the symbol and both signatures) and turns this control to partial until the file is regenerated. |
| `CON-03` | The eight model hooks (Before/After Create, Update, Delete, Find) fire on a model declared outside package quark, and a Before hook's error aborts the write | **present** | — |
| `CON-04` | Every statement Quark sends to the engine passes through the Middleware chain and reaches the QueryObserver | **present** | Measured with a recording database/sql driver underneath Quark, on a battery of what an application does in its first week (migrate, CRUD, upsert, a batch insert, a transaction with a savepoint, client.Exec and client.RawQuery, PlanMigration, Sync): 27 statements reached the engine, and since A11 Q7 the middleware and the observer each see all 27, in the engine's order, with the same kind — 8 query, 2 exec, 3 ddl, 10 introspection, 2 savepoint, 2 raw. Before it the middleware saw 10 and the observer 11: neither saw schema work (the CREATE and ALTER TABLE of Migrate and Sync, the catalog reads of PlanMigration and Sync) nor the savepoints, CreateBatch skipped the observer, and client.Exec and client.RawQuery skipped the middleware. One execution seam now composes the chain around the call that reaches the engine and reports the event; the schema paths, a dialect's SchemaIntrospector, ColumnAlterer and TableRebuilder, the migration lock and the migrate and quarktenant packages get an Executor that goes through it. QueryEvent.Kind and quark.StatementKindFromContext name what each statement is for, and the probe requires the battery to produce all six kinds. Two middlewares compose, the first registered outermost, as WithMiddleware's godoc now says. Sending Migrate's DDL past the seam turns this control back to partial; so does a battery that stops producing one kind. The OpenTelemetry package and Orbit's SQL bridge are middlewares: a trace now shows the schema work and the savepoints, each span tagged with its kind, and Orbit's feed will once its bridge requires a Quark with this change. |
| `CON-05` | A third-party CacheStore serves cached reads and is invalidated by a write; a third-party EventBus hears committed writes only, after the commit | **present** | — |
| `CON-06` | A registered TypeMapper decides the column type Migrate writes for a Go type Quark does not know, and is handed the dialect's name | **present** | — |
| `CON-07` | Every method Quark calls on a caller's type has an exported interface to implement and assert against | **present** | Measured: the three conventions Quark honours on a caller's type each have an exported interface since A11 Q6 — quark.TableNamer for a model's TableName() string, quark.Validator for a model's Validate(context.Context) error, and quarkdriver.SQLStater for an error's SQLState() string, which classifies any driver's error by its PostgreSQL code. Each was exercised: the table took the name, the validation error aborted the insert, a 23505 classified as a unique violation and a 40P01 as a deadlock. Quark asserts against these types itself: quark.TableNamer is an alias of the internal/schema interface the model metadata asserts, Client.Validate asserts quark.Validator, and the PostgreSQL classification asserts quarkdriver.SQLStater through errors.As. Each is additive: a type with the method already satisfies the interface. Before A11 Q6 Quark asserted them against an internal interface and two anonymous ones, and none had a name a third party could write var _ quark.X = (*T)(nil) against; removing any of the three turns this control back to partial. |
| `CON-08` | The global registries other than the dialect's — classifiers, listener factories, type mappers, generated scanners and binders — are race-free under the race detector | **present** | — |

### drivers — 8 present · 0 partial · 0 absent

| id | control | verdict | what is missing |
|---|---|---|---|
| `DRV-01` | RegisterDialect is safe to call while dialects are being resolved | **present** | Measured in a child process under -race: RegisterDialect writing while DetectDialect and DetectDialectByName read is clean since A11 Q1, which put the registry behind a sync.RWMutex — the same guard the other four registries already had (CON-08). Before it, the registry was a plain map and the detector reported a DATA RACE: a driver that registers from init() is serialised by Go, but a registration after start-up (a test registering its own dialect, a module loaded late) raced with quark.New, which calls DetectDialect. Removing the lock turns this control back to absent. |
| `DRV-02` | A driver module outside this repository registers its dialect and its classifier without importing package quark | **present** | Measured with go list -deps on a driver module built standalone (GOWORK=off) as example.com/extdriver, whose three packages register through quarkdriver alone: errs the classifier (163 packages), dialect a SQLite dialect written against quarkdriver.Dialect, LockOptions, ErrUnsupportedFeature, SchemaIntrospector and the schema model (72 packages), and the module root the database/sql driver and quarkdriver.RegisterDialect (165 packages). None imports package quark. Present since A11 Q3 (ADR-0026), which moved the dialect contract to quarkdriver and left every name in package quark as an alias of the same type; against v1.15.0 the dialect half could not avoid package quark — RegisterDialect lived there and Dialect.LockSuffix named quark.LockOptions — and the driver took 208 packages. Making the fixture's dialect import package quark turns this control back to partial (161 packages for the dialect half, 208 for the driver). The fixture's own test is the application, and imports package quark. |
| `DRV-03` | A driver module outside this repository, built standalone against this tree, opens its engine by name, reads and writes, classifies its duplicate key and passes drivertest.Verify | **present** | The dialect the fixture registers is its own, written against quarkdriver alone (DRV-02); the end-to-end test also takes a row lock its engine refuses — quarkdriver.ErrUnsupportedFeature from the dialect, matched as quark.ErrUnsupportedFeature — and reads the table back through the dialect's SchemaIntrospector as a quark.Schema. Since A11 Q4 the same module also runs the dialect kit against its dialect (DRV-05) and the public engine suite against its engine (DRV-07); its dialect gained the AutoIncrementer and TableRebuilder its engine needs and an introspector that reads indexes and foreign keys, which is what the kit asked of it. |
| `DRV-04` | A dialect from outside is a full participant: SQLite's own dialect methods under another name behave as SQLite's do | **present** | Measured with a battery of 20 steps on two arms — the same engine and the same dialect methods, one named sqlite and one extlite: all 20 agree since A11 Q2. The 11 query steps always did. The 9 schema steps now ask the dialect instead of reading its name: its column types (quarkdriver.ColumnTyper), its auto-increment key (quarkdriver.AutoIncrementer), how it creates only what is missing (quarkdriver.IdempotentDDL), the current columns for Sync (SchemaIntrospector), whether ApplyPlan can run in one transaction (Dialect.SupportsTransactionalDDL) and whether a column change or a constraint goes through SQLite's table rebuild (quarkdriver.TableRebuilder). Against v1.15.0 all 9 diverged: Migrate wrote BIGINT PRIMARY KEY, so the first insert left the key NULL; a second Migrate failed; PlanMigration proposed six changes against SQLite's own table; Sync failed; ApplyPlan refused even an added column. The extlite wrapper forwards every optional interface SQLite's dialect implements — embedding the Dialect interface promotes nothing else — and the probe fails if it stops mirroring them. Putting back any one of the name checks turns a step red; the transactional-DDL check is seen only by the step whose plan fails half-way, because an op that succeeds succeeds on the resumable path too. |
| `DRV-05` | The conformance kit checks a driver's dialect: placeholders, quoting, upsert, limit and savepoints | **present** | Measured by running the kit from the fixture driver built standalone (GOWORK=off): drivertest.VerifyDialect takes the dialect and a live *sql.DB the driver's test opens, and checks every method of quarkdriver.Dialect and every optional interface the dialect implements (skipping, with the reason logged, the ones it does not), on queries and on the schema path — Migrate, PlanMigration, ApplyPlan, Sync, IntrospectSchema, savepoints, row locks, the migration lock — judged by what the engine then holds or refuses, never by the text of a statement. The fixture's own dialect passes with the engine half run; six dialects wrong on purpose each fail, and the failing subtest names the method: placeholders that all bind the first value (engine/Placeholder), quoting that does not escape the quote character (engine/Quote), an upsert that ignores the columns to update (engine/UpsertSQL), LIMIT and OFFSET swapped (engine/LimitOffset), a SavepointDialect whose rollback releases (engine/SavepointDialect), and an AutoIncrementer whose key the engine does not number (engine/AutoIncrementer). The six built-in dialects pass it against their engines in the engine suites; it found two of them wrong — MariaDB dropped a CHECK with MySQL's DROP CHECK (Error 1064) and SQL Server accepted a shared lock that skips locked rows, which the engine refuses (error 650) — both fixed in the same change. A kit that stops catching any one of the six turns this control partial. |
| `DRV-06` | Every driver module of this repository runs the conformance kit | **present** | Measured by running each driver module's tests against this tree (a go.work, as CI's driver lane builds) and looking for the kits' subtests: all five run the dialect kit (drivertest.VerifyDialect on the built-in dialect their driver serves — MySQL's module both MySQL's and MariaDB's), and the four that register a classifier also run drivertest.Verify; postgres registers none by design (CON-07), and the dialect kit is what it runs now. The kit's engine half needs a server: in this bench only SQLite's runs (in memory), and the other modules log that they had no DSN. CI's driver lane gives postgres, mysql and mariadb a server each (services), the oracle lane runs the oracle module's kit against its Oracle, and SQL Server's dialect is held to the same kit in the mssql lane through internal/enginesuite. The standalone lane vets those test files out (-tags pinnedquark) until the release train raises the modules' floor to a quark that has the kit. |
| `DRV-07` | A driver module outside this repository can run the engine conformance suite the in-repo engines run | **present** | Measured from the fixture driver built standalone (GOWORK=off): it imports quarkdriver/drivertest/suite, whose graph is 165 packages against the library's 160 — no container library, no engine driver, no Redis or OpenTelemetry — and its TestEngineSuite passes suite.Run's 43 subtests on its own engine. internal/enginesuite's TestSuiteSQLite runs the same suite.Run, with the same 43 subtests, and so do the five integration lanes (TestSuite<Engine>/EngineSuite). The 43 are the engine-generic subtests of the shared suite, moved out of internal/enginesuite unchanged but for a portable DROP TABLE; the 29 that stay there branch on a built-in engine's name, start a container, need Redis or an OpenTelemetry collector, or touch the tenancy paths — and the dialect kit (DRV-05) is the engine-agnostic form of their dialect half. |
| `DRV-08` | A driver template module builds standalone (GOWORK=off) and passes the kit | **present** | Measured over every go.mod of the repository: 13 modules, each named with its role in the probe's knownModules (a module the list does not name fails the probe), and one of them the driver template. Since A11 Q5 internal/drivertemplate is a driver for SQLite through modernc.org/sqlite under a name of its own (templite), in a module whose path is not this repository's (example.com/drivertemplate), so the Go toolchain refuses it Quark's internal packages as it would a third party's; its go.mod replaces Quark with this tree. Built with no workspace: its code reaches 163 packages and package quark is not one of them (quarkdriver alone), and its tests pass — drivertest.Verify on errors the engine raised (4 subtests passed, the 2 about deadlocks skipped: SQLite has none), drivertest.VerifyDialect with both halves run (56 passed, 6 skipped with the default Quark uses logged), suite.Run's 43 subtests on a client opened by name, a check that the driver imports no package of Quark but quarkdriver, and TestGuideMatchesTemplate, which holds every Go block of website/docs/guides/writing-a-driver.mdx to the template's code. Before it, the 12 modules held no template: the nearest things were the five drivers, each pinned to a published quark, and the fixture this bench builds for DRV-03 in testdata. Swapping LIMIT and OFFSET in the template's dialect (engine/LimitOffset fails), making its dialect import package quark (package quark enters the graph, and the import check fails) and editing one line of the guide each turn this control to partial. |

### integrations — 6 present · 0 partial · 0 absent

| id | control | verdict | what is missing |
|---|---|---|---|
| `INT-01` | chi: a module of the repository serves Quark behind a chi router, its tests pass standalone, and they hold the guide's chi section to that code | **present** | — |
| `INT-02` | Echo: a module of the repository serves Quark behind an Echo server, its tests pass standalone, and they hold the guide's Echo section to that code | **present** | — |
| `INT-03` | Gin: a module of the repository serves Quark behind a Gin engine, its tests pass standalone, and they hold the guide's Gin section to that code | **present** | — |
| `INT-04` | gRPC: a module of the repository serves Quark behind a gRPC service, its tests pass standalone, and they hold the guide's gRPC section to that code | **present** | — |
| `INT-05` | Nucleus: a module of the repository serves Quark from a Nucleus module, its tests pass standalone, and they hold the guide's Nucleus section to that code | **present** | — |
| `INT-06` | quark init --with writes each official integration — chi, Echo, Gin, gRPC and Nucleus — and what it writes builds, vets and tests in a project of its own against this tree | **present** | Measured by running the CLI's TestInitWithBuilds against this tree (the CLI is a module of its own that the bench cannot import, so it runs the module's test through a workspace, as CI's CLI lane does), after reading the initWithTargets literal the flag's validation consults: --with accepts all five, and for each the test writes a project with `quark init --with <target>`, replaces every Quark module it requires with this tree, and runs go mod tidy, go build, go vet and go test with no workspace — a dialect per target, so the driver rewrite compiles for every engine module. What init writes is the fixture's code (A11 Q10): the CLI embeds byte-for-byte copies of internal/integrations, a CLI test fails when a copy and its fixture differ, and init changes only the package clause and its doc comment, the import paths and the driver module, beside a server main for chi, Echo, Gin and gRPC. Before A11 Q10 --with accepted nucleus alone and what it wrote was built by nothing; a probe that counted a module requiring the framework as compiling the template's output said less than that. Breaking one copy turns its subtest red and this control to partial. |
