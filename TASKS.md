# Quark — backlog táctico

> **Fase 0 cerrada (2026-05-10).** Los 5 P0 originales están tachados abajo;
> el repo queda consolidado en `main` sin branches huérfanas y con todas las
> docs al día. Backlog vivo ahora en **Fase 1** (`docs/ANALISIS_MADUREZ.md` §4).
>
> Convención: cada tarea lleva su archivo:línea de origen, criterio de "done"
> y dónde queda la documentación al cerrar.

---

## Fase 2 — Query builder componible y locking

### ~~F2-locking · Pessimistic locking~~

**Cerrado** — `Query[T].ForUpdate()`, `ForShare()`, `SkipLocked()`, `NoWait()`
modifiers en `locking.go`. Nuevo `Dialect.LockSuffix(opts) (tableHint,
suffix string, err error)` consumido por `buildSelect`. Implementaciones en
`dialect_lock.go`:

- PG / MySQL / MariaDB: `FOR UPDATE [SKIP LOCKED|NOWAIT]` / `FOR SHARE` suffix.
- Oracle: igual a PG, sin `FOR SHARE` (devuelve `ErrUnsupportedFeature`).
- MSSQL: table hints `WITH (UPDLOCK|HOLDLOCK, ROWLOCK [, READPAST])` en FROM.
  No tiene NOWAIT directo → `ErrUnsupportedFeature`.
- SQLite: cualquier opción no-zero → `ErrUnsupportedFeature` (usar `BEGIN IMMEDIATE`).

Sentinel nuevo `ErrUnsupportedFeature` en `errors.go`.

Cobertura: 17 unit tests (`TestLockSuffix_PerDialect` table-driven sobre los
6 motores con todas las combinaciones, `TestLockOptions_IsZero`,
`TestForUpdate_BuildsLockedSelect`) + `testPessimisticLocking` en
SharedSuite (no-op baseline + SQLite-unsupported).

Doc: `website/docs/guides/querying.mdx` § Pessimistic Locking con la matriz
por dialect y nota sobre transacciones; `website/docs/reference/api/errors.mdx`
ErrUnsupportedFeature; CHANGELOG `### Added`.

### ~~F2-IN-chunking · Chunking automático de `IN(...)` por dialect~~

**Cerrado** — `chunkParentKeys` helper en `query_exec.go` (constante
`inChunkSize = 1000`, conservadora para los 6 motores). Las 3 funciones de
preload — `loadStandardRelation` / `loadM2MRelation` / `loadPolymorphicRelation` —
ahora envuelven sus IN-load en el helper y agregan resultados a través de
chunks. Los predicados de tenant / poly-type discriminator se re-aplican
por chunk.

Cobertura: `testINChunking/PreloadChunksAt1000` en SharedSuite (2500 padres
× 1 child cada uno → 3 IN(...) selects observados via middleware) +
`TestChunkParentKeys_Contract` con la matemática de redondeo.

### ~~F2-AST · Tipo `Expr` componible~~

**Cerrado** — `expr.go` introduce el AST y `query_builder.go` añade
`Query[T].WhereExpr(e Expr)` y `Query[T].HavingExpr(e Expr)`. Nodos:
`Col`, `Lit`, `And`, `Or`, `Not`, `Cmp` (+ `Eq`/`Ne`/`Lt`/`Gt`/`Lte`/`Gte`),
`In`, `NotIn`, `Func`. Cada nodo implementa
`ToSQL(d Dialect, g *SQLGuard) (string, []any, error)`; los identificadores
pasan por `ValidateIdentifier`, los operadores por `ValidateOperator`, y los
nombres de función contra una whitelist conservadora de 10 entradas
(COUNT/SUM/AVG/MIN/MAX/LOWER/UPPER/LENGTH/COALESCE/ABS).

El AST emite `?` como bind marker neutral; `WhereExpr`/`HavingExpr`
almacenan el fragmento en el slot `condition{isRaw:true, operator:""}` y
`buildWhereClause` reutiliza `substitutePathMarkers` para reescribir cada
`?` al placeholder del dialecto en el `argIndex` correcto. La forma
componible `Having(Func("count", Col("*")), ">", 5)` queda disponible vía
`HavingExpr(Gt(Func("COUNT", Col("*")), Lit(5)))`.

`Exists` queda fuera del AST v0.4 — aterriza con F2-subqueries cuando
exista la pieza `Subquery`.

Cobertura: `expr_test.go` (7 tests unitarios sobre cada nodo + composición)
+ `testExprAST` en SharedSuite (5 subtests: EqAndOrFiltersCorrectRows,
InFiltersMultipleValues, NotWrapsCompare, HavingExprWithFunc,
InvalidIdentifierSurfacesAtExec, PlaceholderSubstitution).

Doc: `website/docs/guides/querying.mdx` § Composable Expressions con tabla
de nodos + ejemplo HavingExpr; CHANGELOG `### Added`.

### ~~F2-subqueries · `AsSubquery()` integrable~~

**Cerrado** — `subquery.go` introduce `Subquery` (snapshot del SELECT
renderizado con `?` markers via `qmarkDialect`), `Query[T].AsSubquery()`
+ `MustAsSubquery()`, y los wrappers Expr `Sub`, `Exists`, `NotExists`,
`InSub`, `NotInSub`. La captura usa el dialect activo para Quote /
LimitOffset / JSONExtract / LockSuffix pero overridea Placeholder a `?`
para que el AST exterior renumere a placeholders del dialecto en el
`argIndex` correcto. Errores en validación interna (identifier inválido)
afloran en el momento de `AsSubquery`, no en la ejecución exterior.

`Cast` queda fuera de v0.4 — se añade ad-hoc cuando aparezca un caso
real (typed column projections del codegen, Fase 6).

Cobertura: `subquery_test.go` (1 test unitario sobre placeholders +
ordering de args) + `testSubquery` en SharedSuite (4 subtests:
InSubFiltersUsersWithPositiveOrders, NotInSubFiltersUsersWithoutPositiveOrders,
SubAsScalarComparison, InvalidInnerIdentifierSurfacesAtCapture).

Doc: `website/docs/guides/querying.mdx` § Subqueries con tabla de
wrappers; CHANGELOG `### Added`.

### ~~F2-CTE · `With("t", subq)` + `WithRecursive`~~

**Cerrado** — `cte.go` introduce `Query[T].With(name, sub *Subquery)` y
`WithRecursive(name, sub *Subquery)`. `BaseQuery.ctes` (`[]cteEntry`) se
añade al state y `clone()` lo deep-copia. `buildSelect` antepone el
prefijo `WITH "name" AS (<inner>)` (o `WITH RECURSIVE ...` si alguna
entrada es recursiva), substituye los `?` markers internos via
`substitutePathMarkers` con `argIndex = len(args)+1`, y prepende los
args inner al slice global. WHERE/HAVING reindexan automáticamente
porque su `argIndex` ya es `len(args)+1`.

El cuerpo recursivo en sí necesita `UNION ALL`, que llega con F2-set.
Hasta entonces el caller compone la recursión a través del Subquery
fuente.

Cobertura: `testCTE` en SharedSuite con 5 subtests
(WithPrependsCTEAndJoins, WithRecursiveEmitsRECURSIVE,
InvalidCTENameSurfacesAtExec, NilSubqueryRejected,
CTEArgsAreThreadedBeforeWHERE). Los asserts sobre el SQL emitido pasan
por middleware (`cteCapturingMiddleware`).

Doc: `website/docs/guides/querying.mdx` § Common Table Expressions
(CTEs); CHANGELOG `### Added`.

### F2-window · Pendiente
`OverWindow(name).PartitionBy(...).OrderBy(...)`; `RowNumber`/`Rank`/`Lag`.

### F2-set · Pendiente
`UNION` / `INTERSECT` / `EXCEPT` entre `Query[T]`.

### ~~F2-having-agg · HAVING sobre agregados~~

**Cerrado** — `Query[T].HavingAggregate(fn, column, operator, value)` en
`query_builder.go`. Whitelist de fns (COUNT/SUM/AVG/MIN/MAX, case-insensitive);
column va por `ValidateIdentifier` salvo `*` que sólo se acepta con COUNT.
Internamente construye la expresión `<FN>(<col>) <op> ?` y la mete como
condición con `isRaw: true` en el slot de `having[]` que `buildWhereClause`
ya soporta.

Cobertura: `testHavingAggregate` en SharedSuite, 6 subtests:
CountStarGreaterThan, SumGreaterEqual, CaseInsensitiveFn, RejectsUnknownFn,
RejectsStarOnNonCount, RejectsInvalidColumn. Las verificaciones de SQL
emitido pasan por middleware (Count() devuelve total rows, no group count,
así que no sirve para validar GROUP BY semantics).

Doc: `website/docs/guides/querying.mdx` § Grouped Aggregates and HAVING
con tabla de reglas; CHANGELOG `### Added`. La forma plenamente componible
`Having(Func("count", Col("*")), ">", 5)` aterrizará con el AST de Fase 2.

### ~~F2-nested-preload · `.Preload("Orders.Items.Product")`~~

**Cerrado** — `parsePreloads` (`preload_tree.go`) parsea las paths dotted en
un árbol de `preloadNode` y fusiona prefijos compartidos. `loadRelations`
ahora delega a `loadPreloadTree` que itera el árbol: por cada nodo, llama
al loader correspondiente (loadStandard/loadM2M/loadPolymorphic), y si tiene
`children` recolecta el slice cargado vía `gatherLoadedChildren` (devuelve
`[]*RefType` para que las mutaciones aliasen back al padre) y recurse.

Refactor estructural: los 3 loaders + 2 scan-and-map funciones movidos de
`*Query[T]` a `*BaseQuery` aceptando `parents reflect.Value, ownerMeta *ModelMeta`.
La generic-erasure permite la recursión sin instanciar Query[T] por nivel.

Cobertura: `testNestedPreload` en SharedSuite (3 subtests):
DottedPathLoadsBothLevels (2 authors × 2 posts × 2 comments),
FirstLevelStillWorks (single-level Preload no recurse),
SharedPrefixDoesNotDoubleLoad (`Preload("Posts", "Posts.Comments")` ≡
`Preload("Posts.Comments")`).

Doc: `website/docs/guides/relations.mdx` § Eager Loading with Preload con
sub-secciones "Nested preload" y "IN-list chunking"; CHANGELOG `### Added`.

### F2-join-builder · Pendiente
Builder estructurado `Join(table).On(col, op, otherCol)` reemplazando la deprecation actual de string-raw Join.

---

## Fase 1 — Tipos ricos y dirty tracking ligero (cerrada en v0.3.0)

### ~~F1-1 · Dirty tracking ligero (cierre permanente de P0-4)~~

**Cerrado** — `Query[T].Track()` devuelve `*TrackedQuery[T]` cuyas
`Find/First/List` envuelven cada entidad cargada en `*Tracked[T]` con un
snapshot por columna. `Tracked.Save(ctx)` emite UPDATE sólo de columnas
cambiadas (snapshot-vs-current; sin filtro `isZeroValue`, así que `false`/`0`/`""`
se escriben). Snapshot vive en el wrapper — sin identity map global, sin GC
pressure. Tenant predicate del query padre se propaga al WHERE de Save; PK
y tenant column nunca van al SET aunque el caller los mute.

Cobertura: `testDirtyTracking` (`dirty_track_test.go`) wired a `SharedSuite`
con 5 subtests: WritesZeroValuesWhenChanged, NoChangeMeansNoSQL,
SnapshotRefreshesAfterSave, ListReturnsTrackedSlice, PrimaryKeyNeverMutated.
Doc: `website/docs/reference/api/crud.mdx` § "Track + Save (dirty tracking)";
CHANGELOG `### Added`; Historial en `docs/playbooks/query-builder.md` §P0-4
(cierre permanente).

### ~~F1-2 · Tipos ricos~~

**Cerrado** (parte core; arrays Postgres y timezones quedan deferred a Fase 2
porque requieren motor-specific work no trivial).

- **`quark.JSON[T any]`** (`json_field.go`): wrapper genérico que implementa
  `sql.Scanner`/`driver.Valuer` vía `encoding/json`. Migrate detecta el
  wrapper (`internal/migrate.isQuarkJSON` por package + name prefix) y emite
  JSON column dialect-native: PG JSONB, MySQL/MariaDB JSON, SQLite TEXT,
  MSSQL NVARCHAR(MAX), Oracle CLOB.
- **`[]byte` mapping**: añadido al `internal/migrate.SQLType` switch — PG
  BYTEA, MSSQL VARBINARY(MAX), resto BLOB. Antes caía a TEXT (silently
  wrong en BLOB-heavy workloads).
- **`time.Duration`**: ya cerrado en F1-4 (registrado como BIGINT/NUMBER(19)).

Cobertura: `testJSONField` (`json_field_test.go`) wired a `SharedSuite`. 3
subtests: StructValueRoundTrip (struct + slice + map + []byte), ZeroValueScansAsZero,
UpdateReplacesPayload (vía Tracked.Save para validar la integración con dirty
tracking).

Deferred a Fase 2 con su propio scope:
- **Arrays Postgres** (`pgtype.Array`) — requiere wrapper neutro que
  abstraiga el concepto sin pegar el dialect a `pgtype` directamente.
- **Timezones** (default UTC + override por columna) — diseño abierto sobre
  cómo configurar el override (tag `quark:"tz=UTC"` vs Client option).
- **`shopspring/decimal` y `google/uuid` pre-registered**: el usuario puede
  registrarlos en su init con `RegisterTypeMapper` (F1-4); Quark no los
  pre-registra para no añadir dependencias obligatorias. Documentado en el
  ejemplo de modeling.mdx § Custom type mappers.

Doc: `website/docs/guides/modeling.mdx` § Typed JSON columns + § Binary
columns; CHANGELOG `### Added`.

### ~~F1-3 · `Nullable[T]` genérico~~

**Cerrado** — `quark.Nullable[T]` aliasa `database/sql.Null[T]` (Go 1.22+);
constructores `SomeOf(v)` / `NullOf[T]()` en `nullable.go`. Round-trip funciona
sin cambios en quark porque `*sql.Null[T]` ya implementa Scanner/Valuer.
`internal/migrate.SQLTypeWithOpts` detecta `sql.Null[T]` (helper `isSQLNull`)
y recursa al tipo T, así que `Nullable[int64]` → BIGINT, `Nullable[time.Time]`
→ TIMESTAMP/DATETIME/DATETIME2 por dialect, sin custom mapper.

Cobertura: `testNullable` (`nullable_test.go`) wired a `SharedSuite`. 3 subtests:
RoundTripValuesAndNulls (4 tipos: string, int64, time.Time, float64; mezcla
de Some/None), ExplicitNullSomeAndNone (todo NULL), SomeOfPreservesValues
(time.Time con `.Equal()` para resistir el monotonic-clock issue del F1-1).

Doc: `website/docs/guides/modeling.mdx` § Nullable columns; CHANGELOG `### Added`.

### ~~F1-4 · `RegisterTypeMapper`~~

**Cerrado** — `quark.RegisterTypeMapper(reflect.Type, TypeMapper)` enrutado
a `internal/migrate.RegisterTypeMapper` (sync.Map por reflect.Type, pointer
stripping al registrar). `internal/migrate.SQLTypeWithOpts` consulta el
registry antes del switch built-in, propagando `TypeOptions{Size, Precision,
Scale, IsPK}`. Tag db extendido: `db:"name,size=512"`, `db:"price,precision=18,scale=4"`
parseado en `internal/schema.parseDBTag`. `FieldMeta` lleva ahora `Size`,
`Precision`, `Scale`. Helper `internal/schema.ColumnFromDBTag` strippea
opciones para el guard en hot paths (`query_crud.go` ×8 sites + `query_exec.go` ×1).
`time.Duration` registrado por defecto → BIGINT (NUMBER(19) en Oracle).

Cobertura: `testTypeMapper` (`type_mapper_test.go`) wired a `SharedSuite`,
4 subtests: DurationMapsToBigInt (round-trip), CustomMapperHonored (IPAddr
custom type), SizeTagOptionRespected (500-char bio en `db:"bio,size=512"`),
PointerTypeStrippedOnRegistration (`*time.Duration`). Doc en
`website/docs/guides/modeling.mdx` § Field Tags + § Custom type mappers;
CHANGELOG `### Added`.

### ~~F1-5 · Soft delete real~~

**Cerrado** — `Query[T].WithTrashed()` (incluye trashed) y `Query[T].OnlyTrashed()`
(solo trashed) suman a `Unscoped()` (mantenido como alias). Filtro
`deleted_at IS NULL` por defecto sigue siendo automático en reads/Count/aggregates;
ahora centralizado en `BaseQuery.softDeletePredicate()` para mantener los 3 call
sites coherentes. Nuevo `Query[T].Restore(entity)` que limpia `deleted_at`
con guard `AND deleted_at IS NOT NULL` (un Restore sobre fila live es 0-row
no-op, no stealth NULL write). Tenant predicate se preserva en Restore.

Cobertura: `testSoftDeleteScopes` (`soft_delete_scope_test.go`) wired a
`SharedSuite`. 7 subtests: DefaultScopeHidesTrashed, WithTrashedReturnsAll,
UnscopedAliasOfWithTrashed, OnlyTrashedReturnsTrashed, CountRespectsScopes
(con los 3 modos), RestoreUntrashesARow, RestoreOnLiveRowIsNoop.

Doc: `website/docs/guides/modeling.mdx` § Soft Deletes reescrito con tabla
de modifiers + sección Restore. CHANGELOG `### Added`.

### ~~F1-6 · Optimistic locking~~

**Cerrado** — tag `quark:"version"` en un campo numérico activa el lock.
`buildUpdate`/`UpdateFields`/`Tracked.Save` añaden `version = version + 1`
en SET y `AND version = <loaded>` en WHERE; rows-affected==0 retorna
`ErrStaleEntity` (sentinel nuevo en `errors.go`). Tras éxito se bumpea la
versión del struct en memoria. La columna queda automáticamente NOT NULL.
Solo un campo puede llevar el tag.

`Tracked.Save` sigue siendo no-op si no hay cambios de columnas: la versión
sólo bumpea cuando ya hay otra escritura — la actualización del lock va en
la misma UPDATE, no en una segunda.

Cobertura: `testOptimisticLocking` (`optimistic_locking_test.go`) wired a
`SharedSuite`. 6 subtests: UpdateBumpsVersion, StaleUpdateReturnsErrStaleEntity
(dos lectores, segundo escritor falla), UpdateFieldsBumpsVersion,
UpdateFieldsStaleReturnsErrStaleEntity, TrackedSaveBumpsVersion (incluye
re-save no-op), TrackedSaveStaleReturnsErrStaleEntity. Doc:
`website/docs/guides/modeling.mdx` § Optimistic Locking;
`website/docs/reference/api/errors.mdx`; CHANGELOG `### Added`.

---

## Bugs P0 (cerrados — historial)

### ~~P0-1 · `Or()` no propaga `tenantID` → fuga de aislamiento entre tenants~~

**Cerrado** — fix mediante `BaseQuery.cloneForGroup()` (interno) que propaga
`tenantID/tenantCol/schema/cache/limit/offset/hasLimit/unscoped` al blank
recibido por el callback de `Or()` y pre-inyecta el predicado de tenant en su
`where`. Esto cierra la fuga por precedencia SQL (`A AND B OR C` ≡ `(A AND B) OR C`)
con doble inyección intencional (en `client.go:For[T]` para el outer y en
`cloneForGroup` para los OR groups). Regresión cubierta por `testOrRLSLeak` en
`tenant_router_test.go` (subtests `FlatOrRespectsTenant` / `NestedOrRespectsTenant` /
`OtherTenantUnaffected`), wired into `SharedSuite` para los 6 motores. Doc:
`CHANGELOG.md` bajo `[Unreleased] / ### Security`; nota en
`website/docs/advanced/multi-tenant.mdx` sobre la garantía de aislamiento en `Or()`.

### ~~P0-2 · `WhereJSON` concatena el path con `fmt.Sprintf` sin escapar~~

**Cerrado** — defense-in-depth en dos capas:

1. **Bind del path** en cada dialecto. `Dialect.JSONExtract` cambió a
   `(column, path string) (sql string, args []any, err error)`. PG usa
   `jsonb_extract_path_text(col, ?, ?, …)` con un bind por segmento del path;
   MySQL/MariaDB/SQLite/MSSQL/Oracle usan `JSON_EXTRACT`/`JSON_VALUE(col, ?)`
   con `$.path` bound. SQL fragment usa `?` neutral; `query_exec.go:substitutePathMarkers`
   lo traduce al placeholder de cada motor en build time.
2. **`internal/guard.ValidateJSONPath`** — regex `^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$`,
   max 256 chars. Cada `JSONExtract` la llama antes del bind.

Decisión: leading `$` rechazado en la API (path es `user.name` style, no
`$.user.name`). Razón: API uniforme, sin obligar a conocer la sintaxis interna
de cada motor.

Sentinel: `ErrInvalidJSONPath` (nuevo en `errors.go`).

**Breaking**: dialectos custom registrados vía `RegisterDialect` deben
actualizar la firma de `JSONExtract`.

Regresión: `testJSONPathSecurity` en `json_path_security_test.go` wired a
`SharedSuite` (6 motores). Cubre path bound, dotted bound, y 8 vectores de
inyección. Unit tests adicionales en `internal/guard/guard_test.go`.

Docs: CHANGELOG `### Security` + `### Changed`; `website/docs/guides/querying.mdx`
sección "JSON Predicates" con la grammar y la garantía de bind; Historial en
`docs/playbooks/security.md` y `docs/playbooks/dialects.md`.

### ~~P0-3 · `linkM2M` traga errores silenciosamente~~

**Cerrado** — helper `isUniqueViolation(err)` en `db_errors.go` que usa
`errors.As` contra los tipos de los 6 drivers (PG `*pgconn.PgError` SQLSTATE
23505, MySQL `*mysql.MySQLError` 1062, MSSQL `mssql.Error` 2627/2601, Oracle
`*network.OracleError` ErrCode 1, SQLite extended codes 2067/1555 en mattn y
modernc). `linkM2M` retorna `nil` sólo cuando matchea, propaga el resto envuelto
en `wrapDBError`. Cobertura: `testM2MLinkErrors` en `m2m_link_test.go` wired a
`SharedSuite` — subtests `IdempotentRelink` (re-save mismo (book, author) sin
duplicar la fila join) y `MissingJoinTablePropagates` (drop tabla join + Update
debe devolver error, no nil). Doc en `website/docs/guides/relations.mdx`
sección "Idempotent linking"; CHANGELOG `### Fixed`; Historial en
`docs/playbooks/query-builder.md`.

### ~~P0-4 · `isZeroValue` impide `Update` con valores cero (false / 0 / "")~~

**Mitigado** — el comportamiento de `Update(entity)` saltarse zeros sigue por
diseño (dirty tracking llega en Fase 1), pero ahora hay tres salidas
explícitas para no quedarse sin escribir ceros:

1. Nueva API `UpdateFields(entity, fields ...string)` en `query_crud.go` que
   ignora `isZeroValue` y escribe sólo los campos nombrados. Rechaza lista
   vacía, unknown field y la PK. Hooks Before/After siguen corriendo.
2. `Update(entity)` ahora loguea WARN listando los campos zero-value que se
   está saltando — la trampa deja de ser silenciosa.
3. `website/docs/reference/api/crud.mdx` tiene un admonition `:::caution
   Zero-value trap (P0-4):::` y una sección nueva `## UpdateFields` con
   tabla de reglas y ejemplo.

Cobertura: `testUpdateZeroValues` en `update_zero_values_test.go` wired a
`SharedSuite`. 6 subtests:
- `UpdateSkipsZerosByDesign` documenta el comportamiento actual de Update.
- `UpdateFieldsWritesZeroBool` verifica `false` se escribe.
- `UpdateFieldsWritesZeroIntAndEmptyString` verifica `0` y `""`.
- `UpdateFieldsRejectsUnknownField`, `UpdateFieldsRefusesToOverwritePK`,
  `UpdateFieldsRejectsEmptyList` cubren los errores del builder.

Doc CHANGELOG `### Added` (`UpdateFields`) + `### Changed` (Update WARN).
Historial en `docs/playbooks/query-builder.md`.

**Cierre permanente**: dirty tracking ligero en Fase 1 (Track() + snapshot al
cargar + Save() que sólo emite UPDATE de campos cambiados).
- **Doc**: warning en `website/docs/crud/update.md` y entrada en CHANGELOG.

### ~~P0-5 · `JOIN ON` se concatena al SQL sin pasar por el guard~~

**Cerrado** (fase deprecation; reemplazo definitivo con AST en v0.4).

- `internal/guard.ValidateJoinOn` valida la grammar identifier-only:
  `[ident.]ident OP [ident.]ident ((AND|OR) [ident.]ident OP [ident.]ident)*`
  con operadores `=`, `!=`, `<>`, `<`, `<=`, `>`, `>=` y max 512 chars.
- Wired en `query_exec.go:buildSelect` y `Count` antes de concatenar
  `j.onClause`. Path inválido devuelve `ErrInvalidJoin` (sentinel nuevo en
  `errors.go`) sin ejecutar SQL.
- `Join`, `LeftJoin`, `RightJoin` marcados `// Deprecated:` en godoc; remplazo
  programado para v0.4 con builder estructurado `Join(table).On(col, op, otherCol)`
  (Fase 2 AST).

Cobertura:
- Unit tests en `internal/guard/guard_test.go`: `TestValidateJoinOn_Valid` (12
  casos, incluido lowercase AND/OR + multi-condición), `TestValidateJoinOn_Invalid`
  (18 casos: `;`, `--`, `/*`, literales, function calls, paréntesis, UNION,
  operadores junk, identifiers con dash o leading `$`, three-segment idents,
  double dot, missing operator/lhs/rhs), `TestValidateJoinOn_BoundMethod`.
- Regresión en `join_on_security_test.go` wired a `SharedSuite`. 4 subtests:
  `ValidJoinExecutes`, `ValidMultiConditionJoinExecutes`,
  `InjectionAttemptRejected` (table-driven sobre 8 vectores con
  `errors.Is(err, ErrInvalidJoin)`), `InjectionAttemptRejectedInCount` (cubre
  el path Count() que construye su propio JOIN SQL).

Docs: CHANGELOG `### Security` + `### Added` (sentinel); MIGRATION_v0.2.0
sección de deprecation con tabla de accepted/rejected y migration steps;
nota en `website/docs/guides/querying.mdx` sección "Joins" con la grammar
y la deprecation; `website/docs/reference/api/errors.mdx` actualizado con
el nuevo sentinel; Historial en `docs/playbooks/security.md` y
`docs/playbooks/query-builder.md`.

---

## Limpieza de Fase 0 (no son bugs P0 pero bloquean credibilidad pública)

### F0-1 · Reconciliar versionado público

- **Estado actual**: `RELEASE_NOTES_V1.md` anuncia v1.0.0; `CHANGELOG.md` sólo tiene 0.1.0/0.1.1; `SECURITY.md` dice "pre-1.0"; README dice "v0.x"; ROADMAP marca features de v0.2 como "Completed" sin tag v0.2.
- **Acciones**:
  1. Renombrar `docs/RELEASE_NOTES_V1.md` → `docs/RELEASE_NOTES_v0.2.md` (texto sin marketing, lista honesta de cambios desde 0.1.1).
  2. Actualizar `CHANGELOG.md` con la entrada `[0.2.0] - 2026-MM-DD` que consolide todo lo entre 0.1.1 y hoy.
  3. Alinear `README.md`: badge de versión, snippets que digan "v0.2".
  4. Alinear `SECURITY.md`: cambiar "pre-1.0" por "v0.x — supported only on `main`".
  5. Sincronizar `docs/ROADMAP.md` con el plan de fases de `docs/ANALISIS_MADUREZ.md` §4.
- **Done**: taggear `v0.2.0` en git. Acción de release publica el sitio versionado.

### F0-2 · Crear `examples/blog-api/` o eliminar las menciones

- **Estado**: README enlaza dos veces a `examples/blog-api/` (sección de demo y "go run"). El directorio no existe.
- **Acción recomendada**: crear el ejemplo. Es una buena demo de multi-tenancy + relaciones + migraciones. Si no hay tiempo, eliminar las dos menciones del README.
- **Done**: `cd examples/blog-api && go run main.go` arranca un servidor HTTP de ejemplo, o las menciones desaparecen.

### F0-3 · Corregir paths en `examples/README.md`

- **Estado**: `examples/README.md` instruye `go run pkg/quark/examples/sqlite/main.go` (path heredado de monorepo previo). La ruta real es `examples/sqlite/main.go`.
- **Acción**: reemplazar todos los `pkg/quark/` → ``.
- **Done**: cada comando `go run` del README funciona desde la raíz del repo.

### F0-4 · Consolidar Quick Start duplicado en README

- **Estado**: README tiene dos Quick Starts (líneas ~34-92 y ~164-222). Copy-paste error.
- **Acción**: dejar uno solo, el más actualizado, en posición tras la sección de "Why Quark".
- **Done**: una sola sección Quick Start; lectura lineal sin duplicados.

### F0-5 · Reemplazar badge de coverage hardcoded

- **Estado**: README muestra "Coverage 87%" como badge estático.
- **Acción**: configurar codecov o usar `go tool cover` artifact en CI; badge dinámico que enlace al reporte real. Aceptable interim: eliminar el badge hasta que sea real.
- **Done**: el porcentaje del badge se corresponde con `go test -coverprofile`.

---

## Setup de infraestructura (Fase 0, requerido para Fase 1+)

### F0-6 · Pipeline de publicación de `website/` a `quark-docs`

- **Objetivo**: cada release de Quark publica el sitio Docusaurus al repo `jcsvwinston/quark-docs` rama `gh-pages`. URL pública (`jcsvwinston.github.io/quark-docs/`) intacta.
- **Acción**:
  1. En `website/docusaurus.config.ts`: confirmar `baseUrl: '/quark-docs/'`, `organizationName: 'jcsvwinston'`, `projectName: 'quark-docs'`, `deploymentBranch: 'gh-pages'`.
  2. Generar PAT con scope `repo` para push a `quark-docs` y guardarlo como secret `DOCS_DEPLOY_TOKEN` en el repo de Quark.
  3. Crear `.github/workflows/deploy-docs.yml` que en push a tag `v*` builda `website/` y pushea `website/build/` a `quark-docs:gh-pages`.
  4. Archivar el repo `quark-docs` como read-only para fuente; sólo `gh-pages` queda activa.
- **Done**: hacer un tag de prueba `v0.2.0-rc1`, verificar que el sitio se actualiza sin intervención.

### F0-7 · Inicializar versioning de Docusaurus

- **Objetivo**: que `website/versioned_docs/` exista con el snapshot inicial de la versión actual.
- **Acción**: `cd website && npm run docusaurus docs:version 0.2.0`. Commit del directorio generado.
- **Done**: `versions.json` lista `["0.2.0"]`. Sitio sirve `/docs/` (next) y `/docs/0.2.0/`.

### F0-8 · Setup testcontainers-go para los 6 motores

- **Objetivo**: que `go test ./...` arranque containers de Postgres, MySQL, MariaDB, MSSQL, Oracle (XE) por sí solo. Eliminar los `t.Skip` por env var.
- **Acción**:
  1. Añadir dependencia `github.com/testcontainers/testcontainers-go` y los módulos de cada motor.
  2. Refactorizar `*_suite_test.go` por motor: helper `setupContainer(t)` que devuelve DSN, sin env vars.
  3. Build tag `//go:build integration` para tests caros; default rápido sigue siendo SQLite.
  4. CI matrix con job por motor.
- **Done**: `go test -tags=integration ./...` levanta los 6 motores y corre el suite completo. CI verde con matriz.

### F0-9 · Instalar `release-please` o `semantic-release`

- **Objetivo**: automatizar bump de versión + CHANGELOG desde Conventional Commits.
- **Acción**: añadir `.github/workflows/release-please.yml`. Configurar release type `go` (single-module).
- **Done**: tras un merge a `main` con commits `feat:`/`fix:`, aparece un PR de release automático con CHANGELOG y version bump.

### F0-10 · Linter de docs

- **Objetivo**: detectar drift entre código y docs. Bash o Go script en CI.
- **Checks mínimos**:
  - Cada feature listada en `ROADMAP.md` como "Completed" debe tener entrada en `CHANGELOG.md`.
  - No debe haber referencias a `RELEASE_NOTES_V1` cuando la última tag no es v1.
  - Enlaces internos en `docs/**/*.md` y `website/docs/**/*.md` no deben estar rotos.
  - Cualquier API pública nueva (`go doc`) debe tener su página en `website/docs/`.
- **Done**: CI rojo si alguno falla; verde tras corregir.

---

## Cómo cerrar un item

1. Crear branch `fix/p0-1-or-rls-leak` (o lo que aplique).
2. Implementar fix + test de regresión.
3. Correr `go test -tags=integration ./...` localmente con los 6 motores.
4. Invocar el subagente `code-reviewer` antes del PR.
5. PR con título Conventional Commit (`fix(query): propagate tenant context in Or() clauses`).
6. Verificar que `code-reviewer` aprueba, CI verde en los 6 motores, CHANGELOG actualizado.
7. Mergear con squash.
8. Marcar el item como `~~tachado~~` en este archivo o borrar la sección.

## Cuándo pasar a Fase 1

Cuando este archivo se queda con secciones tachadas y los puntos de **Setup de infraestructura** estén verdes en CI. Antes no.
