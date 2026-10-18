---
type: playbook
module: query-builder
files:
  - query_builder.go
  - query_exec.go
  - query_crud.go
  - page.go
  - cursor.go
last_review: 2026-05-10
related_adrs: [0001, 0002, 0007]
related_p0: []
closed_p0: [P0-1, P0-3, P0-4, P0-5]
phase: 1
---

# Playbook: Query Builder

## Qué es y qué no es

Quark tiene un **query builder reflect-based con clones inmutables**, no un AST componible. `Query[T]` lleva un `BaseQuery` con slices de `condition`, `join`, `orderBy`, etc. Cada método (`Where`, `Join`, `Limit`) clona el query y devuelve uno nuevo. Los generics tipan T pero el núcleo opera con `reflect.Value`.

**Lo que SÍ se puede expresar** (toda la superficie la ejerce el exerciser `builder-advanced` del superapp): WHERE/IN/BETWEEN/NOT/JSON/Or + el AST componible `WhereExpr`/`HavingExpr` (Col/Lit/Eq/And/Or/Not/In/Func), Joins (Inner/Left/Right) con On/OnRaw, GroupBy+Having(Aggregate/Expr), Distinct, Select/SelectExpr, OrderBy, Limit/Offset, Apply(scopes), agregados Sum/Avg/Min/Max, Count, Find, First, List, Iter, Cursor, Paginate, Preload, scopes de soft-delete (WithTrashed/OnlyTrashed/Unscoped/Restore/HardDelete), CTEs `With`/`WithRecursive`, set-ops `Union`/`UnionAll`/`Intersect`/`Except` (cobertura por motor en §dialects), window functions (`SelectExpr`+`Over`/`RowNumber`/`NewWindow`), locking pesimista `ForUpdate`/`ForShare`/`SkipLocked`/`NoWait` (por capability), `Upsert`/`UpsertBatch`, y el CRUD por lotes (`CreateBatch`/`UpdateBatch`/`DeleteBatch`/`DeleteBy`).

**Lo que NO se puede expresar**: subqueries componibles tipadas (sólo hay un `WhereSubquery` raw gateado por `AllowRawQueries`) y nested preload (`Orders.Items` no es expresable; sólo `Orders` plano). El AST de predicados `WhereExpr`/`HavingExpr` cubre el grueso de la composición; lo que falta vive en `Raw()`/`RawQuery` con flag de seguridad.

## Bugs P0 vivos

(ninguno; ver § Historial.)

## Historial — bugs cerrados

### P0-5 · `JOIN ... ON` se concatenaba raw (cerrado, fase deprecation)

`Query[T].Join`/`LeftJoin`/`RightJoin` aceptaban un `on` string-raw que iba al
SQL final sin pasar por el guard. Asimétrico con `WHERE col`. Detalles del fix
en `docs/playbooks/security.md` § Historial — incluye la grammar aceptada,
los call sites tocados (`query_exec.go:buildSelect` y `Count`), el sentinel
`ErrInvalidJoin` (`errors.go`), y la deprecation programada para v0.4 cuando
llegue el builder estructurado `Join(table).On(col, op, otherCol)`.

### P0-1 · `Or()` no propagaba `tenantID/tenantCol/schema/cache/limits` (cerrado)

`Query[T].Or` construía un `BaseQuery` blanco hardcoded; el grupo OR escapaba
el predicado de tenant por precedencia SQL. Fix: `(b *BaseQuery) cloneForGroup()`
copia el contexto de aislamiento al blank y pre-inyecta el predicado de tenant
para que el grupo OR lo herede. Detalles en `docs/playbooks/tenant.md`.

### P0-4 · `isZeroValue` impedía escribir `false`/`0`/`""` en `Update` (cerrado en Fase 1)

`Update(entity)` sigue saltándose zero-values por diseño (mantiene el comportamiento previo) y ahora hay dos salidas explícitas:

1. **`UpdateFields(entity, fields...)`** — la API explícita-por-campo añadida en P0-4 (Fase 0). Apropiada cuando sabes qué columnas quieres tocar.
2. **`Query[T].Track().Find(id)` → `tracked.Save(ctx)`** — la API basada en snapshot añadida en Fase 1 (`dirty_track.go`). Apropiada cuando quieres mutar el struct libremente y dejar que Quark calcule el diff.

`Tracked.Save` cierra la herida P0-4 sin pedir Unit-of-Work completo: la comparación es snapshot-vs-current, así que `false`, `0`, `""` se escriben cuando el valor cambió. La snapshot vive en el wrapper, no en un identity map global — cero memoria compartida, cero GC pressure.

#### Patrón Fase 0 (mitigación; sigue siendo API válida)

`UpdateFields(entity, fields ...string) (int64, error)` en `query_crud.go`
ignora el filtro `isZeroValue` y escribe sólo los campos nombrados. Rechaza
listas vacías, nombres desconocidos, y la PK. Útil cuando ya sabes qué
columnas tocar y no necesitas el snapshot. `Update(entity)` también loguea
WARN listando los campos zero-value que está saltando, para que la trampa
sea visible en runtime.

Cobertura conjunta:
- `testUpdateZeroValues` (`quarkdriver/drivertest/suite/update_zero_values.go`,
  en la suite de motor pública desde A11 Q4) — 6 subtests para
  `UpdateFields` y la trampa de `Update`.
- `testDirtyTracking` (`quarkdriver/drivertest/suite/dirty_track.go`) — 5 subtests para `Track()` +
  `Tracked.Save`: writes-zero-when-changed, no-change-no-SQL, snapshot
  refresh, list-returns-tracked-slice, PK-never-mutated.

`UpdateFields` y `Tracked.Save` coexisten — uno es explícito-por-campo,
el otro es snapshot-driven; cada cual sirve casos distintos.

### P0-3 · `linkM2M` swallowed every driver error (cerrado)

`query_crud.go:linkM2M` retornaba `nil` ante cualquier error del INSERT en la
join table, no sólo ante duplicados. El comentario decía "Ignore duplicate
key errors" pero el código ignoraba todo: FK violations, missing tables,
conexiones rotas. Fix: helper `isUniqueViolation(err)` en `db_errors.go` que
hace `errors.As` contra los tipos de error de los 6 drivers (PG `*pgconn.PgError`
SQLSTATE 23505, MySQL `*mysql.MySQLError` 1062, MSSQL `mssql.Error` 2627/2601,
Oracle `*network.OracleError` ErrCode 1, SQLite extended codes 2067/1555 en
ambos drivers mattn y modernc). `linkM2M` ahora retorna `nil` sólo si el error
es unique violation; cualquier otro error se envuelve con `wrapDBError` y se
propaga. Cobertura: `testM2MLinkErrors` (idempotent re-link + missing-table
propagation).

**Anti-pattern a evitar al añadir Save-flow code nuevo**: cualquier `if err
!= nil { return nil }` en una rama "ignore X" debe discriminar el error por
tipo/código, nunca por su mera presencia.

## Anti-patterns a vigilar

### `fmt.Sprintf` con valores no validados

Cualquier vez que metas `fmt.Sprintf` en la generación de SQL final, los valores deben venir de:
- `dialect.Quote(identifier)` para identifiers ya validados, o
- bind params (`?`/`$N`/`@pN`/`:N` según dialecto, vía `dialect.Placeholder(n)`).

**Nunca** concatenes valores de usuario a la string SQL. El bug P0-2 (`WhereJSON` con path no escapado) es un ejemplo de qué pasa cuando se ignora esto.

### Reflect adicional en hot path

`scanRow` (`query_exec.go:676-717`), `executeQuery` y `loadRelations` ya pagan reflect por columna y por fila. **No introduzcas más reflect en el bucle de scan o de load.** Si tu cambio requiere acceso adicional a fields, cachéalo en `ModelMeta` (ver `internal/schema/schema.go`) durante la primera resolución y reúsalo.

ADR 0002 prohíbe reflect adicional en hot paths sin discusión previa.

### Asserting a caller's value to a concrete type

`Where(col, op, value any)` hands the builder whatever the caller had at hand.
The IN / NOT IN / BETWEEN operand used to be read with `cond.value.([]any)` and
no `ok`, so `Where("id", "IN", []string{...})` ended the goroutine with a panic
(QK-33, closed in A11 Q1). The operand now goes through `listOperand`
(`query_exec.go`): `[]any` keeps its assertion, any other slice or array is read
by reflection — only on that path, which used to panic — and a byte slice, a
scalar or `nil` is refused with `ErrInvalidQuery`. **Never assert a value that
came from the caller without `ok`;** refuse what is not the expected shape with
`ErrInvalidQuery` instead. Regression: `in_operand_test.go` (root) and
`internal/enginesuite/in_typed_slices_test.go` (six engines).

### A second renderer for a caller's conditions

Before QK-39 the write paths — `hardDeleteWhere` (DeleteBy), `buildUpdateMap`
(UpdateMap), `UpdateFields` and the merge in `buildUpdate` — and the
PreloadWhere loader rendered `q.where` themselves, as `col OP ?` joined by
AND. That renderer ignored `cond.logic`, so **WhereNot lost its NOT** and
`WhereNot("status", "=", "active").DeleteBy()` deleted the active rows; Or
groups, IN, BETWEEN, IS NULL and WhereExpr failed there. Every caller
condition now goes through `buildWhereClause` (writes via `whereForWrite`,
which parenthesises the fragment so an Or group cannot escape the key
predicate). **Never render `q.where` anywhere else.** Regression:
`write_where_test.go` (root: each write path touches exactly the rows a
SELECT returns, and those are the rows a Go predicate names) and
`internal/enginesuite/write_where_test.go` (`WriteWhereParity`, six engines).

### Writes by key AND the caller's conditions with the key

Before QK-40, `Update(entity)` (via `saveAny`), `UpdateBatch`, `Delete`,
`HardDelete`, `DeleteBatch` and `Restore` wrote by the key alone: the query
each one built for the row carried none of the caller's conditions — and
none of the tenant predicate RowLevelSecurityClient injects — so an id from
a request wrote another tenant's row. Each now ANDs `whereForWrite` with its
key predicate; `saveAny` takes the condition list as a parameter (the query's
own for the entity Update was called with, `tenantScopeFor` for an
association, nil for an insert) so the caller's conditions never reach a
related table.

When no row with the key satisfies the conditions, the write reports (0, nil)
and runs no After hook, audit row or event. Two places need a probe
(`keyPassesWhere`, a `SELECT COUNT(*)` by key and conditions on the primary):
a guarded UPDATE that affected zero rows — on a versioned model that is
`ErrStaleEntity` only when the row passes the conditions, and on MySQL and
MariaDB rows-affected counts changed rows, so "matched, nothing changed"
must not read as "excluded" — and an Update with a loaded belongs_to, which
is written before the entity's row. The probe sees only rows the conditions
let the caller see, so it cannot tell another tenant's row from a missing
one. Regression: `where_guard_test.go` and
`internal/enginesuite/where_guard_test.go` (`WhereGuards`).

### `UpdateBatch` and the version column (QK-45)

`buildUpdate` puts the version predicate and `version = version + 1` into
every row's UPDATE, so `UpdateBatch` on a versioned model has always sent
them — and, until QK-45, never read the count: a stale row was skipped in
silence, a missing key too, and the written rows' in-memory versions stayed
behind, so the next `Update` of the same struct returned a false
`ErrStaleEntity`. The owner's decision (2026-10-06): an opt-in in v1,
`CheckVersions()` (field `checkVersions` on `BaseQuery`, which records the
call and changes nothing now), and the default since 2.0
(`docs/deprecations/DEP-2026-003-updatebatch-version-check-default.md`).
`UpdateBatch` reads each row's count on a versioned model: zero is skipped
when the query has conditions and `keyPassesWhere` says they exclude the
key, stale otherwise; the stale rows are collected and `errors.Join`ed,
which makes `atomically` roll the whole batch back (a savepoint inside a
`ForTx` query); the written rows' `bumpVersion` runs only after `atomically`
returns nil, and the list is reset at the start of the closure because
`Client.Tx` re-runs it after a deadlock. The SET bumps the version, so a
matched row is a changed row on MySQL and MariaDB too — no "matched, nothing
changed" case. `TestUpdateBatchChecksVersionsByDefault` and
`UpdateBatchVersions/ChecksVersionsByDefault` hold the default; `OPS-04` of
the enterprise bench measures it.

### The other writes of a versioned model (QK-58, DEP-2026-004)

Every write that carries an entity is inside optimistic locking. `Delete`,
`HardDelete`, the soft delete and `Restore` render `AND <version> = ?` with
the entity's version (`versionPredicate`, `query_crud.go`), the soft delete
and `Restore` add `, <version> = <version> + 1` (`versionBump`) and
`bumpVersion` the entity on success; zero rows on a versioned model is
`ErrStaleEntity` unless `keyPassesWhereExtra` says the row is one the
statement could not have matched — excluded by the conditions, already
trashed (`deleted_at IS NULL`), or live for `Restore` (`deleted_at IS NOT
NULL`) — which is `(0, nil)` (`deleteMissed`). Without conditions a hard
delete of a missing key is the conflict it is for `Update`, with no probe.
`UpdateMap` has no entity: `buildUpdateMap` appends the bump so a holder of
the row sees the write, unless the map names the version column
(`mapNamesColumn`), in which case the map's value is written and no bump is
added — the notice's v1 exit (`Where("version", "=", v).UpdateMap(map{…,
"version": v + 1})`) keeps working; the notice's proposal to refuse such a
map with `ErrInvalidQuery` was NOT applied at the 2.0 dry run because it
contradicted that exit. The update branch
of `Upsert`/`UpsertBatch` holds the row to the entity's version when it is
not zero and bumps it: `conflictClause` renders `WHERE (excluded.<v> = 0 OR
<table>.<v> = excluded.<v>)` on PostgreSQL/SQLite and `IF(<conflict match>
AND (VALUES(<v>) = 0 OR <v> = VALUES(<v>)), …)` with the bump as the LAST
assignment on MySQL/MariaDB (earlier IFs read the original version);
`mergeMatchedClause` adds `(src.<v> = 0 OR target.<v> = src.<v>)` and
`target.<v> = target.<v> + 1` on the MERGE engines. No bind: the INSERT
carries the entity's version. `upsertNotWritten` turns a changed-nothing
statement on a row at another version into `ErrStaleEntity`. The entity's
version follows the row: `RETURNING <pk>, <v>` on PostgreSQL/SQLite
(`upsertReturning`/`scanUpsertReturning`), bumped from the carried version
on MySQL/MariaDB when the statement reports an update (rows affected 2), and
read by key otherwise (`syncVersion`: a zero-version entity on MySQL, every
MERGE — which reports its key but not whether it inserted or updated). A
versioned `UpsertBatch` with `updateCols` goes row by row on every engine
inside a transaction, so the stale row is named and nothing stays written.
Regression: `optimistic_locking_scope_test.go`,
`TestVersionedWritesTellAConflictFromAnExclusion` (Delete, HardDelete) and
`internal/enginesuite/upsert_versioned_test.go` (`UpsertVersioned`, six
engines). `OPS-04` of the enterprise bench records **present**.

### Reads by key stay inside the tenant

Before QK-42, `Find(id)` assigned `q.where = [key]` on its receiver: the
tenant predicate went with the caller's conditions, so `Find` under
RowLevelSecurityClient read another tenant's row by id, and the same query
object listed without its tenant afterwards. `Find` now appends the key to
the conditions for the call only, and `First` puts its limit back when it
returns — restored rather than set on a copy, because a copy of the query is
a 632-byte allocation on every `Find`, and the engine bench (PG-02, MY-01)
holds Find's allocations to 5 % of the record. `ensureTenantID`
stamps the resolved tenant on every insert and update by entity instead of
filling an empty field only — an entity carrying another tenant's id was
inserted under it, and an Update moved the row. A foreign value is replaced
and logged (`quark.tenant.foreign_value_replaced`). `UpdateMap` still writes
its map verbatim (until QK-44). The A8 bench control `RLS-03` reads all four direct paths
positively and records **present**.

### Upserts and map updates stay inside the tenant

Before QK-43 the update branch of `Upsert`/`UpsertBatch` carried no tenant
predicate on any engine: tenant A upserting a key tenant B held rewrote B's
row, and on SQL Server and Oracle an empty `updateCols` ("every non-conflict
column") also wrote A's id into B's tenant column. `upsert_tenant.go` builds
the guarded branch per family (`upsertFamily`: `on_conflict`,
`duplicate_key`, `merge`); a dialect outside them refuses under
RowLevelSecurityClient (`checkTenantUpsert`). A conflict with another
tenant's row is reported as `ErrConstraintViolation`: PostgreSQL/SQLite see
no RETURNING row or zero rows affected, SQL Server/Oracle zero rows from the
MERGE, and MySQL/MariaDB — whose zero also means "own row already held the
values" — end the clause with `<pk> = IF(<tenant> = ?, LAST_INSERT_ID(<pk>),
<pk>)`, so the statement reports the tenant's row it met and no key (0) for
another tenant's (QK-63, `duplicateKeyKeyAssignment`); a statement that
changed nothing reads the row back by conflict key (`upsertNotWritten`,
`upsert_conflict.go`), which tells the tenant's own unchanged row from
another tenant's. RETURNING is not used there under the guard because
MariaDB would hand back the other tenant's id. `UpsertBatch` runs in a
transaction under the guard, and on every engine that upserts row by row.
QK-44: `UpdateMap` passes its map through `confineTenantColumn`. Regression:
`upsert_tenant_test.go` and `internal/enginesuite/upsert_tenant_test.go`
(`UpsertTenant`, six engines).

### An empty `updateCols` is one behaviour (QK-48, QK-59, DEP-2026-006)

`Upsert`/`UpsertBatch` with no `updateCols` is insert-or-ignore on the six
engines: PostgreSQL and SQLite write `ON CONFLICT … DO NOTHING`, MySQL and
MariaDB `ON DUPLICATE KEY UPDATE <first conflict col> = <it>`, and the
MERGE of SQL Server and Oracle (`buildMerge`, `upsertBatchMSSQLBulk`) has no
`WHEN MATCHED` branch (`hasUpdate` is false). The conflicting row stays as
it was, the call returns nil, and no key is written back. In v1 the MERGE
inferred an update set — every column the insert writes except the conflict
columns, the key and `created_at` (`mergeInferredUpdateCols`, gone) — and
`internal/enginesuite/upsert_empty_update_cols_test.go`
(`UpsertEmptyUpdateCols`) pinned each engine; it holds the six to the same
now. What QK-48 fixed within v1: on PostgreSQL and SQLite the RETURNING after
DO NOTHING had no row and `Upsert` returned `sql.ErrNoRows` — the other four
answered nil.

### A duplicate of another unique key is `ErrConstraintViolation` (QK-61, QK-65, DEP-2026-006)

The conflict columns name the key `Upsert` arbitrates. ON CONFLICT names its
arbiter on PostgreSQL and SQLite, so a duplicate of another key — the
entity carries an existing row's primary key and a conflict value no row has
— is the engine's own error. ON DUPLICATE KEY UPDATE has no conflict target
and fires on a duplicate of any unique key: in v1 MySQL and MariaDB updated
the row holding the duplicate whichever key it was (QK-61 made the no-op
clause `c = c` so it no longer rewrote the conflict column). Now every
assignment is guarded by the conflict columns — `c = IF(<k> <=> VALUES(<k>)
[AND <tenant> = ?], VALUES(c), c)` (`mysqlConflictMatch`, in
`MySQLDialect.UpsertSQL` and `conflictClause`) — and a statement that
changed nothing reads the row back (`upsertNotWritten`): no row with the
conflict key means the duplicate was elsewhere, `ErrConstraintViolation`; a
row with the conflict key whose key is not the one the entity carries, while
a row holds the carried key, is the same error (MySQL met the carried key
first). The MERGE matches on the conflict columns only and its insert branch
does not write the identity key, so the same entity inserted a second row
with a key of its own: an entity carrying a non-zero integer key is checked
before the MERGE (`carriedKeyIsAnotherRow`) — no row with the conflict key
and a row with the carried key is the error; a row with the conflict key is
met by the MERGE whatever key the entity carried, as ON CONFLICT does. A key
of another type is written by the insert branch, where the engine refuses
the duplicate itself. `UpsertBatch` on MySQL and MariaDB goes row by row,
always: a multi-row statement reports one count for the whole batch and
cannot tell a row left alone from one written; the rows run inside a
transaction so the error leaves nothing written, on Oracle's per-row MERGE
too. QK-62 is moot without the inferred set. Regression:
`internal/enginesuite/upsert_engine_edges_test.go` (`UpsertEngineEdges`, six
engines) and `upsert_engine_edges_test.go`.

### The key an upsert writes into the entity (QK-63, QK-64)

Rule: with a single integer key, the entity ends with the key of the row the
statement inserted or updated, read from THAT statement; where the statement
does not say, the key is left as it was; never another row's
(`upsert_key.go`). QK-63: MySQL read `SELECT LAST_INSERT_ID()` as a second
statement on whichever pooled connection came up — per-connection, and on the
update branch the connection's last generated key, so an upsert that met row
1 wrote 2. Now `res.LastInsertId()` of the INSERT itself, with
`, <pk> = LAST_INSERT_ID(<pk>)` appended LAST to the ON DUPLICATE KEY UPDATE
clause (it sees a key an `updateCols` rewrote; it changes no value, so rows
affected stay 1/2/0). With a carried key and rows affected 1 nothing is read:
the insert wrote the carried key, and the insert id names the AUTO_INCREMENT
column. A dialect outside the `duplicate_key` family without RETURNING gets
no key (the old fallback ran the same second-statement read). MariaDB keeps
RETURNING, which answers the conflicting row, measured. QK-64: on SQL Server
and Oracle the key is an identity the MERGE's insert branch does not write,
so a carried non-zero key stayed on the entity while the row got another.
`mergeRowKeys` reads the key back whatever the entity carried: SQL Server
`OUTPUT INSERTED.<pk> INTO @quark_keys` + `SELECT` (an OUTPUT without INTO
is Msg 334 on a table with a trigger), Oracle a PL/SQL block with
`RETURNING <pk> BULK COLLECT INTO` (scalar INTO fails on two rows; measured
on 23.4, 23.5 and 23.26 — the MERGE quark writes already needs 23ai for its
`SELECT` without `FROM`). Zero rows back = nothing written (no update
branch, or another tenant's row: the guard's error); more than one = key
unknown, left. `UpsertBatch` on SQL Server numbers its source rows
(`quark_ord`) as CreateBatch does and writes each key; Oracle per row; the
multi-row INSERT of the other four writes none. Regression:
`internal/enginesuite/upsert_key_test.go` (`UpsertKey`, six engines plus
the MySQL dialect over MariaDB) and `upsert_key_test.go`.

### Scopes AND with the caller's whole expression

The soft-delete filter and the tenant predicate are scopes. Before QK-41
they were prepended to `q.where`, and a top-level Or group escaped them:
`"deleted_at" IS NULL AND "name" = ? OR ("name" = ?)` returned the trashed
row. `scopedConditions` renders the scopes first and the caller's
conditions as one parenthesised group; the tenant condition is marked
`scope: true` where it is injected (`applyTenantConfinement`,
`cloneForGroup`). A query with no scope renders exactly as before. A bare
`Or(...)` with nothing before it now means its group alone, as it does
without scopes. **Render a statement's WHERE through `scopedConditions` (or
`whereForWrite`), never by prepending to `q.where`.**

### `List()` con resultado truncado silenciosamente

`List()` aplica un cap implícito de 100 filas si el caller no llamó a `Limit()` (`query_exec.go:149`). **Esto trunca sin error.** Si introduces una API similar (`AllWhere`, `FetchAll`), o expón el cap o devuelve error si se rebasa.

Para lectura masiva, usar `Iter()` o `Cursor()` (server-side iteration), no `List()`.

### Eager loading sin chunkear `IN(...)`

Oracle limita `IN` a 1000 elementos. MSSQL limita el número total de parámetros bind a ~2100. Si añades un nuevo `loadXxxRelation` (ej. para nested preload en Fase 2), **chunkea las parent keys** en bloques de 500 antes de emitir el SELECT. Patrón existente en `DeleteBatch` (`query_crud.go:1827`, loop sobre `batchChunkSize=1000`).

Hoy `loadStandardRelation`/`loadM2MRelation`/`loadPolymorphicRelation` (`query_exec.go:739-1065`) NO chunkean — es deuda. Cualquier preload masivo en Oracle va a romper.

### Bulk insert sin chunkear el número de bind-params

`CreateBatch` emite un `INSERT … VALUES (…), (…)` multi-fila. **El número de placeholders es `filas × columnas`**, no de elementos en un `IN(...)`: el techo relevante es el de bind-params del motor (MSSQL ~2100, SQLite 32766, PG/MySQL 65535), no el de `IN` de Oracle. Por eso `CreateBatch` chunkea con su propia constante **`maxBatchBindParams=2000`** (bajo el techo de MSSQL) en `rowsPerChunk = maxBatchBindParams / nºcolumnas` (`query_crud.go:1646` → helper `createBatchStmt:1753`), distinta de `batchChunkSize=1000` que cuenta elementos de un `IN`. El bug BB-10 fue precisamente que `CreateBatch` NO chunkeaba (a diferencia de `DeleteBatch`) y reventaba el techo de params en MSSQL a unos cientos de filas. **Patrón a seguir si añades otro bulk multi-row** (`UpsertBatch` sigue sin chunkear — deuda trackeada): el contexto con timeout se crea **una vez antes del loop de chunks** (timeout por operación, no por sentencia), igual que `DeleteBatch`.

Bifurcación por back-fill de PK (Finding G): cuando el PK es auto-generado, los dialectos sin `RETURNING` **no** usan el multi-row — no pueden leer la key generada de vuelta. Oracle ya iba per-row (IDENTITY incompatible con `INSERT … VALUES (…),(…)`); MySQL y MSSQL caen ahora a `createBatchBackfillPerRow` (insert per-row + back-fill vía `LastInsertId`/`SCOPE_IDENTITY`) para rellenar `entity.ID`. PKs provistos o compuestos mantienen el multi-row chunked. Si tocas este path, la regresión vive en `testBatchOps` (asierta PK≠0 distinto en los 6 motores, sin gate por `SupportsReturning`).

Since A12 Q3 (QK-36, ADR-0028) the per-row form is the fallback, not the rule: `createBatchBackfill` (`batch_backfill.go`) sends a chunk in one round trip only where the engine proves which key each row got — an insert-only `MERGE … OUTPUT quark_src.quark_ord, INSERTED.<pk>` on SQL Server (a plain `INSERT … OUTPUT` does not promise row order), and a multi-row `INSERT` with keys computed from `LAST_INSERT_ID()` and the session's `auto_increment_increment` on MySQL **only under `innodb_autoinc_lock_mode` 0/1** (MySQL 8's default, 2, keeps per-row). Both are gated by a catalog probe cached per client and table (`Client.batchIDsProvable`). A failed chunk is undone (SQL Server: the CATCH block's marker row; MySQL: its own transaction or a savepoint of the caller's) and re-run per-row, so the failure state is the per-row form's. Do not "simplify" either back to `OUTPUT INSERTED` + VALUES order or to LAST_INSERT_ID + consecutive keys without the lock-mode check: a wrong key is silent corruption. The regression is `TestCreateBatchKeysAllEngines` (internal/enginesuite), which runs MySQL twice (default lock mode and `=1`). Known pre-existing defect left as is: the per-row form on SQL Server reports a rejected row as a NULL scan of `SCOPE_IDENTITY()` instead of the engine's error (single `Create` scans a `NullInt64`; `backfillPerRow` does not).

### Comparabilidad de keys en M2M

`loadM2MRelation` indexa parent keys con `parentKeyMap[parentID]`. Si un PK es un struct (composite) o un slice, esto puede panic. Hoy se asume que las PKs son primitivos. Si introduces composite PKs en preload, asegúrate de que el mapa key es serializable (string + separador, o struct key con `comparable` constraint).

## Decisiones que afectan al módulo

- **ADR 0001 (Active Record)**: el query builder devuelve structs, no proxies. No hay lazy loading transparente; `Preload` es explícito.
- **ADR 0002 (Reflect default)**: el núcleo es reflect; codegen reemplazará paths internos en Fase 6 manteniendo la API.
- **ADR 0007 (Multi-tenancy)**: cualquier helper que clone `BaseQuery` debe propagar tenant. El bug P0-1 viola esto.

## Roadmap de mejora

- **Fase 1**: dirty tracking ligero (cierra P0-4 permanentemente), Soft delete con scope automático, optimistic locking (`quark:"version"` tag).
- **Fase 2**: AST de expresiones (`Expr`, `Col`, `Lit`, `Func`, `In`, `Exists`); subqueries tipadas; CTEs/WITH; window functions; UNION/INTERSECT; locking (`ForUpdate`, `SkipLocked`, `NoWait`); HAVING sobre agregados; nested preload; chunking automático de IN; `Or()` reescrito con AST que NO podrá tener el bug P0-1 por construcción.
- **Fase 6**: codegen reemplaza `scanRow`, `buildInsert`, `buildUpdate`, `loadRelations` con paths tipados sin reflect.

## Tests críticos a no romper

- `n_fixes_test.go` — bugs N1-N5 retroalimentados por auditoría externa (Oracle MERGE alias, INSERT ALL, MSSQL composite PK, ORA-01791, ORA-00979).
- `p0_fixes_test.go` — bugs P0 históricos (Paginate immutability, MaxWhereConditions, MaxJoins, etc.).
- `composite_pk_test.go` — composite PKs en los 6 motores.
- `in_operand_test.go` and `internal/enginesuite/in_typed_slices_test.go` — the IN / BETWEEN operand as any slice or array, the empty list meaning what `[]any{}` means, and what is refused (QK-33).
- `write_where_test.go` and `internal/enginesuite/write_where_test.go` — the write paths and PreloadWhere select the same rows as a SELECT with the same conditions (QK-39).
- `upsert_tenant_test.go` and `internal/enginesuite/upsert_tenant_test.go` — the upserts' update branch and UpdateMap stay inside the tenant (QK-43, QK-44).
- `where_guard_test.go` and `internal/enginesuite/where_guard_test.go` — reads by key stay inside the tenant and inserts/updates store the resolved tenant (QK-42); the writes by key honour the query's conditions and the tenant, report nothing when excluded, keep `ErrStaleEntity` for real conflicts (QK-40); the scopes AND with the caller's whole expression (QK-41).
- `update_batch_versions_test.go` and `internal/enginesuite/update_batch_versions_test.go` — `UpdateBatch` is all or nothing over the versions, names every stale key, skips what the conditions exclude and bumps in memory only after the batch, with or without `CheckVersions()` (QK-45, DEP-2026-003).

Cualquier cambio en `Query[T]` debe pasar la suite completa, no sólo SQLite.

## Cuándo invocar al `code-reviewer`

Antes de cualquier PR que toque `query_builder.go`, `query_exec.go`, `query_crud.go`. El reviewer verifica explícitamente: propagación de tenant en clones, validación de identifiers, ausencia de raw concatenation, tests en los 6 motores, entrada en `website/docs/queries/` y `CHANGELOG.md`.
