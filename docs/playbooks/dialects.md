---
type: playbook
module: dialects
files:
  - dialect.go
  - quarkdriver/dialect.go
  - quarkdriver/drivertest/dialect.go
  - quarkdriver/migration_lock.go
  - quarkdriver/schema_model.go
  - quarkdriver/schema.go
last_review: 2026-10-05
related_adrs: [0005, 0023, 0026]
related_p0: []
closed_p0: [P0-2]
phase: 0
---

# Playbook: Dialectos SQL

## Qué cubrimos

Seis dialectos: **SQLite** (con dos drivers, `mattn/go-sqlite3` y `modernc.org/sqlite`), **PostgreSQL** (`pgx/v5`), **MySQL** y **MariaDB** (`go-sql-driver/mysql`; MariaDB embebe MySQLDialect + extras), **MSSQL** (`microsoft/go-mssqldb`), **Oracle** (`sijms/go-ora/v2`).

Cada `Dialect` provee:

- `Placeholder(n int) string` — `?` (MySQL/SQLite), `$n` (Postgres), `@p{n}` (MSSQL), `:{n}` (Oracle).
- `Quote(identifier) string` — `"x"` / `` `x` `` / `[x]`.
- `LimitOffset(limit, offset) string` — sintaxis específica (Postgres `LIMIT/OFFSET`, MSSQL/Oracle `OFFSET ... FETCH NEXT ... ROWS ONLY`).
- `RETURNING` — disponible en Postgres y SQLite (3.35+), simulado con `OUTPUT INSERTED` en MSSQL, no soportado en MySQL/MariaDB/Oracle.
- `LastInsertIDQuery` — `last_insert_rowid()` (SQLite), `LASTVAL()` (PG con secuencia), `SCOPE_IDENTITY()` (MSSQL), driver-level (MySQL).
- `JSONExtract`, `UpsertSQL`, `BuildRoutineQuery`, DDL básico (`AlterTable*`, `RenameColumn`, `RenameTable`).

Registro de dialectos custom: `RegisterDialect("vertica", verticaDialect)`.

### Where the contract lives (ADR-0026, A11 Q3)

The contract is declared in `quarkdriver`, the leaf package a driver module
imports: `Dialect` and `LockOptions`/`LockMode` (`quarkdriver/dialect.go`),
`SavepointDialect`, `ColumnTypeMapper`, the sentinels `ErrUnsupportedFeature`
and `ErrLockTimeout`, the registry (`RegisterDialect`, `LookupDialect`); the
migration lock (`quarkdriver/migration_lock.go`); `SchemaIntrospector` and the
schema model (`quarkdriver/schema_model.go`); and the schema-path questions of
A11 Q2 (`quarkdriver/schema.go`). Package `quark` declares each name again as
an ALIAS (`type Dialect = quarkdriver.Dialect`), never as a copy, and
`quark.RegisterDialect` calls `quarkdriver.RegisterDialect`. `DetectDialect`,
`DetectDialectByName`, `ErrDialectNotSupported` and the six built-ins stay in
package `quark`.

Rules that follow:

- **New contract is born in `quarkdriver`.** A type a dialect implements or a
  dialect method names is declared there from its first commit, and aliased
  from `quark` only if applications name it. If it names a type that is still
  in `quark`, that type moves too, as an alias.
- **Nothing in `quarkdriver` imports package `quark`.** It stays a leaf.
  The bench measures it: `DRV-02` builds `internal/extbench/testdata/extdriver`
  standalone, whose dialect (`dialect/dialect.go`) is written against
  `quarkdriver` alone, and fails if any of its packages needs the root.
- **An alias, not a copy.** `TestDialectContractAliases`
  (`dialect_contract_alias_test.go`) checks that every name is the same
  `reflect.Type` under both packages and that the sentinels are one value.
  `dialect_contract_v115_test.go` is written against v1.15.2's names only and
  must keep compiling unchanged — do not add `quarkdriver` names to it.
- **Methods cannot be added to an alias.** `quark` cannot declare a method on
  `Schema`, `LockOptions` or any moved type; a helper on them is a function in
  `quark` or a method declared in `quarkdriver`.

### The published contract (A11 Q6)

[`website/docs/reference/extension-contract.mdx`](../../website/docs/reference/extension-contract.mdx)
is the contract a driver or dialect author reads: one table with every exported
type of `quark` and `quarkdriver` that code outside them can implement — every
interface Quark asserts on a dialect, `Classifier`, `SQLStater`, the listener
contract and the adapters handed to a migration lock — each marked as an
extension point or plumbing, with its stability (`stable`, `experimental`,
`internal-use`) and where Quark uses it. The six schema-path interfaces of Q2
are `experimental`: no engine outside this repository has implemented them yet.

**The promise those two columns describe is a PROPOSAL pending the owner's
decision**, and the page says so. Until it is adopted — which will be recorded
in an ADR — the v1 promise of `website/docs/operations/upgrade.mdx` ("code that
compiles against v1.3 compiles against every later v1.x") stands for EVERY
exported type, extension point or plumbing: adding a method to any interface a
third party can implement is a breaking change, whatever its row says.

Rules that follow:

- **A new implementable type gets its row in the same PR.** `CON-01` in
  `internal/extbench` computes the census from the compiled packages and
  fails when the table misses a type, names one twice (an alias and its
  target are one type), names something outside the census, or uses a value
  outside the page's vocabulary.
- **An extension point gains no method within v1** (part of the proposal,
  and already true under the adopted v1 promise). A new capability of a
  dialect is a new optional interface Quark type-asserts (the Q2 pattern),
  never a method added to `Dialect`; adding one breaks every dialect outside
  the repository. What the proposal would add — that a plumbing interface
  may gain a method in a minor — is NOT in force: do not rely on it until the
  ADR exists.
- **Signatures are frozen.** `acceptance/apisurface.json` records the `sig`
  of every func, method and type; changing a parameter of a contract method
  makes CI's freshness check fail with the diff, and `CON-02` checks that the
  file holds the compiler's signature for the 94 symbols an implementation
  depends on. Regenerate with `make regen` only when the change is meant.
- **A convention Quark calls on a caller's type has an exported interface.**
  `quarkdriver.SQLStater` is the one for a driver's errors: Quark reads its
  code as a PostgreSQL SQLSTATE and, for unique violations and deadlocks,
  does not consult the registered classifiers — a driver for another engine
  registers a `Classifier` and does not implement `SQLState()`.

## Bugs P0 vivos

(ninguno en este módulo; ver § Historial.)

## Historial — bugs cerrados

### P0-2 · `JSONExtract` concatenaba el path (cerrado)

`Dialect.JSONExtract` cambió de `(column, path string) string` a `(column, path string) (sql string, args []any, err error)`. SQL fragment usa `?` como marker neutral; `query_exec.go:substitutePathMarkers` los traduce al placeholder de cada motor en build time. Cada dialecto llama `guard.ValidateJSONPath` antes del bind. Detalles del fix y decisiones (rechazo de leading `$`, max 256 chars) en `docs/playbooks/security.md` § Historial.

**Impacto en custom dialects**: cualquier dialecto registrado vía `RegisterDialect("vertica", …)` debe actualizar la firma. Pre-1.0; sin uso conocido externo.

## Lo que está bien hecho (no romper)

### Upsert por dialecto correcto

- **PG/SQLite**: `INSERT ... ON CONFLICT (cols) DO UPDATE SET ...`.
- **MySQL/MariaDB**: `INSERT ... ON DUPLICATE KEY UPDATE ...`.
- **MSSQL/Oracle**: `MERGE ... USING (VALUES ...) ...` construido a mano (`query_crud.go:1074-1183`, `query_crud.go:1507-1606`).

Esto está por encima de bun y al nivel de ent. **No simplifiques esto** sin verificar que mantienes el comportamiento de los 6 motores.

### `OFFSET/FETCH` con `ORDER BY` automático en MSSQL/Oracle

`buildSelect` (`query_exec.go`, rama justo tras la cláusula ORDER BY explícita) inyecta un ORDER BY cuando hay LIMIT/OFFSET sin ORDER BY explícito y el dialecto es MSSQL/Oracle (lo exigen para `OFFSET/FETCH`). Usa el **PK** por defecto, pero cae a la posición ordinal **`ORDER BY 1`** cuando hay `DISTINCT`, `GROUP BY` o un **set-op** (UNION/INTERSECT/EXCEPT): esos restringen el ORDER BY a columnas del select-list o a un ordinal, y el PK no proyectado los rompe (ORA-01791/ORA-00979 en Oracle; "ORDER BY items must appear in the select list" bajo compound-select — Finding J). Si introduces nuevo path de paginación, sigue este patrón — un OFFSET sin ORDER BY en estos motores es un error sintáctico.

### MariaDB se autodetecta por versión de servidor (BB-3)

MariaDB no tiene driver `database/sql` propio (usa `go-sql-driver/mysql`, nombre "mysql"), así que `DetectDialect` no puede distinguirlo por nombre. `client.New` hace `SELECT VERSION()` una vez en conexiones "mysql" (`isMariaDBServer`) y cambia a `MariaDBDialect` si el server es MariaDB; un `WithDialect` explícito gana y salta el probe. Consecuencia: `MariaDBDialect.LockSuffix` emite `LOCK IN SHARE MODE` para `ForShare` (MariaDB no tiene `FOR SHARE` — `Error 1064`), y rechaza `ForShare`+`SkipLocked`/`NoWait` con `ErrUnsupportedFeature` (esa forma no admite modificadores). Si añades comportamiento dialect-divergente MariaDB↔MySQL nuevo, ponlo en `MariaDBDialect` (override), no en `MySQLDialect`.

**Oracle + lock pesimista (BB-4):** Oracle prohíbe combinar el row-limiting clause (`OFFSET/FETCH`) con `FOR UPDATE`/`SKIP LOCKED`/`NOWAIT` — **ORA-02014**. `buildSelect` detecta `!q.lock.IsZero() && dialect=="oracle"` y activa `suppressRowLimit`, que inhibe **tanto el OFFSET/FETCH como el ORDER BY implícito** (sin row-limiting, Oracle no exige ORDER BY). El cap implícito de `List()` se descarta (lock sobre todas las filas, con WARN); un `Limit`/`Offset` explícito —o `First()`, que aplica `Limit(1)`— junto al lock devuelve `ErrUnsupportedFeature`. Sólo Oracle; MSSQL usa table hints y sí convive con OFFSET/FETCH.

### Wrapper `timeScanner` para MySQL

`query_exec.go:27-71`. MySQL en algunos drivers/configs devuelve `[]byte` para columnas `DATETIME` en lugar de `time.Time`. El wrapper parsea cuatro formatos. **No quites este código sin verificar primero qué devuelve cada driver para columnas de tiempo en su matriz de configuración.**

### The dialect registry is guarded by a lock (A11 Q1)

`quarkdriver.RegisterDialect` writes, and `quarkdriver.LookupDialect` reads,
`dialects` under `dialectMu` (`quarkdriver/dialect.go`, since A11 Q3; Q1 put
the lock on the map when it lived in `dialect.go`). `quark.RegisterDialect`
calls the first, and `DetectDialect` / `DetectDialectByName` call the second
before the built-in names. Before Q1 it was a plain map, and a registration
after start-up raced with any goroutine building a client (bench control
`DRV-01`). Keep every reader behind `LookupDialect`. Regression:
`TestRegisterDialectConcurrent` in `dialect_unit_test.go` (the race lane runs
it) and `DRV-01` in `internal/extbench`, whose race fixture hammers both
names and turns red if the lock goes.

### The schema path asks the dialect (A11 Q2)

Migrate, PlanMigration, Sync and Quark's bookkeeping tables ask the dialect,
through optional interfaces declared in `quarkdriver/schema.go` (ADR-0026: new
contract is born in the leaf), for its column types (`ColumnTyper`), its
auto-increment key (`AutoIncrementer`) and how it creates only what is missing
(`IdempotentDDL`); ApplyPlan asks `SupportsTransactionalDDL()`, and for its
statements `ColumnAlterer`, `ObjectDropper` and `TableRebuilder` (SQLite's
rebuild), implemented in `dialect_alter.go`. Oracle's
`SupportsTransactionalDDL()` answered true until Q2 made it matter; it is
false. The six built-ins implement them in `dialect_schema.go` by
passing their own ENGINE constant to `internal/migrate/engines.go` — never by
reading `Name()`, which a wrapper can change. A dialect that implements none
gets the portable answers (`TestSchemaPathPortableDefaults`). A wrapper that
embeds a built-in must FORWARD these interfaces: embedding the `Dialect`
interface promotes its methods and nothing else (the extlite wrapper in
`internal/extbench/probes_participant_test.go` is the reference, and its
faithfulness check lists every optional interface).

### The conformance kit holds every dialect to the contract (A11 Q4)

`drivertest.VerifyDialect` (`quarkdriver/drivertest/dialect*.go`) checks a
dialect against a live engine: every method of `Dialect` and every optional
interface the dialect implements, through Quark's queries and its schema path,
judged by what the engine then holds or refuses. The six built-ins run it in
the engine suites (`internal/enginesuite/conformance_test.go`, subtest
`TestSuite<Engine>/DialectKit`), the five driver modules in their own tests
(`drivers/*/kit_test.go`), and the bench's fixture driver too (`DRV-05` breaks
the fixture's dialect six ways and expects six named failures). Its first run
found MariaDB's `DROP CHECK` (MariaDB has none: `DropCheck` writes
`DROP CONSTRAINT`) and SQL Server's `ForShare().SkipLocked()` (`HOLDLOCK` is
serializable and `READPAST` is refused there: now `ErrUnsupportedFeature`).

`quarkdriver/drivertest/suite` is the engine-generic half of what
`SharedSuite` ran: 43 subtests moved there unchanged, which a third party's
driver runs with `suite.Run` and the in-repo engines run through
`TestSuite<Engine>/EngineSuite`. What stays in `SharedSuite` branches on a
built-in's name, starts a container, or needs Redis or an OpenTelemetry
collector.

Rules that follow:

- **A new optional interface gets its kit check in the same change.** The
  check names the interface, exercises it when implemented and its default
  when not, and asserts on the engine's behaviour, never on a statement's text.
- **A dialect change keeps the kit green on its engine.** The kit runs in the
  integration lane of every engine; a red `DialectKit` is a dialect bug or a
  kit bug, never something to skip.
- **The suite package stays engine-agnostic.** A test that has to branch on
  `Dialect().Name()` belongs in `internal/enginesuite`, not in
  `quarkdriver/drivertest/suite`; a test there that a third-party engine cannot
  pass for a reason of the engine (no window functions) is skipped by the
  driver with `go test -skip`, not by the suite.
- **The kit's test files in the driver modules are tagged `!pinnedquark`**
  while the modules' floor predates the kit: the standalone CI lane vets with
  `-tags pinnedquark`. The tag becomes unnecessary once the release train
  raises the floors to the release that ships the kit.

## Anti-patterns a vigilar

### Branching on `Dialect.Name()` where an interface can answer

`DRV-04` in `internal/extbench` measures it: SQLite's methods under another
name must behave as SQLite's. A `switch d.Name()` in schema code makes that
dialect take another engine's branch. Ask an existing method
(`SupportsTransactionalDDL`, `LimitOffset`, …) or add an optional interface in
`quarkdriver` with a documented default.

### Asumir un placeholder

```go
// MAL
sql := fmt.Sprintf("SELECT * FROM users WHERE id = ?")

// BIEN
sql := fmt.Sprintf("SELECT * FROM users WHERE id = %s", dialect.Placeholder(1))
```

Cualquier SQL nuevo construido en el código debe usar `dialect.Placeholder(n)`. Buscar `?` hardcoded en el código fuera de tests es un anti-pattern detectable.

**Excepción documentada**: `Dialect.JSONExtract` devuelve un fragmento con `?` como **marker neutral** que `query_exec.go:substitutePathMarkers` traduce a placeholders dialect-specific en render time. Es deliberado y centralizado — no lo extiendas a otros sitios sin discutirlo, mantén la regla general "usar `dialect.Placeholder(n)` directamente".

### Asumir un quoting

```go
// MAL
sql := "SELECT * FROM \"users\""

// BIEN
sql := "SELECT * FROM " + dialect.Quote("users")
```

### Oracle uppercasea identifiers automáticamente

`dialect.go:622` (Oracle dialect). Esto rompe esquemas con identifiers entre comillas case-sensitive. Es deuda conocida — no hay opción para desactivarlo. Si emerges con un caso de uso que exija lower-case Oracle, abre issue: la solución requiere un flag por dialecto.

### `maxIdentifierLen=64` rompe Postgres (63 max)

Hoy `internal/guard/guard.go` tiene 64. Postgres rechaza identifiers de 64+ caracteres (truncará silenciosamente o errará según versión). Oracle ≤ 30 (legacy) o 128 (12c+). MSSQL 128. **No es configurable por dialecto** hoy. Es deuda — cuando lo arregles, hazlo por dialecto, no global.

### Sin tipos nativos Postgres

Hoy:
- **Sin arrays nativos** (`int[]`, `text[]`). Los slices Go no se mapean.
- **Sin UUID nativo** — se cuela como `VARCHAR(36)` en `internal/migrate/migrate.go:25-34`.
- **Sin `tstzrange`, `daterange`, `inet`, `hstore`, `bytea` tipado.**

ADR 0002 (reflect → codegen Fase 6) lo tendrá más fácil de resolver con codegen. Hasta entonces, requieren `pgtype.Array`/`pgtype.UUID` envueltos por el usuario.

### Interfaz `Dialect` no es ortogonal

Mezcla SQL builder + DDL + procedures + JSON. Cuando MariaDB añade `CreateSequence`/`HistoryQuery` (`dialect.go:768-806`), sólo accesibles vía type-assert. **No añadas más métodos a `Dialect` sin considerar si pertenecen a una interfaz secundaria** (`SequenceSupport`, `TemporalTablesSupport`, etc.) que el usuario obtiene con type-assert opcional.

## Decisiones que afectan al módulo

- **ADR 0026 (dialect contract in `quarkdriver`)**: the `Dialect` interface, the types it names, the optional interfaces, the schema model, the sentinels and the registry live in `quarkdriver` since A11 Q3, and package `quark` keeps every name as an alias. **Declare any NEW dialect contract type in `quarkdriver`**, not here (see § Where the contract lives).

- **ADR 0005 (Solo relacional)**: no hay backends NoSQL. TimescaleDB/CockroachDB se aceptan vía dialecto Postgres si emergen.

## Roadmap de mejora

- **Fase 0**: ~~cerrar P0-2 (JSON path) — cerrado, ver § Historial.~~
- **Fase 1**: tipos ricos — `decimal.Decimal`, `uuid.UUID`, `time.Duration`, `[]byte`/`bytea`, `JSON[T]` genérico, arrays Postgres.
- **Fase 2**: AST permite expresar window functions, locking, CTEs por dialecto.
- **Fase 3**: introspección completa (tipos, NOT NULL, defaults, índices, FKs, checks) por dialecto.
- **A11 Q5 (driver template and "Writing a driver")** builds on the kit:
  the reference implementation is `internal/extbench/testdata/extdriver` — a
  dialect written against `quarkdriver` alone that passes `VerifyDialect` and
  `suite.Run` (it needed `AutoIncrementer`, `TableRebuilder` and an
  introspector that reads indexes and foreign keys). A template module with
  its own `go.mod` built `GOWORK=off` needs a quark that ships the kit (the
  release after A11 Q4) or a `replace` as the fixture has; it is a new module,
  so the umbrella's `align-module-floors.sh`, Dependabot's list and `DRV-08`'s
  `knownModules` have to learn it. What the guide must say that the contract
  cannot express yet: the query path still branches on the names `mssql` and
  `oracle` (`query_crud.go`, `query_exec.go`) and the LIKE escape tail on
  `mysql`/`mariadb`/`mssql` (`like.go`); referential actions reach the DDL as
  written (Oracle refuses `ON UPDATE` and `ON DELETE NO ACTION`); and Oracle's
  `VARCHAR2(n)` counts bytes, not characters.

## Tests críticos a no romper

- `drivertest.VerifyDialect` en las suites por motor (`TestSuite<Engine>/DialectKit`) y en los módulos de driver (`drivers/*/kit_test.go`) — el contrato del dialecto contra el motor vivo.
- `dialect_test.go` y `dialect_unit_test.go` — pruebas unitarias por dialecto (placeholder, quote, limit/offset, returning).
- `n_fixes_test.go` — bugs Oracle/MSSQL retroalimentados por auditoría.
- Suites por motor (`postgres_suite_test.go`, etc.).

## Cuándo invocar al `code-reviewer`

Antes de cualquier PR que añada un dialecto, modifique los upserts, toque la interfaz `Dialect` o introduzca tipos nuevos. El reviewer verifica que el cambio aplica en los 6 motores (o justifica por qué algunos no), que no asume placeholder/quoting, y que los tests cubren los 6.
