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
related_p0: [P0-4, P0-5]
closed_p0: [P0-1, P0-3]
phase: 0
---

# Playbook: Query Builder

## Qué es y qué no es

Quark tiene un **query builder reflect-based con clones inmutables**, no un AST componible. `Query[T]` lleva un `BaseQuery` con slices de `condition`, `join`, `orderBy`, etc. Cada método (`Where`, `Join`, `Limit`) clona el query y devuelve uno nuevo. Los generics tipan T pero el núcleo opera con `reflect.Value`.

**Lo que SÍ se puede expresar**: WHERE/IN/BETWEEN/NOT/JSON/Or, Joins (Inner/Left/Right), GroupBy+Having, Distinct, Select cols, OrderBy, Limit/Offset, Apply(scopes), agregados Sum/Avg/Min/Max, Count, Find, First, List, Iter, Cursor, Paginate, eager loading via Preload.

**Lo que NO se puede expresar (cae en RawQuery, ver §Roadmap)**: CTEs (`WITH`), recursive CTEs, window functions (`OVER`), `UNION`/`INTERSECT`/`EXCEPT`, `FOR UPDATE`/`FOR SHARE`/`SKIP LOCKED`, subqueries componibles tipadas (sólo hay un `WhereSubquery` raw gateado por flag de seguridad), nested preload (`Orders.Items` no es expresable; sólo `Orders` plano).

Esto es deuda conocida; el plan es introducir un AST en Fase 2 (ver `docs/ANALISIS_MADUREZ.md` §4).

## Bugs P0 vivos

### P0-4 · `isZeroValue` impide `Update` con `false`/`0`/`""`

**Localización**: `query_crud.go:649` (`isZeroValue`).

**Impacto**: `Update(entity)` salta los campos cuyo valor es zero-value del tipo. No se puede poner un bool a `false`, un int a `0`, un string a `""`.

**Workaround actual**: `UpdateMap(map[string]any{...})` con `Where` manual. Documentar la trampa visiblemente. Fix permanente con dirty tracking ligero llega en Fase 1.

### P0-5 · `JOIN ... ON` se concatena raw

**Localización**: `query_builder.go:229` (firma `Join(table, onClause)`) + `query_exec.go:467` (concatena el `onClause`).

**Impacto**: `WHERE col` se valida via `internal/guard.SQLGuard.ValidateIdentifier`; `JOIN ON` no. Inconsistencia de seguridad. Si `onClause` viene de input dinámico, vector de inyección.

**Fix esperado**: API estructurada `Join(table).On(col, op, otherCol)` y deprecar la string-raw. Mientras tanto, validar el patrón mínimo en el guard.

## Historial — bugs cerrados

### P0-1 · `Or()` no propagaba `tenantID/tenantCol/schema/cache/limits` (cerrado)

`Query[T].Or` construía un `BaseQuery` blanco hardcoded; el grupo OR escapaba
el predicado de tenant por precedencia SQL. Fix: `(b *BaseQuery) cloneForGroup()`
copia el contexto de aislamiento al blank y pre-inyecta el predicado de tenant
para que el grupo OR lo herede. Detalles en `docs/playbooks/tenant.md`.

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

### `List()` con resultado truncado silenciosamente

`List()` aplica un cap implícito de 100 filas si el caller no llamó a `Limit()` (`query_exec.go:149`). **Esto trunca sin error.** Si introduces una API similar (`AllWhere`, `FetchAll`), o expón el cap o devuelve error si se rebasa.

Para lectura masiva, usar `Iter()` o `Cursor()` (server-side iteration), no `List()`.

### Eager loading sin chunkear `IN(...)`

Oracle limita `IN` a 1000 elementos. MSSQL limita el número total de parámetros bind a ~2100. Si añades un nuevo `loadXxxRelation` (ej. para nested preload en Fase 2), **chunkea las parent keys** en bloques de 500 antes de emitir el SELECT. Patrón existente en `DeleteBatch` (`query_crud.go:1356`).

Hoy `loadStandardRelation`/`loadM2MRelation`/`loadPolymorphicRelation` (`query_exec.go:739-1065`) NO chunkean — es deuda. Cualquier preload masivo en Oracle va a romper.

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

Cualquier cambio en `Query[T]` debe pasar la suite completa, no sólo SQLite.

## Cuándo invocar al `code-reviewer`

Antes de cualquier PR que toque `query_builder.go`, `query_exec.go`, `query_crud.go`. El reviewer verifica explícitamente: propagación de tenant en clones, validación de identifiers, ausencia de raw concatenation, tests en los 6 motores, entrada en `website/docs/queries/` y `CHANGELOG.md`.
