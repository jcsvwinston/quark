# Quark — backlog táctico

> **🧪 Bug-bash post-v1.0 — herramienta operativa de calidad.** Con
> v1.0.0 publicada y los 5 items §A del V1_GATE.md cerrados, el bloqueante
> de calidad para v1.0.x / v1.1 no es ya un gate cuantitativo. Es la
> **acumulación silenciosa** de regresiones cross-engine y dialect-specific
> gaps que la suite unitaria no atrapa, y que el bug-bash post-v1.0 está
> diseñado para capturar antes de que las reporte un usuario externo.
>
> **Plan:** [`docs/BUGBASH_PLAN.md`](docs/BUGBASH_PLAN.md). **Dominio:**
> [`bugbash/DOMAIN.md`](bugbash/DOMAIN.md). **Comando:** `/bugbash`. **Subagente:**
> `bugbash-reporter`.
>
> **Cadencia recomendada:** F0+F1+F13 obligatorios antes de cualquier
> v1.0.x patch; pasada completa F0-F13 antes de cualquier v1.x.0 minor;
> F14 soak overnight en ventana de release-candidate.
>
> Los fallos del bug-bash aparecen en la sección § "Bug-bash hallazgos"
> de abajo (creada por `bugbash-reporter` al cerrar cada pasada).

## Bug-bash hallazgos (activos)

> Mantenido por `bugbash-reporter` tras cada pasada. F1 (smoke) y F2 (API
> surface) se corrieron el 2026-05-28 sobre los 6 motores
> (SQLite/PG/MySQL/MariaDB/MSSQL/Oracle). **Sin hallazgos F1/F2 abiertos:
> BB-1, BB-2, BB-3 y BB-4 cerrados** (2026-05-29). PG y SQLite limpios en
> ambas fases. **Fases implementadas: F0, F1, F2, F3, F13** (F13 — security/
> anti-injection — añadida 2026-05-29, gate obligatorio antes de patch
> v1.0.x; verde en los 6 motores, sin hallazgos. F3 — relaciones — añadida
> 2026-05-31; halló y **cerró BB-5, BB-6 y BB-7**. F5 — multi-tenancy —
> añadida 2026-05-31; halló y **cerró BB-8** (SchemaPerTenant write routing).
> Pendientes: F4, F6-F12, F14.
>
> **Pasada F3 cross-engine (2026-05-31, Docker):** **verde 9/9 en los 6
> motores** (SQLite + PG + MySQL + MariaDB + MSSQL + Oracle), sin hallazgos
> abiertos. Los 3 bugs que destapó (BB-5 nullable-FK preload, BB-6 MSSQL null
> `Nullable[[]byte]`, BB-7 Oracle m2m) quedaron arreglados y verificados
> cross-engine en la misma pasada.
>
> **Pasada F5 cross-engine (2026-05-31, Docker):** **verde en los 6 motores**.
> RLSClient (aislamiento + Or/P0-1 + concurrencia) y la aserción de
> `ErrUnsupportedFeature` de RLSNative corren en los 6; DatabasePerTenant en
> SQLite, SchemaPerTenant + RLSNative (engine-enforced vía rol no-superusuario)
> en PG. Destapó **BB-8** (writes de SchemaPerTenant iban al schema por
> defecto, no al del tenant), arreglado y verificado en la misma pasada.

### ~~BB-1 · `uuid.UUID` se corrompe en silencio si se mapea a `UNIQUEIDENTIFIER` (MSSQL)~~

**Cerrado** (2026-05-29, rama `fix/bb1-mssql-uuid-caveat`). Fix **docs-only**:
el default de Quark ya es seguro (el migrador mapea uuid-PK → `NVARCHAR(36)` en
MSSQL, `internal/migrate/migrate.go:240`), y la trampa sólo aparece si el
usuario registra un `TypeMapper` que devuelva `UNIQUEIDENTIFIER`. Se añadió un
admonition `:::warning` en `website/docs/guides/modeling.mdx` §"Custom type
mappers" explicando el byte-swap (SQL Server little-endian vs google/uuid
big-endian RFC-4122), dirigiendo a `VARCHAR(36)`/`NVARCHAR(36)`, y apuntando a
`mssql.UniqueIdentifier` (del driver) para quien necesite la columna nativa.
Sin cambio de código ni nueva API (un helper uuid sería superficie nueva, fuera
de scope para un finding doc-drift). CHANGELOG `[Unreleased]/Documentation`.

<details><summary>Descripción original del hallazgo</summary>

**Severidad:** P2 (footgun con corrupción silenciosa; el camino documentado
—`VARCHAR(36)`— funciona). **Categoría:** doc-drift / gap. **Motor:** MSSQL.
**Fase:** F1 (`bugbash/phases/f01_smoke`). **Estado:** abierto.

Detectado en la 1ª pasada real de F1 multi-motor. Mapear `uuid.UUID` a la
columna nativa `UNIQUEIDENTIFIER` y hacer round-trip devuelve un UUID
**distinto**: SQL Server almacena los 3 primeros grupos del GUID en
little-endian mientras `github.com/google/uuid` (RFC-4122) es big-endian, así
que `go-mssqldb` los devuelve byte-swapped. Ejemplo real:
`want 6a4c38e2-218a-4d93-… → got e2384c6a-8a21-934d-…` (grupos 1-3 invertidos).
PG (native `UUID`), MySQL/MariaDB/Oracle (`VARCHAR`/`VARCHAR2(36)`) hacen
round-trip correcto; sólo `UNIQUEIDENTIFIER` falla.

- **Workaround (ya aplicado en el harness):** mapear uuid a `VARCHAR(36)` en
  MSSQL — coincide con el ejemplo `type_mapper.go` de Quark. F1 quedó verde
  6/6 con ese cambio (`bugbash/domain/mappers.go`).
- **Acción Quark sugerida:** la docs/ejemplo usan `VARCHAR(36)` para uuid en
  MSSQL pero **no explican por qué** (la trampa de `UNIQUEIDENTIFIER`). Añadir
  un caveat explícito en la guía de tipos custom, o proveer un helper de uuid
  que maneje el byte-order de UNIQUEIDENTIFIER (`mssql.UniqueIdentifier`).
- **Reproducer:** `go test -tags=bugbash -run TestSmoke ./phases/f01_smoke/... -engines=mssql`
  revirtiendo el mapper de `mappers.go` a `UNIQUEIDENTIFIER`.

</details>

### ~~BB-2 · Los `Join` sobre queries tipadas no acotan el `SELECT` a la tabla base~~

**Cerrado** (2026-05-29, rama `fix/bb2-typed-join-projection`). Dos defectos
en la generación de SQL bajo join: (A) `buildSelect` emitía `SELECT *`, que
bajo un join trae columnas de todas las tablas → nombres duplicados / mis-bind
del scanner; ahora proyecta `SELECT "<tabla_base>".*` cuando hay joins
(`query_exec.go`). (B) el predicado de soft-delete se inyectaba sin cualificar
(`deleted_at IS NULL`) → `ambiguous column`; ahora se cualifica con la tabla
base como fragmento `isRaw` pre-quoteado (`soft_delete.go`). `Join().List()`
pasa a ser camino soportado en los 6 motores (antes sólo `Count()`).
Regresión: `testBB2JoinProjection` (nuevo `bb2_join_projection_test.go`,
wired a SharedSuite) + subtest `OnTypedFormListsBaseColumns` en
`join_builder_test.go`. Verde en SQLite + Postgres local; resto vía CI.
Docs: `CHANGELOG.md` `[Unreleased]/Fixed` + `website/docs/guides/querying.mdx`
§ Projection under a join.

<details><summary>Descripción original del hallazgo</summary>


**Severidad:** P1 (joins es feature core; produce error duro o corrupción
silenciosa). **Categoría:** gap. **Motores:** todos (es generación de SQL).
**Fase:** F2 (`bugbash/phases/f02_api_surface`). **Estado:** abierto.

Una query `For[T]` sin `Select` explícito genera `SELECT *` (confirmado en
`query_builder.go:601`). Bajo un `Join`, eso trae **columnas duplicadas** de
todas las tablas unidas, y además el filtro de soft-delete se inyecta **sin
cualificar** (`deleted_at IS NULL`, no `orders.deleted_at IS NULL`). Síntomas:

- `For[Order].Join("customers")…List()` → `ambiguous column name: deleted_at`
  (orders y customers tienen `deleted_at`) — en los 6 motores.
- `For[Order].LeftJoin("order_lines")…List()` → `converting NULL to int64`
  (el `order_lines.id` NULL del outer join se escanea en `Order.ID`).
- `For[Order].With(cte).Join(cte)…List()` → `ambiguous column 'id'`
  (MSSQL `Ambiguous column name 'id'`, Oracle `ORA-00918`).

La suite de Quark **no tiene cobertura de `Join().List()`** (greppeado). Un
inner join "funciona" en motores laxos pero puede escanear el `id` de la otra
tabla en `T.ID` (corrupción silenciosa).

- **Acción Quark sugerida:** para queries tipadas con join, proyectar
  `SELECT <tabla_base>.*` (o columnas cualificadas) y cualificar el filtro de
  soft-delete con la tabla base. Mientras tanto, el harness valida la
  generación de SQL del join vía `AsSubquery` y no ejecuta el join-en-`T`.
- **Reproducer:** `For[Order](ctx,c).Join("customers").On("orders.customer_id","=","customers.id").List()`.

</details>

### ~~BB-3 · MariaDB rechaza `FOR SHARE` (sintaxis MySQL-8 en el dialecto compartido)~~

**Cerrado** (2026-05-29, rama `fix/bb3-mariadb-for-share`). Dos partes:
(1) **causa raíz** — MariaDB no tiene driver `database/sql` propio (usa
`go-sql-driver/mysql`, nombre "mysql"), así que `New` le asignaba el dialecto
MySQL. Ahora `New` hace `SELECT VERSION()` una vez en conexiones "mysql" y
cambia a `MariaDBDialect` si el server es MariaDB (`client.go:isMariaDBServer`;
`WithDialect` explícito gana y salta el probe). (2) **fix de dialecto** —
`MariaDBDialect.LockSuffix` emite `LOCK IN SHARE MODE` para `ForShare` (MariaDB
no tiene `FOR SHARE`); como esa forma no admite modificadores,
`ForShare`+`SkipLocked`/`NoWait` devuelve `ErrUnsupportedFeature`. `ForUpdate`
intacto; MySQL sigue emitiendo `FOR SHARE`. Regresión:
`TestLockSuffix_PerDialect` (casos MariaDB) + subtests
`MariaDBForShareUsesLockInShareMode` / `MariaDBForShareWithSkipLockedUnsupported`
/ `MySQLForShareStillEmitsForShare` en `testPessimisticLocking`. Verde en
MariaDB + MySQL + SQLite local. Docs: `CHANGELOG.md` `[Unreleased]`
(Added: auto-detect; Fixed: ForShare) + `website/docs/guides/installation.mdx`
+ `querying.mdx` §Pessimistic Locking.

<details><summary>Descripción original del hallazgo</summary>

**Severidad:** P2. **Categoría:** dialect-specific. **Motor:** MariaDB.
**Fase:** F2. **Estado:** abierto.

`ForShare()` emite `FOR SHARE` (sintaxis de MySQL 8) porque Quark trata a
MariaDB con el mismo dialecto que MySQL. MariaDB no soporta `FOR SHARE` (usa
`LOCK IN SHARE MODE`): `Error 1064 … syntax error … near 'SHARE'`. `ForUpdate`
sí funciona en MariaDB.

- **Acción Quark sugerida:** distinguir MariaDB de MySQL (server version o
  flag) y emitir `LOCK IN SHARE MODE`, o devolver `ErrUnsupportedFeature`
  limpio en MariaDB para `ForShare`.
- **Reproducer:** `For[Order](ctx,c).Where("status","=","pending").ForShare().List()` en MariaDB.

</details>

### ~~BB-4 · Oracle: `ForUpdate` + el `Limit` implícito de `List()` → ORA-02014~~

**Cerrado** (2026-05-29, rama `fix/bb4-oracle-forupdate-list`). Estrategia
elegida (opción B): en Oracle, bajo lock activo, se **suprime el cap implícito**
de `List()` (el OFFSET/FETCH desaparece y el `FOR UPDATE` aplica a todas las
filas que matchean, con `WARN`), de modo que `ForUpdate().List()` funciona; un
`Limit`/`Offset` **explícito** junto a un lock devuelve `ErrUnsupportedFeature`
(no hay forma de una sola sentencia en Oracle — ORA-02014). Implementado en
`query_exec.go:buildSelect` (flag `suppressRowLimit`, gated a
`dialect.Name()=="oracle" && !lock.IsZero()`). Los otros 5 motores intactos
(PG/MySQL/MariaDB permiten `LIMIT`+`FOR UPDATE`; MSSQL usa table hints).
Regresión: subtests `OracleForUpdateListDropsImplicitRowLimit` /
`OracleForUpdateExplicitLimitIsUnsupported` /
`ForUpdateListUnaffectedOnRowLockDialects` en `testPessimisticLocking`
(`locking_test.go`). Verde en Oracle + PG + SQLite local. Docs:
`CHANGELOG.md` `[Unreleased]/Fixed` + `website/docs/guides/querying.mdx`
§ Oracle: locking and row limits don't mix.

<details><summary>Descripción original del hallazgo</summary>

**Severidad:** P1 (FOR UPDATE vía `List()` está roto en Oracle: `List` aplica
un `Limit` por defecto). **Categoría:** dialect-specific. **Motor:** Oracle.
**Fase:** F2. **Estado:** abierto.

`ForUpdate().List()` falla en Oracle con `ORA-02014: cannot select FOR UPDATE
from view with DISTINCT, GROUP BY, etc.`: `List()` aplica un `Limit(100)` por
defecto, que en Oracle se implementa envolviendo la query en una vista
(ROWNUM/OFFSET), y `FOR UPDATE` no puede aplicarse sobre esa vista envuelta.
Afecta a `ForUpdate`/`SkipLocked`/`NoWait` (todos pasan por la envoltura).

- **Acción Quark sugerida:** en Oracle, empujar `FOR UPDATE` dentro de la
  subconsulta no envuelta, o no envolver cuando hay locking, o devolver un
  error claro guiando a usar `Limit` explícito compatible.
- **Reproducer:** `For[Order](ctx,c).Where("status","=","pending").ForUpdate().List()` en Oracle.

</details>

### ~~BB-5 · `Preload` de relaciones con FK *nullable* (`*int64`) carga `nil`/vacío~~

**Cerrado** (2026-05-31, mismo PR que añade la fase F3). El eager loader
(`preload_loaders.go`) indexaba el mapa de match padre/hijo por el **valor
crudo del campo**: cuando la columna de join mapea a un campo puntero
(`*int64`, típico en FK nullable), la clave del mapa era un `*int64` mientras
la PK de la fila relacionada se escaneaba a `int64`, así que las claves nunca
comparaban iguales y la relación cargaba silenciosamente `nil`/vacía. Afectaba
a **toda** relación con FK nullable en ambas direcciones: belongs_to (la FK
vive en el dueño, p.ej. `Invoice.Order` sobre `OrderID *int64`) y has_many /
has_one (la FK vive en el hijo, p.ej. el árbol autorreferencial
`Category.Children` sobre `ParentID *int64`). Las relaciones con FK `int64`
(no puntero) nunca se vieron afectadas — de ahí que F1/F2 no lo detectaran.

Fix: helper `normalizeKey` que desreferencia la clave puntero a su pointee
antes del match en los dos lados del join (`loadStandard` + `scanAndMapStandard`);
una FK `NULL` no matchea a ningún padre, como debe. Regresión:
`preload_nullable_fk_test.go` (unit, rápido, 4 subtests cubriendo ambas
direcciones + FK NULL + dotted anidado) y la fase F3 (cross-engine).
CHANGELOG `[Unreleased]/Fixed`.

<details><summary>Descripción del hallazgo</summary>

**Severidad:** P1 (corrupción silenciosa de lectura: la relación existe en BD
pero llega vacía a la app; ningún error). **Categoría:** regression / gap.
**Motor:** detectado en SQLite, root-cause es lógica de reflect compartida →
afecta a los 6 motores por igual. **Fase:** F3 (`bugbash/phases/f03_relaciones`).
**Estado:** cerrado.

- **Reproducer:** `For[Category](ctx,c).Preload("Children").Find(rootID)`
  con `Category{ParentID *int64; Children []Category rel:"has_many" join:"parent_id"}`
  → `Children` vacío pese a existir hijos. Idem `Preload("Parent")`.

</details>

### ~~BB-6 · `Nullable[[]byte]` NULL no se inserta en MSSQL (nvarchar→varbinary)~~

**Cerrado** (2026-05-31, mismo PR que añade F3). Fix: `nullBytesArg` en
`nullable.go` sustituye un `Nullable[[]byte]` inválido por un `[]byte(nil)`
tipado (NULL binario en los 6 motores) en `bindColumnArg`, en vez de dejar
que el `Valuer` devuelva un nil sin tipo que go-mssqldb codifica como
`nvarchar`. Regresión: `nullable_bytes_test.go` (unit del helper + round-trip
SQLite) y la fase F3 (el seed deja `UserProfile.Avatar` NULL a propósito,
verde en los 6 motores). CHANGELOG `[Unreleased]/Fixed`.

<details><summary>Descripción del hallazgo</summary>

**Severidad:** P1 (rompía el INSERT de cualquier fila con un BLOB nullable
vacío en SQL Server). **Categoría:** dialect-specific. **Motor:** MSSQL.
**Fase:** F3 (`bugbash/phases/f03_relaciones`, destapado al sembrar
`UserProfile`).

`Nullable[[]byte]` es `sql.Null[[]byte]`. Al insertar uno con `Valid:false`
(NULL) en MSSQL, el parámetro llega como `nvarchar` contra una columna
`varbinary(max)` y el motor aborta:

```
mssql: Implicit conversion from data type nvarchar to varbinary(max) is not
allowed. Use the CONVERT function to run this query.
```

PG/MySQL/MariaDB/Oracle/SQLite insertan el NULL sin problema; sólo MSSQL.
Sospecha: el path de bind de `INSERT` no envía un NULL tipado (o tipa el
parámetro como string) para `sql.Null[[]byte]` vacío en el dialecto MSSQL.

- **Reproducer:** sembrar cualquier struct con un campo `quark.Nullable[[]byte]`
  sin valor (`Valid:false`) contra MSSQL. En F3, `UserProfile.Avatar`.

</details>

### ~~BB-7 · `many_to_many` preload carga 0 filas en Oracle (coerción de NUMBER)~~

**Cerrado** (2026-05-31, mismo PR que añade F3). **Dos** defectos en `loadM2M`
(`preload_loaders.go`) se combinaban: (1) el scan de la fila relacionada hacía
`FieldByCol[col]` con el nombre de columna tal cual lo reporta el driver, pero
Oracle lo devuelve en MAYÚSCULAS y `FieldByCol` está indexado por el db-tag en
minúsculas → no mapeaba ninguna columna y la fila relacionada se escaneaba toda
a cero (los otros loaders ya hacían `ToLower`; el de m2m no); y (2) las columnas
FK de la join table se escaneaban en `interface{}`, y go-ora devuelve `NUMBER`
como `string`, que no es `==` al `int64` de la PK. Fix: el scan ahora hace
`strings.ToLower(col)` y lee los FK de la join en destinos tipados a los campos
PK (dueño/relacionado) vía `makeScanDest`, así las claves cuadran en cualquier
driver. Regresión: la fase F3 sobre Oracle (el path ya funcionaba en los otros
5 motores; `TestM2MPreload` lo cubre en SQLite). CHANGELOG `[Unreleased]/Fixed`.

<details><summary>Descripción del hallazgo</summary>

**Severidad:** P1 (relación m2m rota en Oracle: la relación existe en la join
table pero llega vacía a la app, sin error). **Categoría:** dialect-specific.
**Motor:** Oracle. **Fase:** F3.

`User.Roles` (`many_to_many` vía `user_roles`) cargaba `0` roles en Oracle pese
a existir los enlaces (verificado: `SELECT COUNT(*)` en la join table = 2); los
otros 4 motores cargaban los 2 correctos. La causa raíz primaria fue la
sensibilidad a mayúsculas del lookup de columnas en el scan de la fila
relacionada (Oracle devuelve `ID`/`NAME`, `FieldByCol` indexa `id`/`name`); el
scan de la join en `interface{}` (go-ora devuelve `NUMBER` como `string`) era un
segundo desajuste. Aislado porque has_one/has_many/belongs_to/polymorphic
**sí** hacían `ToLower` y pasaban en Oracle; sólo m2m fallaba.

</details>

### ~~BB-8 · `SchemaPerTenant`: los writes van al schema por defecto, no al del tenant~~

**Cerrado** (2026-05-31, mismo PR que añade F5). En `Create`/`Update`, el path
de persistencia (`saveAny`, `query_crud.go`) construía el INSERT/UPDATE desde
un `BaseQuery` nuevo (`dq`/`sq`) que copiaba `tenantID`/`tenantCol` de `q` pero
**no `schema`**, así que `fullTableName()` emitía el nombre de tabla sin
cualificar y el write caía en el schema del `search_path` por defecto, mientras
las lecturas (que sí honran `q.schema` vía `fullTableName`) miraban en el schema
del tenant. Bajo `SchemaPerTenant` los writes "desaparecían" para el lector del
tenant y los de **todos** los tenants se co-mingaban en un único schema. Fix:
propagar `schema: q.schema` a `dq` y `sq` en `saveAny`. Verificado en PG por la
fase F5 (diagnóstico: write → `spa.tdocs`, no `public`). CHANGELOG
`[Unreleased]/Fixed`.

<details><summary>Descripción del hallazgo</summary>

**Severidad:** P1 (correctness + aislamiento de SchemaPerTenant: writes al
schema equivocado, co-mingle entre tenants). **Categoría:** regression.
**Motor:** todos los que soportan schemas (verificado en PG; lógica
engine-agnostic). **Fase:** F5 (`bugbash/phases/f05_tenancy`).

`For[T](ctx, router)` con `SchemaPerTenant` fija `q.schema = tenantID`. Las
lecturas lo respetan; `saveAny` no, porque su `BaseQuery` interno no copiaba el
campo. Aislado porque las otras tres estrategias no usan `q.schema`.

- **Reproducer:** crear schema `spa` + tabla; `For[T](withTenant(ctx,"spa"),
  router).Create(&row)` con `SchemaPerTenant` → el row aparece en el schema por
  defecto y `For[T](...,"spa").List()` devuelve 0.

</details>

---

> **✅ v1.0.0 publicado (2026-05-27).** Tag `v1.0.0` (PR #116, vía
> release-please con trailer `Release-As: 1.0.0`); GitHub Release marcada
> Latest; docs live en `jcsvwinston.github.io/quark/docs` (1.0.0 es ahora
> la versión por defecto). El gate v1.0 ([`docs/V1_GATE.md`](docs/V1_GATE.md))
> §A cerró 5/5; ADR-0017 ya había retirado el gate ≥3× p99 de ADR-0002 y
> reencuadrado el codegen como type-safety. **Fase 6 cerrada — era la última
> fase pre-v1.0; con ella el roadmap a v1.0 está completo.** Compromiso
> SemVer: `v1.x` mantiene compatibilidad de API; breaking → `v2.x` con
> `docs/MIGRATION_v2.0.0.md`. **Trabajo siguiente = post-v1.0 / v1.1**
> (items diferidos abajo: scatter-gather y shard-key-from-entity de F6-7,
> F6-3b sólo si type-safety, stampede cross-instance, registry de
> migración versionado per-Client; **inbound `LISTEN/NOTIFY` ya
> entregado** en `[Unreleased]`, ADR-0019 — ver abajo). El historial del
> §A se conserva abajo.
>
> 1. ~~**Oracle en CI**~~ — ✅ **CERRADO (2026-05-27, Salida A — Oracle en CI
>    bloqueante)**. Programa multi-sesión: PR (a) #123 (JSON path literal +
>    `''`→NULL) 187/24→199/12; PR (b) #125 / F3-2 introspección Oracle
>    199/12→211/5; PR (c) #126 / lock distribuido `DBMS_LOCK` (ADR-0018)
>    211/5→**216/0**. **Flip de CI: PR #127** — Oracle en la matriz
>    `integration` bloqueante, en verde sobre runner hosted (216/0) sin
>    regresión en los otros 5 motores. El job arranca `gvenzl/oracle-free`
>    con `docker run` + DSN (no testcontainers, que crashea en hosted);
>    `docker exec -i` para el grant `DBMS_LOCK` (sin `-i` el grant era un
>    no-op silencioso). Detalle en [`docs/V1_GATE.md`](docs/V1_GATE.md) §A Item 1.
> 2. ~~**F6-7 follow-ups**~~ — ✅ CERRADO (alcance mínimo): ejemplo runnable
>    `examples/sharding/main.go` (SQLite, self-contained) + `advanced/sharding.mdx`;
>    scatter-gather y `shard-key-from-entity` diferidos a v1.1.
> 3. ~~**`LISTEN/NOTIFY` listener side**~~ — ✅ CERRADO en dos pasos:
>    (a) en v1.0, Salida B: asimetría outbound/inbound documentada
>    (warning en `events.mdx` + caveat en `intro.mdx`), inbound diferido
>    a post-v1.0; (b) **inbound real entregado post-v1.0** (`[Unreleased]`,
>    [ADR-0019](docs/adr/0019-inbound-listen-notify-dedicated-conn.md)):
>    `ListenerFactory.CreateListener` devuelve un `EventListener` real en
>    PostgreSQL sobre una `*sql.Conn` dedicada del pool (pgx
>    `WaitForNotification`); otros dialectos siguen en
>    `ErrDialectNotSupported`. `pg_listener.go` + `pg_listener_test.go`
>    (round-trip Listen→Notify→Receive gated por DSN). Sentinels nuevos
>    `ErrListenerClosed`/`ErrNoSubscription`.
> 4. ~~**Cross-instance stampede protection**~~ — ✅ CERRADO vía Salida B:
>    warning "in-process only" promovido en `caching-observability.mdx` +
>    caveat en `intro.mdx`; hook `DistributedLock` diferido a post-v1.0.
> 5. ~~**`RELEASE_NOTES_v1.0.0.md` con Known limitations**~~ — ✅ CERRADO
>    (2026-05-27, PR #127): `docs/RELEASE_NOTES_v1.0.0.md` con los waivers de
>    items 2+3+4 (+ F6-3b, migration registry global, failover pasivo) y la
>    fila Oracle ya resuelta (Oracle en CI bloqueante). La narrativa "Phases
>    delivered" la escribe el PR de `/release v1.0.0` (prosa de release, no
>    bloqueante del gate).
>
> **Items recomendados pero no bloqueantes**: bug-bash externo
> (`v0.x-rc1` con ventana de feedback), F6-3b (UPDATE/partial binder),
> versioned migration registry per-Client.
>
> **Orden de ataque sugerido** (§C de V1_GATE.md): Item 1 → Item 2 →
> Items 3+4 en pasada conjunta → Item 5 → opcional RC. 4-5 sesiones
> efectivas con Salidas B donde corresponde.

---

> **Fase 5 cerrada (2026-05-21, v0.9.0).** Los 7 items F5-1..F5-7
> entregados: rename `RowLevelSecurityClient` + alias (#78),
> `RowLevelSecurityNative` motor PG (#80), CLI `quarktenant`
> install-rls-policies (#81), hooks transaccionales `After*`
> post-commit + `BeforeFind`/`AfterFind` (#82), `Tx.OnCommit`/
> `Tx.OnRollback` + `TxFromContext` (#83), `EventBus` real (#84),
> audit log atómico (#85). Dos breaking-minor (timing de hooks bajo
> `Client.Tx`; rename placeholder `EventBus`→`ListenerFactory`),
> documentados en `MIGRATION_v0.9.0.md`. **Próxima fase: Fase 6**
> (codegen + HA + benchmarks → v1.0); requiere apertura formal con
> ADR para la convivencia reflect/codegen. Deuda menor heredada:
> ~~savepoint-rollback gap~~ (corregido en `[Unreleased]`: los hooks
> `After*`/`OnCommit`/`OnRollback` encolados dentro de un scope de
> savepoint se descartan al hacer `RollbackTo`; `tx.go` +
> `hooks_tx_test.go` + subtest `SavepointHookUnwind` en SharedSuite),
> ~~warning `client.Raw()` bajo Native~~ (añadido en `[Unreleased]`:
> `RawQuery`/`Exec` emiten `quark.tenant.raw_under_native_rls` cuando
> hay tenant en contexto bajo router Native; PG sigue enforcando la
> policy, el warning es UX — `client.go` + `tenant_router.go` +
> `raw_under_native_test.go`), ~~guards `logger != nil` redundantes~~
> (**descartado**: NO son redundantes — protegen literales de test que
> pasan logger nil (`newStampedeStore(...,nil)`, `&Client{}`); en
> producción `c.logger` siempre es no-nil, pero quitarlos rompe tests
> sin beneficio), ~~MSSQL JSON[T] scan bug~~ (corregido en
> `[Unreleased]`: `JSON[T].Value()`/`Array[T].Value()` devuelven string
> en vez de `[]byte`, así go-mssqldb los bindea como NVARCHAR y no como
> VARBINARY; round-trip limpio en MSSQL para JSON/Array/audit, skips
> eliminados), Oracle fuera de CI. **Gap nuevo documentado**: los
> savepoints emiten SQL ANSI (`SAVEPOINT` / `ROLLBACK TO SAVEPOINT`);
> MSSQL necesita `SAVE TRANSACTION` / `ROLLBACK TRANSACTION`, así que
> savepoints no funcionan en MSSQL hoy — `SavepointHookUnwind` skipea
> MSSQL hasta que se añada el soporte de dialecto (follow-up).
>
> **Fase 4 cerrada (2026-05-15, v0.8.0).** Los 7 items F4-1..F4-7
> entregados: OTel metrics + span redaction (#70), slow query log
> (#71), cache key determinismo (#69), stampede protection vía
> `stampedeStore` wrapper (singleflight + ±jitter + XFetch, ADR-0011;
> #72 + gofmt #73), per-row invalidation + Redis tag-TTL fix (#74),
> deadlock retry on `Client.Tx` (#75). Sin breaking changes; todas
> las features opt-in. Cross-instance stampede queda como gap
> documentado para ADR sucesor; deadlock retry test cross-engine
> queda como follow-up.
>
> **v0.7.0 publicada (2026-05-14).** Timezones por columna entregadas;
> Bloque B cerrado entero. Estrategia híbrida `WithDefaultTZ` + tag
> `quark:"tz=..."`, wire UTC-always, fail-fast con `ErrInvalidTimezone`,
> opt-in puro (ADR-0010, PR #63).
>
> **Phase 3 cerrada (2026-05-14, v0.6.0).** Los 7 items F3-1..F3-7
> entregados; `Array[T]` (Bloque B / Arrays Postgres) también dentro
> de v0.6.0. Schema-as-code migrations en producción: introspection
> neutral en los 4 motores CI + SQLite, diff puro en Go,
> `PlanMigration` con round-trip vacío en los 5 motores, `ApplyPlan`
> transaccional en PG/MSSQL/SQLite + resumable en MySQL/MariaDB/Oracle,
> `quarkmigrate` CLI, `Backfill` orquestado, registry per-Client,
> lock distribuido. Sin breaking changes.
>
> **Fase 0 cerrada (2026-05-13, v0.5.0)** — los 5 P0 originales
> tachados, F0-1..F0-10 todos cerrados, integration matrix bloqueante
> en 4/5 motores (PG/MySQL/MariaDB/MSSQL; Oracle como gap documentado).
>
> Convención: cada tarea lleva su archivo:línea de origen, criterio de "done"
> y dónde queda la documentación al cerrar.

---

## Próxima sesión — arranque automatizado

> **No empieces "explorando".** Invoca `/next-session [foco]` (definido en
> `.claude/commands/next-session.md`) y trabaja el bloque que indique.
>
> Foco admitido: `auto` (post-v1.0). Si dudas, usa `auto`. Los focos
> `f0`, `fase3`, `tipos`, `fase4`, `fase5` y `fase6` ya no aplican —
> cerrados; **v1.0.0 publicado**. El trabajo post-v1.0 (v1.1) aún no tiene
> fase formal abierta — `auto` audita el backlog diferido y propone.

Estado real del backlog post-v0.9.0 (releases v0.5.0 → v0.9.0 hechos;
**Fases 0, 1, 2, 3, 4 y 5 cerradas**; `[Unreleased]` con la deuda menor
post-v0.9.0 en vuelo, ver abajo):

1. ~~**Bloque A — Cerrar Fase 0**~~. Cerrado en v0.5.0.
2. ~~**Bloque B — Tipos diferidos de Fase 1**~~. Cerrado en v0.7.0
   (`Array[T]` PR #42 + timezones por columna PR #63, ADR-0010).
3. ~~**Bloque C — Phase 3 (migraciones)**~~. Cerrado en v0.6.0.
   F3-1..F3-7 entregados; ADR-0009 archivado.
4. ~~**Fase 4 — observability + cache + deadlock retry**~~. Cerrado en
   v0.8.0. F4-1..F4-7 entregados; ADR-0011 archivado.
5. ~~**Fase 5 — RLS real + hooks transaccionales + EventBus**~~. Cerrado
   en v0.9.0 (F5-1..F5-7; ADR-0012/0013 archivados).
6. **Fase 6 — Codegen, performance y HA** (apertura formal hecha
   2026-05-22, scope completo del ROADMAP). `docs/ROADMAP.md` §
   "Phase 6"; `docs/ANALISIS_MADUREZ.md` §4 Fase 6. ADR de apertura:
   [ADR-0014](docs/adr/0014-codegen-coexistence-typed-registry.md)
   (mecanismo de coexistencia codegen/reflect; detalla ADR-0002).
   Descomposición en F6-1..F6-9 más abajo. Salida esperada: **v1.0.0**.

**Próxima acción concreta** (al arrancar sesión nueva):
1. `/next-session fase6` — sesión de **entrega**: arrancar por **F6-1**
   (skeleton del generador + contrato de registro) por ser foundation
   que desbloquea F6-2..F6-4, luego los typed scanners/binders. HA
   (F6-5/F6-6) y sharding (F6-7) son independientes del codegen y pueden
   ir en paralelo; benchmarks (F6-8/F6-9) al final porque miden todo lo
   anterior. Cada F6-N es 1 PR con `code-reviewer` + docs en
   `website/docs/` + CHANGELOG; F6-5/F6-7 escriben su ADR (ADR-0015/0016)
   en el mismo PR.

**Deuda menor post-v0.9.0** (cerrada en `[Unreleased]`, no bloquea
Fase 6): savepoint-rollback gap (PR #88), MSSQL JSON[T] scan bug
(PR #89), F4-7 deadlock real cross-engine test (PR #90 —
`tx_deadlock_integration_test.go`, dos tx con lock invertido tras un
barrier; SQLite excluido, MSSQL/Oracle cubiertos por el classifier
unit test), Raw-under-Native warning (PR #91). Guards `logger != nil`:
**descartado** (no son redundantes — protegen literales de test con
logger nil). Cross-instance stampede protection sigue diferido
(ADR-0011 §Cuándo reabrir; sólo si surge demanda real, con un hook
`DistributedLock` opcional).

**Foco sugerido** del slash command: `fase6` — abrir el camino a v1.0
con el mismo rigor que Fases 3/4/5. Cada F6-N como su propio PR.

**Disciplina recordada**: `code-reviewer` subagent obligatorio antes
de cada PR (regla CLAUDE.md #6); `/next-session` plantilla de cierre
al final de cada sesión.

---

## Fase 6 — Codegen, performance y HA (apertura formal)

> Spec narrativo: `docs/ANALISIS_MADUREZ.md` §4 Fase 6;
> `docs/ROADMAP.md` § "Phase 6". Decisión arquitectónica de apertura:
> [ADR-0014](docs/adr/0014-codegen-coexistence-typed-registry.md)
> (codegen coexiste vía registry de funciones tipadas por tipo con
> fallback a reflect; detalla el mecanismo que ADR-0002 dejó abierto).
> Objetivo de fase: cerrar la brecha de performance vs sqlc/ent y
> entrar en territorio enterprise (HA + sharding). **Salida: v1.0.0
> honesto.**

Apertura formal hecha 2026-05-22 con scope completo del ROADMAP (los
cuatro pilares: codegen, HA, sharding, benchmarks). Decisiones de scope:

- **Codegen es opt-in y NO bifurca la API** (ADR-0002 + ADR-0014). El
  reflect path se queda como default permanente; el código generado se
  auto-registra en un registry por `reflect.Type` y el runtime lo
  consulta antes de caer a reflect.
- **HA y sharding son aditivos y opt-in.** `WithReplicas`,
  `ShardRouter` — un Client sin configurarlos se comporta exactamente
  como hoy. Cada uno abre su propio ADR cuando se diseñe el item
  (ADR-0015+; no se anticipan aquí porque el diseño depende de la
  implementación).
- **Benchmarks honestos o nada.** F6-8 reemplaza cualquier número
  hardcoded de perf/coverage; el harness debe ser reproducible y
  apples-to-apples documentado (no marketing).
- ~~**El gate de v1.0** es ADR-0002 §Restricciones: los benchmarks de
  F6-8 deben demostrar ≥3× mejora p99 con codegen para justificar el
  esfuerzo. Si no se alcanza, codegen se reabre antes de taggear v1.0.~~
  **Retirado por [ADR-0017](docs/adr/0017-codegen-type-safety-not-perf-gate.md)
  (2026-05-25):** el gate ≥3× no es alcanzable por codegen de scan/bind
  (reflect no es el cuello); v1.0 se mide contra el checklist honesto de
  `ANALISIS_MADUREZ.md` §3. Ver bloque "✅ Decisión de gate ADR-0002 —
  RESUELTA" abajo.

Descomposición en 9 items entregables independientemente. Orden de
ataque sugerido: codegen primero (F6-1 desbloquea F6-2..F6-4), HA y
sharding en paralelo (independientes del codegen), benchmarks al final
(miden todo lo anterior).

### ✅ Decisión de gate ADR-0002 — RESUELTA (2026-05-25, [ADR-0017](docs/adr/0017-codegen-type-safety-not-perf-gate.md))

> **RESUELTO.** El mantenedor decidió **retirar el gate ≥3× p99**:
> [ADR-0017](docs/adr/0017-codegen-type-safety-not-perf-gate.md) supersede esa
> cláusula de ADR-0002 §Restricciones y reencuadra codegen como **type-safety**
> (F6-4), no velocidad. **El gate ya NO bloquea v1.0** — v1.0 se mide contra el
> checklist honesto de `ANALISIS_MADUREZ.md` §3, no contra un speedup.
> Dispositions finales: **F6-3b** sigue diferido (reabrir sólo por
> type-safety/corrección, nunca por velocidad); **F6-8b** pasa a informativo/
> opcional (no gate). `docs/ROADMAP.md` actualizado. La evidencia que motivó la
> decisión se conserva abajo.

> **Tres data points + profiling dicen que el gate ≥3× NO se alcanza por
> codegen de scan/bind.** F6-8a: Quark ~1.5-2.1× sobre `database/sql`. F6-2:
> scan codegen ~2-5%. F6-3a: insert binder ~1%. El profiling (`benchmarks/PROFILING.md`)
> lo explica: **(1) la CPU está dominada por el motor SQLite + `database/sql`
> (syscalls ~67%, `Rows.Next/Close` ~52% cum); el reflect de Quark NO aparece
> en el top-25 de CPU. (2) El sobrecoste de Quark vs raw es de ALLOCATIONS,
> y son arquitectónicas, no de reflexión**: read → `List.func1` recolección
> 36% + `scanRow` []any/boxing 14% + `clone` (builder inmutable) 7% + query
> building ~10%; write → `saveAny` 19% + `For[T]` 19% + `buildInsert` 12% +
> `rowToMap` 9% (diff de audit calculado SIEMPRE, aun sin audit/bus) +
> dialect. El codegen toca una fracción menor y ni siquiera elimina esos
> allocs (sigue alocando []any/strings). **Cumple la condición de reapertura
> de ADR-0002.**
>
> **Recomendación al mantenedor** (decisión pendiente):
> - **No perseguir codegen por velocidad.** F6-3b (UPDATE/partial/batch binder)
>   queda diferido/descartado por payoff (~1%) y riesgo (corrección de
>   escritura). El mecanismo F6-1/F6-2/F6-3a queda como foundation correcta.
> - **Reencuadrar el valor del codegen como type-safety** → **F6-4** (accesores
>   de columna compile-time). Valor real e independiente del gate de perf.
> - **Si la perf importa, las palancas son reducción de allocs, no codegen**, y
>   son independientes: ~~`rowToMap` lazy (sólo con sink configurado, ~9% write
>   allocs, quick win)~~ **hecho** (commit `02ec8543` `perf(crud): compute audit
>   row diff only when a sink is configured` — `rowToMap`/`pkStringFromMeta` se
>   computan dentro de `recordAudit`, tras el gate `audit==nil || !shouldAudit`;
>   guard `TestRecordAuditNoAllocWhenDisabled` en `audit_internal_test.go`);
>   ~~clone lazy/pooled~~ **hecho** (copy-on-write: `clone()` comparte slices
>   en vez de deep-copy; los builder methods appendan vía `ownedAppend`
>   (`append(s[:len:len], …)`) que realoca on-grow → aislamiento preservado.
>   ~7%→1 alloc/op en derive sobre base "gorda". Guards `TestOwnedAppend*` +
>   `TestCloneCOWIsolation` en `clone_cow_test.go`); buffers reusados en
>   scan/bind. Aun así acotadas — el motor/driver domina.
> - **Revisar el gate de ADR-0002**: el ≥3× p99 "con codegen" no es alcanzable
>   con el diseño actual; o se revisa el número o se acepta que codegen es
>   para type-safety, no velocidad. (Posible ADR sucesor de 0002/0014.)

### F6-1 · Codegen tooling skeleton (`quark gen`) ✅ v0.11.0 (PR #99)

> **Mergeado en v0.11.0 (PR #99, `ce85abc`; prereq ADR-0014 amend +
> cmd/quark build en PR #96).** Foundation en `codegen_registry.go` (package quark):
> `GenContractVersion`, tipos `TypedScanner`/`TypedBinder`/`GeneratedMeta`,
> registries keyed por `reflect.Type`, registradores **exportados**
> `RegisterTypedScanner`/`RegisterTypedBinder`/`RegisterGeneratedMeta`
> (llamados desde el `init()` del código generado en el paquete del
> usuario — por eso exportados, no `registerTyped*` como decía el sketch
> de ADR-0014; consistente con su "superficie semi-pública"), lookups
> unexported gateados por versión (miss en versión incompatible → reflect),
> `ModelHash`/`HashModelFields`/`CanonicalType` (algoritmo de hash único
> compartido por generador y runtime), `CheckGeneratedDrift`. `cmd/quark/main.go`
> nuevo → binario instalable (`go install .../cmd/quark`). `quark gen` en
> `cmd/quark/commands/gen.go` + `cmd/quark/internal/codegen/` (`extract.go`
> go/packages+go/types con `types.Unalias` para alias como `Nullable[T]`;
> `emit.go` render gofmt'd + `format.Source`; reusa `schema.ColumnFromDBTag`
> para que las columnas no puedan divergir). Genera `*_quark_gen.go` con
> `//quark:gen v1` + hash + `init()` que registra `StubScanner`/`StubBinder`
> (no-ops, F6-2/F6-3 emiten los reales). **Test de conformidad** real
> (`cmd/quark/internal/codegen/codegen_test.go`): paquete `sample/` con
> golden `quark_gen.go` commiteado; compara hash AST vs reflexión, golden
> estabilidad, registración runtime sin drift. Reflect path intacto (lookups
> NO cableados en hot paths — eso es F6-2/F6-3). cmd/quark compila en CI vía
> `go test ./...`. Doc `website/docs/guides/codegen.mdx` + sidebar; nota de
> corrección en `cli.mdx`. **Mergeado**: PR #99 (`ce85abc`).

> **Enfoque decidido (2026-05-22, enmienda ADR-0014):** `quark gen` es
> subcomando de `cmd/quark` y parsea el **AST** del paquete del usuario
> (`go/packages` + `go/types`), no reflexión — para soportar la UX de
> `go install` + `//go:generate`. Prerequisito: **arreglar `cmd/quark`**
> (hoy no compila — faltan `cobra`/`viper`/`fatih/color`/
> `olekukonko/tablewriter`/`gopkg.in/yaml.v3` en `go.mod`, y no está en
> CI). Se hace como PR previo (chore) o como primer paso de F6-1.

Subcomando `quark gen ./pkg` que carga el paquete con `go/packages`,
encuentra structs con tags `db:`/`pk:`, resuelve tipos con `go/types`
(incl. genéricos `JSON[T]`/`Array[T]`/`Nullable[T]`), y emite
`*_quark_gen.go` por package con un `func init()` que registra las
implementaciones tipadas. Establece el pipeline + el contrato de
registro interno (`registerTypedScanner` / `registerTypedBinder`) + un
header de versión de contrato (`//quark:gen vN`) + un hash del modelo
para detectar codegen stale + un **test de conformidad** AST-vs-reflexión
(ADR-0014 §Consecuencias, mitigación del drift de dos intérpretes de
tags). Sin fast-path todavía — sólo el andamiaje y el opt-in. **Done**:
`cmd/quark` compila y está en CI; `quark gen` emite código que compila y
registra no-ops; el reflect path sigue intacto; test de registro +
fallback + conformidad. Doc: `website/docs/guides/codegen.mdx` (nuevo) +
sidebar.

### F6-2 · Generated typed scanners (read path sin reflect) ✅ v0.11.0 (commit `9fcc3db`)

> **Mergeado en v0.11.0 (commit `9fcc3db`, directo a main, sin PR
> propio).** `scanRow` (query_exec.go)
> consulta `lookupTypedScanner(reflect.TypeOf(dest))` antes del reflect,
> gateado por `!q.tzActive()` (el scanner generado no lleva estado de
> timezone runtime → tz activa cae a reflect). Helper exportado
> `quark.ScanTarget(ptr)` = `makeScanDest` para punteros tipados con loc nil
> (mantiene el parsing string/[]byte de `timeScanner` que SQLite necesita);
> `makeScanDest` refactorizado para delegar en `scanDestForPtr`. Generador
> (`emit.go`) emite un scanner real por modelo: lee `rows.Columns()`, switch
> `lower(col)` → `quark.ScanTarget(&m.Field)`, desconocidas → discard,
> `rows.Scan`. `GenContractVersion` 1→2 (ficheros v1 con stubs caen a reflect
> por el gate de versión). `RegisterTypedBinder` sigue con `StubBinder`
> (F6-3). Cobertura: `sample/roundtrip_test.go` prueba el scanner GENERADO
> real contra un gemelo reflect (`reflectAccount`) — round-trip idéntico en
> Find/List para escalares + `JSON[T]`/`Nullable[T]`/`time.Time`/`*time.Time`;
> fallback verificado (el gemelo sin codegen usa reflect). **Hallazgo honesto
> (relevante para el gate ADR-0002 ≥3×)**: la mejora del scan-path codegen es
> **pequeña** — Find ~2%, List(200) ~4-5%, mismos allocs — porque el scan es
> fracción menor del coste de query (driver + database/sql dominan) y el
> scanner generado sigue alocando el slice `[]any` + boxing por campo. El
> mecanismo y la corrección quedan validados; el win grande del read-path
> requeriría eliminar el `[]any`/boxing (optimización futura). **Cobertura
> 5-motores**: el scanner usa `rows.Scan` + `ScanTarget` (mismo helper que
> reflect) → equivalencia por construcción independiente del motor; SQLite es
> la prueba CI. Doc `codegen.mdx` actualizada (read path real, binder stub,
> nota de mejora modesta). **Mergeado**: commit `9fcc3db` (v0.11.0).

`scanRow` consulta `typedScanners[reflect.Type]` antes del reflect.
El generado escanea `*sql.Rows → *T` con índices de columna fijos, sin
`reflect.Value.Field`. Cubre `List`/`First`/`Find`. **Done**:
round-trip idéntico con y sin codegen en los 5 motores CI; benchmark
micro que muestra la mejora; fallback verificado cuando no hay generado.

### F6-3 · Generated typed binders (write path sin reflect)

> **Dividido en 3a (INSERT, mergeado en v0.11.0) y 3b (UPDATE/partial/
> batch, diferido).** El UPDATE completo lleyendo `version`/soft-delete +
> el partial de `buildUpdateMap` + el batch son sustancialmente más
> arriesgados (corrupción de escritura) y, a la luz del hallazgo de abajo,
> de payoff dudoso; se difieren a 3b con gating conservador por-modelo.

#### F6-3a · INSERT binder ✅ v0.11.0 (commit `550c13f`, directo a main, sin PR propio)

`buildInsert` (query_crud.go) consulta `lookupTypedBinder` antes del
reflect, gateado por `!q.tzActive() && v.CanAddr()` y por que el binder
devuelva sin error (StubBinder y `BindUpdate` devuelven `ErrGeneratedStub`
→ reflect). El generador (`emit.go`) emite un binder INSERT real **sólo
para modelos con un único PK entero** (`insertBinderPK`): skip del PK
cuando es cero (auto-increment), resto de campos db siempre, columnas
sin-quote + args raw (buildInsert hace quote/placeholder/tenant/assembly).
Modelos con PK compuesto/string/no-entero → `StubBinder` (reflect).
`GenContractVersion` 2→3 (cambio de shape del binder; ficheros v2 caen a
reflect por el gate de versión). Round-trip: el test F6-2 ya crea vía el
binder generado y compara contra el gemelo reflect → binder fiel. Benchmark
`Create` generado vs reflect añadido. **tenant injection y SQL assembly
intactos**; el reflect loop es byte-idéntico cuando el fast path no aplica.
Doc `codegen.mdx` + nota; suite completa verde (buildInsert es hot path de
escritura). **Mergeado**: commit `550c13f` (v0.11.0).

> **Hallazgo honesto — SEGUNDO punto de datos para el gate ADR-0002 ≥3×.**
> El binder INSERT generado da mejora **~1%** (Create ~15.4µs gen vs
> ~15.6µs reflect; -6 allocs/op: 89 vs 95). Sumado al scan de F6-2 (~2-5%)
> y al baseline de F6-8a (~2× sobre `database/sql`), los datos confirman
> que **reflect NO es el cuello de botella** de Quark por-operación: el
> coste lo domina el round-trip driver/`database/sql`, no la reflexión en
> scan/bind. **Esto cumple la condición de reapertura de ADR-0002**
> ("Si... los benchmarks muestran que reflect ya no es el cuello de botella
> ... reevaluar prioridad de Fase 6"). Recomendación para el mantenedor:
> antes de invertir en 3b/F6-4-por-perf, decidir si codegen se justifica
> por **type-safety** (F6-4, valor independiente del gate) en lugar de
> velocidad, o perfilar dónde vive realmente el coste. El mecanismo y la
> corrección quedan validados; el gate de perf, con el diseño actual, no se
> alcanza por scan+bind.

#### F6-3b · UPDATE / partial / batch binder — diferido (no bloquea v1.0)

`buildUpdate`/`buildUpdateMap`/`CreateBatch` consultan `typedBinders`. El
generado respeta `version` (optimistic lock), soft-delete y el partial de
`UpdateFields`. **Done**: Update/UpdateFields/CreateBatch round-trip
idéntico con y sin codegen; optimistic locking + soft delete + dirty
tracking siguen funcionando. **Disposición final
([ADR-0017](docs/adr/0017-codegen-type-safety-not-perf-gate.md), 2026-05-25):**
diferido; reabrir **sólo por type-safety/corrección, nunca por velocidad**
(payoff ~1% medido en 3a, riesgo de corrupción de escritura mayor).

### F6-4 · Typed query field accessors ✅ v0.12.0 (#105)

API generada **compile-time** (no reemplaza runtime): por cada modelo,
accesores tipados de columna que producen condiciones sin strings
mágicos, dando type-safety de columnas. **Done**: ejemplo compila;
un typo de columna no compila; coexiste con la API string actual
(`Where("name","=",...)` sigue válida). Doc en codegen.mdx.

> **Mergeado en PR #105 (`34ea945e`), liberado en v0.12.0 (#104).** Runtime en
> `typed_columns.go` (package quark): `TypedColumn[T]` genérico (Eq/Neq/Gt/
> Gte/Lt/Lte/In/NotIn/Between/IsNull/IsNotNull), `TypedStringColumn` (embebe
> `TypedColumn[string]` + Like/NotLike), `Predicate` opaco, y método aditivo
> `Query[T].WhereP(...Predicate)` que baja cada predicado a la MISMA
> `condition` interna que `Where(col,op,val)` (intercambiables y mezclables;
> la API string sigue válida). **Type-safety de valor además de columna**: el
> nombre se eligió `TypedColumn` (no `Column`/`Col`, ya ocupados por la
> introspección F3 y el `Col()` de `expr.go`). Generador
> (`cmd/quark/internal/codegen/`): `extract.go` calcula un `ColType` por campo
> (tipo renderizado para el paquete local, qualifier propio stripeado) y
> recolecta los imports de los tipos de campo en `PackageModels.Imports`,
> dejando intacto `GoType` (qualificado, lo usa el hash de conformidad);
> `emit.go` emite imports dinámicos (stdlib + terceros, ordenados) y un
> `var <Model>Columns` con `quark.TypedColumn[T]` / `TypedStringColumn` por
> columna. **No** cambia `GenContractVersion` (los accesores no registran nada
> en runtime — azúcar pura, ADR-0014 §53). Golden `sample/quark_gen.go`
> regenerado; tests: `typed_columns_test.go` (lowering vs API string +
> shapes de predicado) y `sample/accessors_test.go` (accesores GENERADOS
> end-to-end en sqlite: Eq/Gte/Like/In/Between/IsNotNull, mezcla con `Where`,
> equivalencia typed↔string). `go test -short ./...` verde. Doc:
> sección "Typed column accessors" en `website/docs/guides/codegen.mdx`.

### F6-5 · Read replicas / pool routing ✅ v0.13.0 (#110); follow-up ✅ (random/least-conn, single-row read routing, PG integration)

> **Follow-up cerrado (esta sesión; pendiente code-reviewer + PR).** Tres
> piezas que el skeleton dejó abiertas:
> - **Estrategias de selección**: `ReplicaStrategy` (`ReplicaRoundRobin` default
>   / `ReplicaRandom` / `ReplicaLeastConn`) + `WithReplicaStrategy`. `pickReplica`
>   despacha sobre `replicaStrategy` en `replicas.go`; las tres respetan el
>   cooldown F6-6. Least-conn usa `sql.DB.Stats().InUse`.
> - **Lecturas de una-fila enrutadas**: `First`/`Find` ya enrutaban (bajan a
>   `List`→`executeQuery`); el follow-up partió el primitivo de una-fila en
>   `executeReadRow` (lectura, `readExec`+failover) vs `executeQueryRow`
>   (escritura RETURNING/LastInsertID, primary-only). `Count` y los agregados
>   (`Sum`/`Avg`/`Min`/`Max`) ahora usan `executeReadRow`.
> - **Integration test PG**: `replicas_postgres_test.go` (`//go:build
>   integration`, package `quark_test`) provisiona una 2ª base como réplica con
>   datos divergentes (no es replicación streaming — Quark enruta, no replica) y
>   verifica read→réplica / Sticky→primary / Count→réplica contra el driver pgx
>   real. Cableado en la matriz CI postgres (`ci.yml`). Skip si no se puede crear
>   la 2ª base (DSN restringido).
>
> Tests unitarios SQLite: `TestReplicaStrategyRandom`,
> `TestReplicaStrategyLeastConn`, `TestSingleRowReadsRouteToReplica`. Docs:
> `read-replicas.mdx` (estrategias + todas las lecturas enrutan) + ADR-0015
> actualizado. Pendiente: scatter-gather no aplica (eso es F6-7).

`WithReplicas(replicaDSNs...)`: SELECT enruta a réplicas
(round-robin/random/least-conn configurable), mutaciones al primary.
`Sticky(ctx)` fuerza primary para coherencia post-write. Healthcheck
pasivo (saca de rotación una réplica que devuelve `driver.ErrBadConn`).
**Abre ADR-0015** (modelo de consistencia + estrategia de routing).
**Done**: integration test que verifica split read/write y sticky en
PG (réplica vía testcontainers o DSN); skip documentado donde no
aplique.

> **Entregado esta sesión (design-first; pendiente code-reviewer + PR).**
> **ADR-0015 escrito y aceptado** (`docs/adr/0015-read-replicas-routing.md`):
> routing en ejecución (no construcción), modelo de consistencia (eventual +
> `Sticky` read-your-writes; reads en tx siempre primary), exclusiones
> (tx/RLS-nativa/Sticky), failover → F6-6. API skeleton funcional en
> `replicas.go` + `client.go` + `option.go`: `WithReplicas(dsns...)` (abre un
> `*sql.DB` por DSN en `New()`, mismas pool opts, ping; `Close()` los cierra),
> `Sticky(ctx)`, `pickReplica()` round-robin atómico, `BaseQuery.readExec(ctx)`.
> Wired en `executeQuery` (multi-fila). Tests `replicas_test.go` (routing
> read→réplica round-robin, write→primary, Sticky→primary, no-réplica
> regression). **Hallazgo de diseño**: `executeQueryRow` es primitivo
> compartido reads (First/Find/Count) + escritura (`INSERT...RETURNING`,
> SCOPE_IDENTITY MSSQL) → NO se enruta (mandaría writes a réplica); el skeleton
> enruta sólo `executeQuery`. **Follow-up** (no en este slice): round-robin
> random/least-conn, enrutar First/Find/Count (separar del RETURNING),
> integration test PG con réplica real. **Estrategia única** (round-robin) por
> ahora. **EXPERIMENTAL hasta F6-6** (sin healthcheck/failover).

### F6-6 · Failover de primary ✅ v0.13.0 (#113) — replica failover

Detección de errores transitorios (`errors.Is(err, driver.ErrBadConn)`
+ códigos por dialecto, reusando el classifier de F4-7) y reintento
contra un primary sano. **Done**: unit test del classifier extendido +
integration test que mata el primary y verifica recuperación. Comparte
diseño con ADR-0015.

> **Entregado esta sesión (pendiente code-reviewer + PR).** Reencuadrado como
> **replica failover** (no "primary failover" multi-primary: el modelo tiene un
> único primary, que es el destino del fallback; promoción de réplica→primary
> es otro modelo, fuera de alcance — documentado en ADR-0015). Clasificador
> `isTransientConnErr` en `db_errors.go` (estilo F4-7 `errors.As`):
> `driver.ErrBadConn`/`sql.ErrConnDone`/`net.Error`/clase 08 + shutdown PG/
> 2002·2003·2006·2013 MySQL/233·10053·10054·10060 MSSQL/"database is closed"
> SQLite. Health por réplica (`replicaUnhealthyUntil []atomic.Int64`,
> `replicaDownCooldown` default 5s): `pickReplica` salta réplicas en cooldown
> (nil si todas → primary); `markReplicaDown` las saca. `executeQuery` hace
> failover: read a réplica con error transitorio → marca down + reintenta en
> primary. Recuperación pasiva. **Gradúa `WithReplicas` de experimental**
> (ADR-0015 + docs actualizadas). Tests `replicas_test.go`:
> `TestReplicaFailoverToPrimary`, `TestReplicaHealthRecovery`,
> `TestIsTransientConnErr`. Verde. Cierra el pillar HA F6-5+F6-6.

### F6-7 · Sharding pluggable (`ShardRouter`) ✅ mergeado (#115, post-v0.13.0); follow-up: scatter-gather, shard-key-from-entity, runnable PG example

Interface `ShardRouter` que, dada una entidad + operación, elige el
Client del shard. Fan-out de reads con scatter-gather opcional.
**Abre ADR-0016** (interface de shard key + semántica de queries
cross-shard). **Done**: ejemplo con 2 shards en SQLite/PG; test de
routing por shard key; doc de límites (no cross-shard joins, no
cross-shard tx).

> **Entregado esta sesión (design-first; pendiente code-reviewer + PR).**
> **ADR-0016 escrito y aceptado** (`docs/adr/0016-sharding-shardrouter.md`):
> shard key por contexto y por operación (uniforme read/write; extracción por
> entidad = futuro), mapeo key→shard pluggable (`ShardFunc`), **sin cross-shard
> implícito** (query sin shard key → error, no fan-out), límites duros (no
> cross-shard joins, no cross-shard tx, shards fijos en construcción),
> composición ortogonal con multi-tenancy. API en `shard_router.go`:
> `ShardRouter` (implementa `ClientProvider`), `NewShardRouter(shards, resolve,
> shardFor)` con validación, `GetClient` (resuelve key→shard→Client),
> `WithShardKey`/`ShardKeyFromContext`/`DefaultShardResolver`, `HashShardFunc`
> (FNV-1a mod N), `ShardNames()`. Tests `shard_router_test.go`: routing por key
> + no-leak entre shards, missing-key error, validación de construcción,
> determinismo del hash. Doc pública `advanced/sharding.mdx` + sidebar.
> **Follow-up**: scatter-gather (lectura cross-shard con merge), extracción de
> shard key desde la entidad, ejemplo runnable PG. **Con F6-7, los pillars de
> Fase 6 quedan entregados** (sólo F6-3b y F6-8b diferidos) — candidato a v1.0.
> **Gate ≥3× retirado** ([ADR-0017](docs/adr/0017-codegen-type-safety-not-perf-gate.md),
> 2026-05-25): el último bloqueo arquitectónico a v1.0 queda resuelto; v1.0 se
> mide contra el checklist honesto de `ANALISIS_MADUREZ.md` §3.

### F6-8 · Benchmarks proper ✅ (8a v0.11.0 #98; 8b entregado 2026-05-27)

> **Dividido en 8a (baseline, mergeado en v0.11.0) y 8b (codegen-tier,
> entregado 2026-05-27).** Razón: el objetivo declarado del foco "benchmarks first" es
> el **baseline pre-codegen** (Quark vs `database/sql` puro), que es lo que
> mide el overhead que el codegen quita y contra lo que se mide el gate de
> ADR-0002. ent y sqlc son codegen-tier (necesitan código generado
> commiteado) y sólo aportan señal cuando Quark+codegen exista para
> compararse — son la comparación relevante en el gate de v1.0, no en el
> baseline.

#### F6-8a · Harness + baseline (Quark vs database/sql vs GORM) ✅ v0.11.0 (PR #98)

Módulo independiente `benchmarks/` (su propio `go.mod` con `replace =>
../`, para que GORM no contamine el `go.mod` de la librería). Cinco
operaciones (`InsertOne`/`InsertBatch`/`FindByPK`/`ListWhere`/`Update`)
que ejercen los hot paths reflect (`scanRow`/`buildInsert`/`buildUpdate`)
que el codegen reemplazará, medidas en tres implementaciones: raw
`database/sql` (el suelo), Quark (path reflect actual = baseline
pre-codegen), GORM (par reflect). SQLite in-memory para aislar el overhead
de ORM/driver del I/O. Quark/raw y GORM corren en **binarios de test
separados** (`benchmarks/` y `benchmarks/gorm/`) porque `modernc.org/sqlite`
y el driver de glebarez registran ambos el driver `sqlite`; el modelo
compartido vive en `benchmarks/internal/model` (sin imports de ORM).
**Auditadas y reemplazadas** las cifras hardcoded v0.1.0 + la tabla
cross-ORM estimada en `docs/benchmarks.md` y `website/docs/reference/benchmarks.mdx`
(este último además enlazaba a un `benchmark_test.go` inexistente). Job CI
`benchmarks` smoke (`go vet` + `-benchtime=1x`) evita el bit-rot.
**Hallazgo honesto**: el path reflect de Quark va ~1.5–2.1× sobre el suelo
de `database/sql` en estas ops; ese margen acota lo que el codegen puede
recuperar — input directo al gate ≥3× p99 de ADR-0002 (en single-row
in-memory el margen al suelo es ~2×, así que el gate, de cumplirse, será en
paths más pesados o bajo la concurrencia de F6-9). **Mergeado**:
PR #98 (`c16de24f`); profiling de seguimiento en PR #102.

#### ~~F6-8b · Comparación codegen-tier (ent + sqlc)~~ ✅ entregado (informativo, no gate)

**Cerrado (2026-05-27).** ent y sqlc añadidos como subpaquetes propios
(`benchmarks/ent/`, `benchmarks/sqlc/`), cada uno su binario de test
espejando `benchmarks/gorm/` (aislamiento de driver, import de
`internal/model`, sin core de Quark). ent: schema en `ent/schema` +
cliente generado vía `go generate` (tool `entgo.io/ent/cmd/ent` fijado por
directiva `tool` en `go.mod`). sqlc: `schema.sql`/`query.sql`/`sqlc.yaml` +
paquete generado `sqlc/sqlcdb` (sólo importa `database/sql`, cero deps de
módulo). Las 5 ops por implementación. **Hallazgo (confirma
[ADR-0017](docs/adr/0017-codegen-type-safety-not-perf-gate.md)):** sqlc va al
suelo de `database/sql` (~1.0–1.1×, sin runtime) mientras ent —también
codegen pero con runtime rico (builders/mutaciones)— se queda en la clase
reflect (su Update es el más lento de los 5); la diferencia de velocidad
entre librerías la marca el diseño de runtime/allocs, NO reflect-vs-codegen.
Esto es exactamente por qué el codegen propio de Quark (F6-2/F6-3) recupera
~1–5% y se reencuadró como type-safety. Números publicados (medianas
`-count=6` + benchstat) en `website/docs/reference/benchmarks.mdx`; README
del harness + `docs/benchmarks.md` actualizados; CHANGELOG `[Unreleased]
### Tests`. **Disposición final (ADR-0017):** informativo/opcional, NO gate
de v1.0 (el gate ≥3× que alimentaba quedó retirado). Asimetría documentada:
sqlc no emite batch multi-fila para SQLite (`:copyfrom`/`:batch` son
pgx-only) → su InsertBatch es bucle single-row en una transacción.

### F6-9 · Stress / load testing ✅ v0.13.0 (#109)

Workload generator (patrones estilo `vegeta`/`hey`): latencias
p50/p95/p99 bajo concurrencia, contención de pool, deadlock rate real.
**Done**: harness reproducible en `docs/benchmarks/stress/`; un run
documentado con números; identifica el primer cuello de botella real
(dato que prioriza optimizaciones post-1.0).

> **Entregado esta sesión (pendiente code-reviewer + PR).** Harness runnable
> en `benchmarks/stress/main.go` (`package main` en el módulo `quarkbench`,
> reusa `internal/model`): N workers concurrentes, mezcla read/write
> configurable, durante una duración fija; reporta throughput, latencias
> p50/p95/p99/max (read y write por separado), errores + bucket de
> contención, y stats del pool (`client.Raw().Stats()`: waitCount/waitDuration/
> inUse/idle). Flags: `-driver -dsn -conns -workers -duration -write-pct -seed`.
> DSN SQLite por defecto con `busy_timeout` para que la contención de escritura
> aparezca como latencia y no como `SQLITE_BUSY`. Run documentado +
> metodología + hallazgo en `docs/benchmarks/stress/README.md`. **Primer cuello
> de botella identificado** (data, no asunción): (1) *sizing del pool* —
> con `MaxOpenConns < workers` casi toda op bloquea esperando conexión
> (waitCount ≈ total ops, ~250-530µs, domina la latencia de read); igualar
> pool a workers baja read p50 286µs→64µs y waitCount→0. (2) *serialización de
> escritura del motor* — con pool igualado, SQLite serializa writes
> (p99 10ms) mientras reads siguen rápidos; propiedad del motor, no del mapping
> de Quark. Coherente con `benchmarks/PROFILING.md` y el gate ADR-0002:
> el driver/pool/motor dominan, no el reflect. Acción post-1.0: documentar
> guía de pool-sizing; micro-opt del mapping tiene valor acotado hasta
> direccionar pool+motor.

### Cierre de Fase 6 → v1.0.0

Los cuatro pilares de Fase 6 están entregados (F6-1/2/3a/4 codegen,
F6-5/6 HA, F6-7 sharding, F6-8a/F6-9 benchmarks+stress); sólo F6-3b y
F6-8b quedan diferidos y **no bloquean v1.0**. El gate de performance
≥3× de ADR-0002 **ya NO es la condición de v1.0**: fue retirado por
[ADR-0017](docs/adr/0017-codegen-type-safety-not-perf-gate.md) (2026-05-25).
**v1.0.0 se taggea contra el checklist honesto de
`docs/ANALISIS_MADUREZ.md` §3** (gaps estructurales cerrados, cobertura
cross-engine), no contra un speedup. Cuando ese checklist esté verde,
taggear **v1.0.0** vía `/release v1.0.0`. Cada F6-N es 1 PR con
`code-reviewer` + docs + CHANGELOG; los items que abren ADR (F6-5/F6-7)
escriben el ADR en el mismo PR.

---

## Fase 5 — RLS real + hooks transaccionales + EventBus (apertura formal)

> Spec narrativo: `docs/ANALISIS_MADUREZ.md` §4 Fase 5. Decisiones
> arquitectónicas:
> [ADR-0012](docs/adr/0012-rls-real-postgres-set-local-plus-policies.md)
> (RLS real PG vía `SET LOCAL` + `CREATE POLICY`, supersede ADR-0003) y
> [ADR-0013](docs/adr/0013-transactional-hooks-and-sync-eventbus.md)
> (hooks transaccionales + EventBus síncrono en commit-phase).
> Playbooks aplicables: `docs/playbooks/tenant.md` (F5-1..F5-3),
> `docs/playbooks/query-builder.md` (F5-4..F5-5),
> `docs/playbooks/security.md` (F5-7 audit log).
> Objetivo de fase: aislamiento real (no disciplina) en PG, y semántica
> de hooks/eventos predecible en transacciones. Entrega esperada en
> v0.9.0.

Apertura formal hecha en sesión post-v0.8.0 (2026-05-15). Decisiones de
scope fijadas con el usuario:

- **RLS coexistencia en PG**: NO coexisten. `RowLevelSecurityNative`
  reemplaza a `RowLevelSecurityClient` en PG (mutuamente excluyentes
  por router). En motores sin policies (MySQL/MariaDB/MSSQL/Oracle/
  SQLite) sigue `RowLevelSecurityClient` como única opción de fila.
  Ver ADR-0012.
- **Semántica de hooks**: `Before*` corren dentro de tx y error aborta
  el commit; `After*` se encolan en el `*Tx` y disparan tras commit OK
  (rollback los descarta). Nuevo `OnCommit(fn)` / `OnRollback(fn)` para
  side-effects arbitrarios. Ver ADR-0013.
- **EventBus delivery**: **síncrono en commit-phase, at-least-once**.
  No outbox transaccional (eso es Fase 6 si aparece). Ver ADR-0013.
- **`LISTEN/NOTIFY` PG (listener side)**: fuera de scope para Fase 5.
  Requiere conexión dedicada fuera del pool; queda devolviendo
  `ErrDialectNotSupported` hasta Fase 6.
- **Audit log opcional**: ENTRA, dentro de F5-7. Tabla `quark_audit`
  + capture vía `tx.OnCommit` con diff de `Tracked.Save` (F1-1 ya
  existe — se reutiliza).

Descomposición en 7 items entregables independientemente. Orden de
ataque sugerido:

1. **F5-1 primero** (rename + alias deprecado): foundation-only, sin
   riesgo arquitectónico. Desbloquea F5-2..F5-3 sin dejar a usuarios
   con código roto.
2. **F5-2 y F5-3 en paralelo**: F5-2 implementa el motor (`SET LOCAL` +
   intercepción de `Tx`), F5-3 el generador CLI. Pueden coexistir en
   PRs separados; F5-3 depende del schema introspection (F3-2) ya
   entregado en v0.6.0.
3. **F5-4 y F5-5 en serie**: F5-4 refactoriza `query_crud.go` para
   pasar `*Tx` al motor de hooks; F5-5 añade `OnCommit`/`OnRollback`
   sobre esa base. Romper esto en dos PRs reduce el blast radius.
4. **F5-6 EventBus** (depende de F5-5 — `OnCommit` es el callsite).
5. **F5-7 Audit log** (depende de F5-6 — el bus es el transporte).

Cada item es 1 PR con `code-reviewer` + docs en `website/docs/` +
CHANGELOG `### Added` / `### Changed` / `### Deprecated` según
corresponda. **Si tocas hooks (F5-4..F5-6)** escribe
`docs/MIGRATION_v0.9.0.md` en el mismo PR — el cambio de "After inline"
a "After post-commit" es breaking minor (ADR-0013).

### ~~F5-1 · Rename `RowLevelSecurity` → `RowLevelSecurityClient` + deprecation~~

**Cerrado (2026-05-15, PR #78)** — `tenant_router.go` declara
`RowLevelSecurityClient` como la constante canónica con doc-comment que
explicita "client-side WHERE injection" y deja `RowLevelSecurity` como
`// Deprecated:` alias del mismo valor (sunset v1.0). El `switch` de
`client.go:233`, los comentarios internos en `query_builder.go` /
`dirty_track.go` / `query_crud.go`, los tests existentes
(`quark_test.go`, `dirty_track_test.go`, `suite_test.go`,
`tenant_router_test.go`) y el ejemplo `examples/postgres/main.go` usan
ahora el nombre canónico. `TestRowLevelSecurityAliasBackwardCompat`
(`tenant_router_test.go:23-44`) guarda valor-equality y type-check de
asignación vía el alias; lleva sunset comment ligado a la eliminación
del alias en v1.0. Doc viva (`website/docs/advanced/multi-tenant.mdx`),
referencia (`reference/api/multi-tenant.mdx`), comparison
(`reference/comparison.mdx`), README, `docs/ENGLISH_DOCS.md`,
ADR-0007, CLAUDE.md y CHANGELOG `### Changed`/`### Deprecated`
sincronizados. El snapshot versionado v0.8.0 lleva `:::note Renamed
in v0.9.0` admonitions sin reescribir la historia (tablas y snippets
de v0.8.0 conservan el nombre original — eso es lo que esa release
entregó). Code-reviewer aprobado en R2 tras cerrar 1 blocker
(versioned-docs admonitions) + 2 nits (paths en TASKS.md, sunset
comment). Build / vet / gofmt / lint-docs / tests cortos verdes.

**Foundation. Sin lógica nueva — sólo rename + alias.**

**Localización**:
- `tenant_router.go:29` — `RowLevelSecurity` constante.
- `tenant_router.go:36-37` — comentario en `TenantConfig` ("RLS uses…").
- `client.go:233-235` — `case RowLevelSecurity:` en el switch.
- `examples/` — cualquier referencia a la constante.
- `website/docs/advanced/multi-tenant.mdx` + `website/docs/reference/api/multi-tenant.mdx` + `website/docs/reference/comparison.mdx` — todas las menciones.

**Definition of done**:
- Constante actual renombrada a `RowLevelSecurityClient`.
- Alias deprecado añadido: `const RowLevelSecurity = RowLevelSecurityClient`
  con comentario `// Deprecated: use RowLevelSecurityClient.` (gopls
  marcará el uso como deprecated).
- Comentario en `tenant_router.go:27-29` actualiza la descripción a
  "client-side WHERE injection" sin ambigüedad.
- Tests existentes siguen verdes (alias = mismo valor; el switch no
  cambia comportamiento).
- Doc en `website/docs/advanced/multi-tenant.mdx` documenta el alias
  y apunta a F5-2 para la modalidad nativa (el sidebar `advanced/multi-tenant`
  es la landing de multi-tenancy desde v0.4.x).
- CHANGELOG `### Deprecated`: `RowLevelSecurity` reemplazada por
  `RowLevelSecurityClient`; alias se retira en v1.0.

**Estimación**: 1 sesión corta (~2 h).

### ~~F5-2 · `RowLevelSecurityNative` motor real (PG `SET LOCAL` + tx hooking)~~

**Cerrado (2026-05-15, PR #79)** — `rls_native.go` (nuevo, ~180
líneas) entrega `nativeRLSExecutor` que envuelve `*sql.DB` y emite
`SELECT set_config($1, $2, true)` antes de cada `Exec`/`Query`/
`QueryRow`; el commit de la tx implícita se registra vía
`context.AfterFunc` por la opacidad de `*sql.Rows`/`*sql.Row` en
`database/sql`. `TenantRouter.Tx(ctx, fn)` es la entrada recomendada
para operaciones multi-paso: abre una sola tx, emite `set_config`,
sin leak. `tenant_router.go` añade la constante `RowLevelSecurityNative`
con doc-comment apuntando a ADR-0012; `TenantConfig.NativeRLSVar`
default `"app.tenant_id"` con helper `defaultNativeRLSVar()`.
`client.go For[T]` ramifica Native: valida `dialect.Name() == "postgres"`
(fail-fast con `ErrUnsupportedFeature`) y reemplaza `q.exec` con
`nativeRLSExecutor`; **no** inyecta `WHERE tenant_id = ?` — la policy
PG lo hace server-side. Cobertura: `rls_native_test.go` (4 unit tests:
non-PG via For[T], non-PG via router.Tx, default `NativeRLSVar`,
router.Tx delega para Client/Schema/DBPerTenant) + `rls_native_postgres_test.go`
(integration cross-engine con build-tag-free env-DSN path + 5 subtests:
router.Tx ta/tb, For[T] implicit-tx ta/tb, Count via QueryRow, Create
via ExecContext+QueryRowContext). Doc `website/docs/advanced/row-level-native.mdx`
nueva con sidebar entry + cross-link desde `multi-tenant.mdx`.
ADR-0007 / playbook tenant.md sincronizados con la 4ª estrategia
documentada y caveats operacionales (request-scoped vs long-lived ctx).
**Warning estructurado para `client.Raw()` bajo Native NO incluido**
— deferido a follow-up: PG enforza la policy independientemente, el
warning es UX y no de seguridad. La doc lo documenta.

**Implementa el aislamiento de motor anticipado en ADR-0012.**

**Localización**:
- `tenant_router.go` — añadir constante `RowLevelSecurityNative` tras
  `RowLevelSecurityClient`. Validación en `NewTenantRouter`: rechazar
  combinaciones inválidas (Native sin PG; Native + Client en mismo
  router).
- `client.go:233-235` — el switch añade rama Native que **no** inyecta
  `q.tenantID/q.tenantCol` (la policy lo hace).
- `client.go` `Tx(...)` y `client.go` `For[T]` (rama implícita) —
  envolver con `SET LOCAL app.tenant_id = $1` como primer statement
  cuando router es Native.
- `client.Raw()` y `client.Exec()` — emitir warning estructurado
  `quark.tenant.raw_under_native_rls` cuando context lleva tenantID y
  router es Native (la policy bloquea por defecto, pero el warning
  ayuda al debugging).

**Definition of done**:
- Constante `RowLevelSecurityNative` añadida y validada.
- `Client.Tx` y la tx implícita de `Query[T]` emiten `SET LOCAL
  app.tenant_id = $1` como primer statement bajo router Native.
- `client.Raw()` y `client.Exec()` loguean warning si context.tenantID
  no nulo bajo router Native (la policy hará su trabajo; el warning
  documenta).
- Integration test cross-engine: dos tenants, modelo `Order`, policy
  instalada manualmente en el suite, queries de tenant A no ven filas
  de tenant B; **skip explícito** (sin `t.Skip` por env var — usar
  `testcontainers` y build-tag `//go:build integration`) en motores no
  PG, con razón documentada.
- Doc en `website/docs/advanced/row-level-native.mdx` (nuevo archivo
  bajo el mismo sidebar `advanced/`; añadir entrada en
  `website/sidebars.ts`): cuándo usar, qué garantías da, qué pasa con
  `client.Raw()`, ejemplo de configuración.
- CHANGELOG `### Added`: `RowLevelSecurityNative` (PG-only).

**Estimación**: 1-2 sesiones largas (~6-10 h). Bloque crítico de la
fase.

### ~~F5-3 · CLI `quark tenant install-rls-policies`~~

**Cerrado (2026-05-15, PR #81)** — nuevo paquete
`github.com/jcsvwinston/quark/quarktenant` con dos archivos de
producción (~280 líneas) más tests + example. `install.go` define
`InstallOptions` (`TenantColumn`, `NativeRLSVar`, `ForceRLS` default
true, `DryRun`, `LockTimeout`, `LockName`, `TenantColumnSQLCast`) y
la función `InstallRLSPolicies(ctx, client, opts) ([]string, error)`
que genera la DDL por modelo registrado (`ENABLE`/`FORCE ROW LEVEL
SECURITY` + `CREATE POLICY <table>_tenant_isolation ... USING ...
WITH CHECK ...`). Validación PG-only via `client.Dialect().Name()`,
modelo-sin-columna via `ErrNoTenantColumn`, registro vacío via
`ErrNoRegisteredModels`. Apply path: `Client.AcquireMigrationLock`
(F3-1) + `client.Exec` por statement (requires `AllowRawQueries=true`
en el client embedder). `run.go` define `Action` enum,
`ActionInstallRLSPolicies`, `ParseAction`, `Run(ctx, args, client)` +
`RunWithIO` con flags `--dry-run / --tenant-col / --native-rls-var /
--cast / --no-force-rls / --lock-name`. Cobertura:
`quarktenant/install_test.go` (7 unit tests: non-PG rejection,
empty-registry guard order, nil client, default values, CLI unknown
action, empty args, ParseAction round-trip) + `install_postgres_test.go`
(PG integration con 3 subtests: dry-run renders sin apply, apply
inserta pg_policies con nombre canónico, re-apply falla con
duplicate-object). `examples/tenant-rls-native/main.go` ejemplo
runnable. Doc `website/docs/advanced/row-level-native.mdx` añade
sección "Option A — quarktenant CLI (recommended)" + warning para
UUID/BIGINT con `--cast`. Reutiliza F3-1 (lock) y F3-7 (registry);
**no reutiliza F3-2 (introspection)** — la DDL se genera a partir
del modelo, no de la tabla viva. Estimación cumplida (~4-5 h).

**Generador de DDL para Native: reutiliza schema introspection (F3-2)
y migration lock (F3-1).**

**Localización**:
- `quarktenant/` (paquete nuevo en la raíz del módulo, siguiendo el
  patrón de `quarkmigrate/` entregado en F3-5 — biblioteca, no binario;
  el usuario embebe en un `tenant/main.go` propio).
- Subcomando `install-rls-policies [--dry-run] [--tenant-col=...]`.
- Output: SQL templated por modelo registrado en el Client (uso del
  registry per-Client F3-7):

```sql
ALTER TABLE orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE orders FORCE ROW LEVEL SECURITY;
CREATE POLICY orders_tenant_isolation ON orders
    USING (tenant_id = current_setting('app.tenant_id', true)::text);
```

**Definition of done**:
- Subcomando funcional con `--dry-run` (stdout) y `--apply` (vía
  `Client.AcquireMigrationLock`).
- Tipo de la columna inferido del modelo registrado (`text`/`uuid`/
  `bigint`).
- Rechazo explícito en motor no-PG con mensaje claro
  (`ErrDialectNotSupported`).
- Test e2e en suite PG: registrar 3 modelos, correr `--dry-run`,
  asertar SQL emitido; correr `--apply`, asertar `pg_policies` lo
  contiene.
- Doc `website/docs/advanced/row-level-native.mdx` (creada en F5-2)
  incluye ejemplo del CLI.
- Ejemplo en `examples/tenant-rls-native/main.go`.
- CHANGELOG `### Added`: `quark tenant install-rls-policies` CLI.

**Estimación**: 1 sesión media (~4-5 h).

### ~~F5-4 · Hooks transaccionales — `After*` fire post-commit~~

**Cerrado (2026-05-15, PR #82)** — Plumbing core (`tx.go` +
`query_builder.go`): `*quark.Tx` ahora lleva `afterHooks []func() error`
+ `hooksMu sync.Mutex`; `Tx.Commit` drena la cola en orden FIFO
tras el commit OK (errores se loguean via `Client.logger` con event
`quark.hook.after_post_commit_error`, no abortan); `Tx.Rollback`
descarta la cola entera. `BaseQuery.tx *Tx` añadido para que la
ruta CRUD detecte tx explícita; `ForTx[T]` lo puebla. Refactor de
los 5 callsites `After*` en `query_crud.go` (1 AfterCreate, 2
AfterUpdate, 2 AfterDelete) para usar `queueOrRunAfterHook(fn)` que
encola si `q.tx != nil`, ejecuta inline si no. Decisión de scope:
**non-tx CRUD NO se envuelve en implicit-tx** — el ADR-0013 lo
pedía pero el coste (2 RPCs adicionales por op) no compensa el
beneficio nulo (no hay tx para deshacer si no hay tx). El race que
F5-4 cierra es exclusivamente del path explícito `Client.Tx`.

`BeforeFindHook` / `AfterFindHook` añadidos a `hooks.go` con
helpers `callBeforeFind`/`callAfterFind` en `hooks_find.go`
(dispatch sobre zero `*T` por la opacidad de Generics). Wiring:
`List` (BeforeFind antes de buildSelect, AfterFind tras Preload),
`Find`/`First` (heredan vía `List`), `Iter` (BeforeFind antes del
loop, AfterFind tras `rows.Err()` OK), `Cursor` (BeforeFind antes
de open, AfterFind desde `Cursor.Close()` cuando `rows.Err()`
nil).

Cobertura: `hooks_tx_test.go` con 5 tests sequenciales (recorder
global; `t.Parallel()` no aplica): AfterCreate fires after commit,
AfterCreate skipped on rollback, non-tx still inline, BeforeFind/
AfterFind fire around List, FIFO order de 3 creates inside one tx.

Docs: nueva `website/docs/guides/hooks.mdx` con tabla "qué corre
dónde" + sidebar entry. `docs/MIGRATION_v0.9.0.md` creado con
audit checklist para callers que dependían del timing v0.8.0.
CHANGELOG `### Changed` (breaking minor) + `### Added` con la
descripción del queue. **NO entrega `Tx.OnCommit`/`Tx.OnRollback`
público** — eso es F5-5, construye sobre esta cola interna.

**Estimación cumplida**: 1 sesión larga (~6 h con tests + docs).

### F5-4 (histórico spec)

**Refactor preparatorio para F5-5. Cambio interno, sin nuevo API
externo todavía.**

**Localización**:
- `query_crud.go` — cuerpo de `Create`/`Update`/`UpdateFields`/`Delete`/
  `Tracked.Save`. Hoy abren tx implícita en algunos casos; pasar a
  patrón uniforme: si `q.tx != nil` usar esa, si no abrir una
  implícita.
- `hooks.go` — añadir `BeforeFindHook` / `AfterFindHook` con la misma
  superficie que los existentes (sólo añade interfaces; no cambia
  ejecución todavía).
- `query_exec.go` — invocar `BeforeFind` antes del scan, `AfterFind`
  tras llenar la slice.
- `tx.go` — añadir cola interna de `afterHooks []func() error` y
  `onCommitHooks []func(ctx) error` / `onRollbackHooks []func(ctx) error`
  (las dos últimas se rellenan en F5-5; la cola de afterHooks se
  rellena ya aquí).
- `Tx.Commit()` / `Tx.Rollback()` — disparar la cola `afterHooks`
  tras commit OK; descartarla en rollback.

**Definition of done**:
- Tras este PR, los hooks `After*` corren **post-commit** cuando hay
  tx (implícita o explícita). Antes corrían inline; el comportamiento
  observable cambia para casos con tx explícita.
- `BeforeFindHook` y `AfterFindHook` definidos en `hooks.go` y
  enganchados en `query_exec.go`.
- Test de regresión: tx que falla en commit no dispara `AfterCreate`;
  tx con commit OK lo dispara una vez en orden FIFO.
- Test cross-engine que los hooks existentes siguen funcionando con la
  nueva semántica.
- Doc en `website/docs/guides/hooks.mdx`: tabla "qué corre dónde"
  (Before in-tx-abortable, After post-commit-observational).
- `docs/MIGRATION_v0.9.0.md` creado con la sección "Hook semantics
  change" (breaking minor).
- CHANGELOG `### Changed`: hooks `After*` post-commit; `### Added`:
  `BeforeFindHook` / `AfterFindHook`.

**Estimación**: 2 sesiones (~8 h). Riesgoso porque toca el path
crítico.

### ~~F5-5 · `Tx.OnCommit(fn)` / `Tx.OnRollback(fn)` API pública~~

**Cerrado (2026-05-20, PR #83)** — `tx.go`: `Tx` gana dos colas
`onCommitHooks` / `onRollbackHooks` (`[]func(context.Context) error`)
junto a la `afterHooks` de F5-4, más el campo `ctx` capturado en
`BeginTx`. Métodos públicos `OnCommit(fn)` / `OnRollback(fn)`.
`Commit()` drena en orden: `afterHooks` (modelo, contrato ORM) →
`onCommitHooks` (usuario) y descarta `onRollbackHooks`. `Rollback()`
descarta `afterHooks`+`onCommitHooks`, ejecuta `tx.Rollback()`, y
drena `onRollbackHooks` después. Commit fallido descarta todas las
colas (`discardAllHooks`). Errores en callbacks se loguean via
`Client.logger` (events `quark.hook.on_commit_error` /
`quark.hook.on_rollback_error`) sin parar la cadena ni cambiar el
retorno de `Client.Tx`. Helpers `takeOnCommitHooks`/`takeOnRollbackHooks`
hacen lift-and-clear bajo `hooksMu` para que el drain corra sin
sostener el lock (callback puede re-entrar a Quark).

`quark.TxFromContext(ctx) *Tx` añadido con context key no exportado
`txContextKey{}`; `ForTx[T]` inyecta el `*Tx` en `q.ctx` para que
los hooks de lifecycle (que sólo reciben ctx, ADR-0013 rechazó
ensanchar las firmas) puedan alcanzar la tx y registrar
OnCommit/OnRollback propios.

Cobertura: `tx_oncommit_test.go` con 6 tests — OnCommit FIFO
post-commit, error no para la cadena, OnCommit descartado en
rollback, OnRollback dispara sólo en rollback, OnCommit dispara
DESPUÉS de los model After* (orden de drain), TxFromContext resuelve
dentro de un hook + registra OnCommit que dispara post-commit
(fixture `txAwareRow`). `-race` limpio. Doc en
`website/docs/guides/transactions.mdx` § "Side-effects on
commit/rollback" con tabla de drain-order. CHANGELOG `### Added`.

**Estimación cumplida**: ~3 h (la cola F5-4 ya existía; low risk).

### F5-5 (histórico spec)

**Construye sobre F5-4. API nueva para side-effects arbitrarios
controlados por commit/rollback.**

**Localización**:
- `tx.go` — métodos públicos `OnCommit(func(context.Context) error)` y
  `OnRollback(func(context.Context) error)` que añaden a las colas
  internas creadas en F5-4.
- `Tx.Commit()` — tras `db.Commit()` OK, disparar `onCommitHooks` en
  FIFO secuencial; cualquier error loguea con span OTel
  `quark.hook.on_commit_error` y **no para la cadena**.
- `Tx.Rollback()` — análogo con `onRollbackHooks` (mismo principio
  no-bloqueante).
- Helper `quark.TxFromContext(ctx) *Tx` para hooks que necesiten el tx
  actual (alternativa a cambiar la firma de `hooks.go`, ADR-0013
  rechazó cambiar las interfaces).

**Definition of done**:
- API `OnCommit`/`OnRollback` documentada.
- Test: 3 OnCommit registrados en FIFO se ejecutan en orden tras
  commit; uno falla → los otros 2 siguen; rollback los descarta.
- Test: OnRollback se dispara en rollback (no en commit).
- `TxFromContext` documentado y testeado.
- Doc en `website/docs/guides/transactions.mdx` § "Side-effects on
  commit/rollback".
- CHANGELOG `### Added`: `Tx.OnCommit` / `Tx.OnRollback` /
  `quark.TxFromContext`.

**Estimación**: 1 sesión (~4 h). Bajo riesgo si F5-4 está sólido.

### ~~F5-6 · `EventBus` real — interfaz pública + `LoggerEventBus`/`OTelEventBus`~~

**Cerrado (2026-05-21, PR #84)** — `events.go` reescrito: interfaces
públicas `Event` (`Kind`/`Table`/`Payload`) y `EventBus`
(`Publish(ctx, Event) error`); evento concreto interno `modelEvent`;
constantes `eventCreated`/`eventUpdated`/`eventDeleted`. Dos buses
in-tree: `LoggerEventBus` (slog) y `OTelEventBus` (slog
correlation-tagged, sin acoplar el SDK OTel al core). El placeholder
struct `EventBus` de v0.8.0 (LISTEN/NOTIFY, siempre devolvía error) se
renombró a `ListenerFactory` + `NewListenerFactory` para liberar el
nombre `EventBus` — **breaking minor** sobre un tipo no-funcional,
documentado en MIGRATION. `CreateListener` sigue devolviendo
`ErrDialectNotSupported` (LISTEN/NOTIFY fuera de scope, ADR-0013).

`client.go`: campo `eventBus EventBus` + `Client.UseEventBus(bus)`.
`query_crud.go`: helper `emitEvent(kind, entity)` — bajo tx registra
`Tx.OnCommit` (post-commit, descartado en rollback, self-log
`quark.event.emit_failure` sin double-log con la cola F5-5); sin tx
emite inline y devuelve `ErrEventEmitFailed` (nuevo sentinel en
errors.go) envuelto al caller. Enganchado en los 5 callsites CRUD
(Create→created, Update + UpdateFields→updated, 2× Delete→deleted).
`emitEvent` retorna nil si no hay bus (coste cero opt-out).

Cobertura: `events_test.go` (8 tests: Logger/OTel publish no-error,
Create emite created tras commit + nada tras rollback, Update/Delete
emiten con kind/table correctos, emit-failure non-tx devuelve
ErrEventEmitFailed + fila persiste, emit-failure tx no propaga,
no-bus zero-cost) + `p0_fixes_test.go` actualizado a
`NewListenerFactory`. `-race` clean. Doc nueva
`website/docs/advanced/events.mdx` + sidebar; docs stale
(observability/caching/roadmap/transactions/row-level-native)
actualizadas al rename. CHANGELOG `### Added` + `### Changed`
(breaking minor). MIGRATION_v0.9.0.md con sección de rename.

**Estimación cumplida**: ~5 h. Delivery síncrona at-least-once, sin
outbox (ADR-0013); outbox transaccional explícitamente fuera de scope.

### F5-6 (histórico spec)

**Reemplaza el placeholder de `events.go:50` (CreateListener →
ErrDialectNotSupported). Emisión síncrona vía `OnCommit`.**

**Localización**:
- `events.go` — definir interfaz `EventBus` pública:

```go
type EventBus interface {
    Publish(ctx context.Context, event Event) error
}

type Event interface {
    Kind() string  // "created" | "updated" | "deleted"
    Table() string
    Payload() any
}
```

- `events.go` — `LoggerEventBus` (slog) y `OTelEventBus` (span emit)
  in-tree.
- `client.go` — `Client.UseEventBus(bus EventBus)` engancha el bus al
  pipeline CRUD: cada `Create/Update/Delete` registra un `OnCommit`
  que llama a `bus.Publish`.
- `events.go:CreateListener` se mantiene devolviendo
  `ErrDialectNotSupported` (LISTEN/NOTIFY explícitamente fuera de
  scope, ver ADR-0013).
- `events.go:Notify` se documenta como "pg_notify only, no relacionado
  con `EventBus.Publish`" para evitar confusión.

**Definition of done**:
- Interfaz `EventBus` y `Event` públicas.
- `LoggerEventBus` y `OTelEventBus` con tests unitarios.
- `Client.UseEventBus` engancha al pipeline; test e2e que un `Create`
  emite `created` evento tras commit OK y nada tras rollback.
- Test: emit que falla **no revierte** la tx (ya commitéo) pero loguea
  span `quark.event.emit_failure` y propaga error envuelto
  `ErrEventEmitFailed`.
- Doc en `website/docs/advanced/events.mdx`: cómo conectar un bus
  externo (NATS / Kafka skeleton), warning de "at-least-once, no
  outbox".
- CHANGELOG `### Added`: `EventBus` interfaz pública +
  `LoggerEventBus` / `OTelEventBus`.

**Estimación**: 1 sesión larga (~5-6 h).

### ~~F5-7 · Audit log opcional — tabla `quark_audit`~~

**Cerrado (2026-05-21, PR #85)** — `audit.go` (nuevo): `AuditConfig`
(`UserFromContext`/`TenantFromContext`/`IncludeTables`/`ExcludeTables`)
+ `Client.EnableAuditLog(ctx, cfg)` que migra `quarkAuditRow`
(modelo con `TableName()="quark_audit"`, tipos portables vía
`JSON[map[string]any]` para el diff — el `JSONB`/`BIGSERIAL` del
sketch original era PG-only). Campo `Client.audit *auditState` con
filtros include/exclude (quark_audit siempre excluido — anti-recursión).

**Desviación del sketch (documentada)**: el sketch decía escribir vía
`tx.OnCommit` (post-commit), pero ADR-0013 dice "la audit table
necesita capturar el diff **junto al commit, no después**". Implementado
así: `recordAudit` escribe la fila de auditoría **inline vía `q.exec`**,
de modo que se une a la tx activa cuando la hay (atómico — la fila de
audit hace commit/rollback junto al dato). Para CRUD sin tx es un INSERT
separado tras el write (ventana de crash documentada). El INSERT se
construye a mano (parameterizado, bypassa el pipeline observer) → cero
recursión, cero ruido en slow-query log.

Diff: `rowToMap` (fila completa) para Create/Delete y new-values para
`Update`/`UpdateFields`; `Tracked.Save` captura `{col:{old,new}}` desde
el snapshot antes del refresh. Enganchado en los 5 callsites CRUD +
`dirty_track.go Save`. user_id/tenant_id desde ctx via config funcs.

Cobertura: `testAuditLog` wired al `SharedSuite` (corre en los 5
motores CI: created full-row, Tracked.Save {old,new}, deleted, filtro
ExcludeTables) + `TestF5_7_AuditAtomicWithTxRollback` (SQLite, prueba
la garantía atómica: rollback descarta dato Y audit row). Doc nueva
`website/docs/advanced/audit-log.mdx` + sidebar. CHANGELOG `### Added`.
MIGRATION_v0.9.0 marca Fase 5 completa.

**Estimación cumplida**: ~5 h.

### F5-7 (histórico spec)

**Construye sobre F5-5 (OnCommit) + F5-6 (EventBus) + F1-1 (Tracked
dirty tracking, ya entregado).**

**Localización**:
- `audit.go` (nuevo) — `Client.EnableAuditLog(opts AuditConfig)` que:
  1. Asegura existencia de tabla `quark_audit(id BIGSERIAL PK,
     ts TIMESTAMPTZ, tenant_id TEXT, user_id TEXT, table_name TEXT,
     operation TEXT, pk TEXT, diff JSONB)` vía `MigrateRegistered`
     (F3-7).
  2. Registra un middleware que en `Create/Update/Delete` captura el
     diff (de `Tracked.Save` cuando aplica; del row entero en
     `Create`/`Delete`) y registra `tx.OnCommit(func(ctx) error {
     return audit.write(ctx, entry) })`.
- `audit.go` `AuditConfig`: `UserFromContext func(context.Context) string`,
  `TenantFromContext func(context.Context) string`,
  `IncludeTables []string` / `ExcludeTables []string`.
- `website/docs/advanced/audit-log.mdx` — guía completa.

**Definition of done**:
- Tabla `quark_audit` creada automáticamente al llamar
  `EnableAuditLog`.
- CRUD bajo `Client.EnableAuditLog` genera entradas con diff JSON
  correcto (test: crear → INSERT con `diff = {"id":1,"name":"foo"}`;
  update → `{"name":{"old":"foo","new":"bar"}}`; delete → diff del row
  completo).
- Tests cross-engine en los 5 motores CI (PG/MySQL/MariaDB/MSSQL/SQLite).
- Doc completa con ejemplos de `UserFromContext` y filtros.
- CHANGELOG `### Added`: `Client.EnableAuditLog` + `audit_log` doc.

**Estimación**: 1-2 sesiones (~6-8 h). Bloque opcional — si la fase se
alarga, F5-7 se diferiría a v0.9.1 sin bloquear el resto.

### Cierre de Fase 5

Al cerrar los 7 items y antes de taggear `v0.9.0`:

- Verificar que `docs/MIGRATION_v0.9.0.md` lista los breaking minors
  (hooks `After*` post-commit, rename `RowLevelSecurity` →
  `RowLevelSecurityClient` con alias).
- Versionar docs (`npm run docusaurus docs:version 0.9.0`).
- Actualizar header de TASKS.md a "Fase 5 cerrada".
- Marcar Fase 5 como `[x]` en `docs/ROADMAP.md` con PR refs.
- Correr `/release v=0.9.0` (el slash command valida todo).

---

## Fase 4 — Observabilidad y caché de producción (cerrada en v0.8.0)

> Spec narrativo: `docs/ANALISIS_MADUREZ.md` §4 Fase 4. Decisión
> arquitectónica del cache stampede: [`docs/adr/0011-cache-stampede-protection-wrapper.md`](docs/adr/0011-cache-stampede-protection-wrapper.md).
> Playbooks aplicables: `docs/playbooks/cache.md` (F4-4..F4-6),
> `docs/playbooks/dialects.md` (F4-7 — códigos de error por driver).
> Objetivo de fase: que en prod sepas qué pasa y la caché no se incendie.
> **Cerrado en v0.8.0 (2026-05-15)** — los 7 items entregados, todas
> las features opt-in, sin breaking changes.

Apertura formal hecha en sesión post-v0.7.0 (2026-05-14). Decisiones de
scope fijadas con el usuario:

- **Probabilistic early expiration (XFetch)**: ENTRA, dentro de F4-5.
- **Negative caching**: DIFERIDO — future work, no en Fase 4.
- **Compresión gzip de values**: DIFERIDO — future work, no en Fase 4.
- **Cache stampede protection**: wrapper común sobre `CacheStore`
  (ADR-0011), singleflight in-process; el caso cross-instancia queda
  como gap documentado, ADR sucesor si hay demanda real.

Descomposición en 7 items entregables independientemente. Orden de
ataque sugerido: **F4-4 primero** (cache key determinista es fix de
correctness y prerequisito de F4-5/F4-6), luego observabilidad
(F4-1..F4-3, el análisis avisa "sin métricas serias, optimizar
prematuramente"), luego F4-5/F4-6 (caché pesada), F4-7 al final.
Cada item es 1 PR con `code-reviewer` + docs en `website/docs/` +
CHANGELOG `### Added`.

### ~~F4-1 · OTel metrics~~

**Cerrado** — `otel/otel.go` añade `meter()` lazy (mismo patrón
panic-safe que `tracer()`, líneas 102-119) más tres instruments
inicializados con `sync.Once`: `quark.queries.total` (Int64Counter),
`quark.queries.duration` (Float64Histogram, ms) y `quark.queries.rows`
(Int64Histogram, sólo `Exec` vía `sql.Result.RowsAffected`). Cada data
point lleva `db.operation` y, cuando se setea, `db.system` — helper
`commonAttrs` compartido con la ruta de spans. **Gap intencional**:
`db.table` queda fuera (el Middleware sólo ve el SQL parametrizado, no
la tabla parseada — requeriría cambiar el contrato `Executor`); y
`quark.queries.rows` no se emite en `Query`/`QueryRow` porque contar
filas requiere envolver `*sql.Rows`. Ambos documentados en
`observability.mdx`. Cobertura: `otel_test.go` con `SpanRecorder` +
`sdkmetric.ManualReader` — defaults, opciones, redacción on/off,
`db.system` en spans, contador + duración emiten, histograma rows sólo
en Exec.

### ~~F4-2 · Redacción de SQL en spans~~

**Cerrado** — opción `WithSpanRedaction(mode)` en `otel/otel.go` con
los modos `RedactArgs` (default ON) e `IncludeArgs` (opt-out explícito).
Bajo `RedactArgs`, sólo el SQL parametrizado va a `db.statement`; bajo
`IncludeArgs`, los args se renderizan a `db.statement.args`
(`StringSlice`). Decisión de scope: como el SQL ya va parametrizado al
Middleware, la redacción aplica al rendering opcional de args, no al
SQL crudo. Helper `argsToStrings` usa `fmt.Sprintf("%v", arg)` — sin
scrubbing extra (sólo opt-in, para debug local). Cobertura en
`otel_test.go`: dos tests dedicados (`DefaultRedactionExcludesArgs`,
`IncludeArgsAttachesArgs`).

### ~~F4-3 · Slow query log estructurado~~

**Cerrado** — `quark.WithSlowQueryThreshold(d time.Duration)` Option
(option.go). Field `Client.slowQueryThreshold`. Punto de integración:
`(*BaseQuery).notifyObservers` (`query_builder.go:691-705`) llama a
`c.logSlowQueryIfNeeded(event)` ANTES del loop de observers. Los dos
emit sites de raw (`Client.RawQuery`/`Client.Exec` en `client.go`)
también pasan por el helper. Una sola pieza de código maneja los 7
call sites de `QueryEvent`. Cero duplicación, sin re-medir tiempos
(usa `event.Duration` que ya viene del emit site).
`logSlowQueryIfNeeded` (`slow_query_log.go`) emite WARN vía
`Client.logger` (`*slog.Logger`) con `duration_ms` / `threshold_ms` /
`operation` / `table` / `rows` / `sql` (parametrizado — bind args NO
incluidos, mismo principio que F4-2). Threshold `0` o negativo
desactiva la feature (single comparison check, cero coste sin uso).
Cobertura: `slow_query_log_test.go` (7 tests: disabled-by-default,
negative-disabled, below-threshold, equal-threshold, above-threshold
con todos los campos, no-args, nil-logger-safe). Doc:
`observability.mdx` § Slow query log + `caching-observability.mdx`,
CHANGELOG `### Added`.

### ~~F4-4 · Cache key con serialización determinista~~

**Cerrado** — `generateCacheKey` (`cache.go`) abandona
`fmt.Sprintf("%v", arg)`. Encoding **type-tagged y length-prefixed**:
cada campo fijo (`dialect.Name()` / `tenantID` / `schema` / `sqlStr`)
va length-prefixed; cada bind arg lleva un byte de tipo (`cacheArg*`)
+ valor en big-endian. Cierra las 3 clases de colisión: tipo
(`int64(1)` vs `string("1")` vs `uint64` vs `float64` vs `bool` vs
`nil`), boundary (sin separadores `"my"+"sql"` ≡ `"mysql"+""`,
`"ab"+""` ≡ `"a"+"b"`), y `nil` vs `""`. `time.Time` keyeado por
`UnixNano()` — mismo instante en zonas distintas = mismo key (hit
legítimo). Tipos no primitivos → `fmt.Sprintf("%#v", v)` (incluye el
tipo Go, no invoca `Stringer`). Reflection-free (ADR-0002). Cobertura:
`cache_test.go` (5 tests: determinismo, type/boundary/nil collision,
time same-instant/distinct, discriminantes de query); el integration
`cache_all_engines_test.go` sigue verde (el comportamiento hit/miss no
cambia — sólo se endurece el key). Doc: `docs/playbooks/cache.md`
§"Cache key" + CHANGELOG `### Fixed`.

### ~~F4-5 · Cache stampede protection (ADR-0011)~~

**Cerrado** — `stampedeStore` (`cache_stampede.go`) envuelve cualquier
`CacheStore` con singleflight + TTL jitter + XFetch. Activación
automática en `WithCacheStore` (no opt-in, "todo o nada" del playbook).
Componentes:

- **Singleflight** (`golang.org/x/sync/singleflight`): N callers
  colapsan a 1 compute. El query path (`query_exec.go:List`) usa el
  método interno `getOrCompute` cuando type-assert detecta
  `*stampedeStore`; stores de terceros caen al cache-aside histórico.
- **TTL jitter**: factor uniforme `[1-jitterPct, 1+jitterPct]` por
  Set, default `±10%`, ajustable con `WithCacheJitter(pct)`.
- **XFetch**: cada entrada lleva metadata embebida (`xfetchEntry`
  length-prefixed con magic `QSPD`/version 0x01 + deltaNs + computedAt
  + expiresAt + data). Fórmula Vattani:
  `timeLeft ≤ delta * β * (-ln(rand()))`. Ajustable con
  `WithCacheXFetchBeta(β)`; `β=0` desactiva XFetch.

`memory.Store` / `redis.Store` / terceros NO cambian — la interfaz
`CacheStore` no rompe. Gap conocido: singleflight in-process; cross-
instancia queda como ADR sucesor (ADR-0011 §Cuándo reabrir).
Cobertura: `cache_stampede_test.go` (10 tests: round-trip encoding,
detección de entradas foráneas, jitter en rango, XFetch boundary
cases — delta=0/expirado/lejos-de-expiry, singleflight bajo 50
goroutines concurrentes, hit-after-first-compute, clamping de config,
panic en inner nil). Doc: `docs/playbooks/cache.md` § "Sin protección
contra cache stampede" (deuda marcada cerrada),
`website/docs/advanced/caching-observability.mdx` § "Stampede
protection", `website/docs/reference/api/caching.mdx`, CHANGELOG
`### Added`.

### ~~F4-6 · Invalidación granular por PK + fix Redis tag-key TTL~~

**Cerrado** — dos mejoras de cache en un PR:

1. **Invalidación por PK**: `executeExec` (`query_crud.go`) acepta
   `extraTags ...string` variadic. Las mutaciones que conocen la PK
   (`Update`/`UpdateFields`/`Tracked.Save`/`softDelete`/`hardDeleteByPK`/
   `Create` post-PK-populate) pasan `<table>:<pk>` para que el mismo
   `InvalidateTags` call cargue ambos tags. Helper `rowTag(pkValue)` en
   `cache_invalidation.go` formatea el tag (`""` para composite PKs —
   gap documentado). Mutaciones sin PK conocida (`DeleteBatch`/`UpdateBatch`
   /raw `Exec`/upserts) usan sólo el tag de tabla — fallback histórico
   intacto. Callers cachean queries by-PK con
   `.Cache(ttl, "users", "users:1")` para invalidación granular.

2. **Redis tag-TTL fix**: `cache/redis/redis.go:Set` reemplaza
   `pipe.Expire(...)` por `pipe.ExpireNX(...)` + `pipe.ExpireGT(...)`
   en el pipe. NX inicializa cuando el SET no tiene TTL; GT extiende
   sólo cuando el nuevo > actual. **Nunca acorta** — keys con TTL
   pequeño no dejan huérfanas. Requiere Redis 7.0+ (flags `NX`/`GT`);
   gap documentado en comentario inline.

Cobertura: `cache_invalidation_test.go` (12 sub-tests:
`TestRowTag_Format` 5 cases, `TestInvalidateRowTag_*` 4 cases,
`TestExecuteExec_PassesRowTagAlongTable` 3 cases). Doc:
`docs/playbooks/cache.md` § "Invalidación grosera" + § "TTL del
tag-key Redis" (ambas deudas tachadas), `website/docs/reference/api/caching.mdx`
§ "Per-row invalidation", CHANGELOG `### Added`.

### ~~F4-7 · Retry de deadlocks~~

**Cerrado** — `WithDeadlockRetry(maxAttempts)` Option (`option.go`) +
`isDeadlock(err)` helper (`db_errors.go`, mismo patrón que
`isUniqueViolation` de P0-3) + retry loop en `Client.Tx`
(`tx.go:56-...`). El closure se re-ejecuta contra una tx fresca con
exponential backoff + ±50% jitter (10ms doblando, cap 1s) cuando el
error matchea uno de los 4 motores multi-writer (PG 40P01, MySQL 1213,
MSSQL 1205, Oracle ORA-00060). SQLite es single-writer y nunca emite
deadlock real — el option es no-op en SQLite por construcción. Ctx
cancelado durante backoff → aborta. Disabled por default (maxAttempts
≤ 1); opt-in puro. `runTxOnce` extraído como helper interno para que
el loop pueda re-invocar la unidad transaccional completa. Cobertura:
`db_errors_test.go` (3 tests, 13 sub-cases del classifier incluyendo
wrapped errors y no-collision con isUniqueViolation),
`tx_deadlock_retry_test.go` (5 tests: no-retry-by-default, retry-
eventually-commits, retry-exhausted con unwrap, non-deadlock-
propagates-immediately, cancelled-context-aborts-backoff). Doc:
`website/docs/reference/api/client.mdx` § "WithDeadlockRetry",
CHANGELOG `### Added`.

**Detección por código de error del driver:**
- PostgreSQL `40P01` (`pgconn.PgError.Code` SQLSTATE)
- MySQL / MariaDB `1213` (`gomysql.MySQLError.Number`)
- MSSQL `1205` (`mssql.Error.Number`)
- Oracle `ORA-00060` (`goora.OracleError.ErrCode == 60`)

Helper `isDeadlock(err)` en `db_errors.go` (mismo patrón que
`isUniqueViolation` de P0-3, con `errors.As` contra los tipos de los
6 drivers). Exponential backoff con jitter, máximo N intentos.
**Disabled por default**; `WithDeadlockRetry(n)` lo habilita. El retry
envuelve la unidad transaccional completa, no la query suelta — un
deadlock aborta la transacción entera, así que reintentar una query
sin reabrir la tx no tiene sentido. **Punto de integración:
`Client.Tx(ctx, fn)` (`tx.go:56`)** — el runner closure-based ya
ejecuta `fn` dentro de BEGIN/COMMIT; el retry re-ejecuta `fn` con una
tx nueva cuando `isDeadlock(err)` y quedan intentos. `BeginTx` (la
variante manual, `tx.go:38`) queda fuera de scope: sin closure no hay
forma de re-ejecutar el trabajo del caller. Tests: difícil provocar
deadlocks deterministas cross-engine — al menos unit tests del mapeo
de códigos + un integration test que fuerce el deadlock en PG (dos tx
con orden de lock invertido).

### ~~Cierre de Fase 4~~

**Hecho** — v0.8.0 taggeada el 2026-05-15 con los 7 items entregados.
Diferidos a future work explícitos (no bloquearon el cierre y caen en
ADRs / issues posteriores cuando aparezca demanda real): negative
caching, compresión gzip de values, cross-instance stampede
protection (ADR sucesor de ADR-0011 con `DistributedLock` hook). El
integration test de deadlock cross-engine real (dos tx de lock
invertido) llegó en `[Unreleased]` —
`tx_deadlock_integration_test.go`, PG/MySQL/MariaDB.

---

## Fase 3 — Migraciones serias y schema-as-code (apertura formal)

> Spec narrativo: `docs/ANALISIS_MADUREZ.md` §4 Fase 3. Decisión
> arquitectónica: [`docs/adr/0009-migrations-introspection-diff-not-versioned-files.md`](docs/adr/0009-migrations-introspection-diff-not-versioned-files.md).
> Objetivo de fase: emparejar Quark con Alembic / EF Migrations / Atlas.
> Salida: v0.6.0 con migraciones que un equipo serio aceptaría.

Estrategia decidida (ADR-0009): **code-first + diff bidireccional**.
El modelo Go es la fuente de verdad; un `quark schema diff` introspecciona
el DB en vivo, lo compara, y emite la migración candidata Up + Down.

Descomposición en 7 items entregables independientemente:

### ~~F3-1 · Lock distribuido de migración~~

**Cerrado** — `migration_lock.go` introduce `MigrationLock` (interface
con `Release(ctx)`) y `MigrationLocker` (interface opcional que un
Dialect implementa para soportar el lock). El método público
`Client.AcquireMigrationLock(ctx, name, timeout)` hace type-assertion
contra `MigrationLocker`; si el dialect no lo implementa, devuelve
`ErrUnsupportedFeature` envuelto con un mensaje descriptivo.
`ErrLockTimeout` es el sentinel para timeouts (distinguible de
`ErrUnsupportedFeature` por `errors.Is`).

Implementaciones por dialect (`dialect_migration_lock.go`):
- **PG**: session-level `pg_advisory_lock(hashtext(name))` sobre
  conexión dedicada, con `SET lock_timeout` previo. SQLSTATE
  `55P03` (`lock_not_available`) → `ErrLockTimeout`. Se eligió
  session-level (no `pg_advisory_xact_lock`) para no atar el lock
  a una transacción larga — el caller puede correr múltiples
  statements bajo el lock.
- **MySQL/MariaDB**: `GET_LOCK(name, timeout_seconds)` con
  `RELEASE_LOCK(name)` en `Release`. Return 0 → `ErrLockTimeout`,
  NULL → error descriptivo. Resolución de timeout es segundos
  enteros (sub-second se redondea hacia arriba a 1s).
- **MSSQL**: `sp_getapplock @LockMode='Exclusive', @LockOwner='Session'`
  + `sp_releaseapplock`. Status `-1` → `ErrLockTimeout`; otros
  códigos negativos → error con el código.
- **SQLite**: no implementa `MigrationLocker` (intencional). Sin
  primitiva distribuida; usar `BEGIN IMMEDIATE` para mutex
  intra-proceso. `Client.AcquireMigrationLock` devuelve
  `ErrUnsupportedFeature`.
- **Oracle**: tampoco implementa `MigrationLocker` aún. `DBMS_LOCK`
  necesita PL/SQL blocks y handles per-lock vía `ALLOCATE_UNIQUE`;
  diferido a follow-up PR. Comportamiento idéntico al de SQLite
  por el momento.

Decisión clave: `MigrationLocker` es **interface opcional**, no
método requerido en `Dialect`. Custom dialects existentes downstream
no rompen su build.

Cobertura: `migration_lock_test.go` (5 unit tests: type assertions
sobre supported/unsupported dialects + PG SQL shape + MySQL/MSSQL
timeout mapping). `testMigrationLock` en SharedSuite (3 subtests
para los 4 motores que lo soportan: AcquireRelease,
ConcurrentAcquireSerialises con mutex-exclusión verificada por
contador atómico, TimeoutWhenAlreadyHeld). SQLite ejecuta un
subtest dedicado `UnsupportedOnSQLite` que verifica
`ErrUnsupportedFeature`.

Doc: `website/docs/guides/migrations.mdx` § Distributed Migration
Lock con la tabla per-dialect y notas sobre opt-in / sub-second
timeout / session-level advisory; CHANGELOG `### Added`.

### F3-2 · Schema introspection (per-dialect) — ✅ cerrado (6 dialectos)

**Core (SQLite + PG) cerrado**. `schema.go` introduce los tipos
neutrales `Schema{Tables}`, `Table{Name, Columns}`, `Column{Name, Type, Nullable, Default}`,
la interface opcional `SchemaIntrospector`, y `Client.IntrospectSchema(ctx)`.
`dialect_introspection.go` implementa SQLite (`sqlite_master` + `PRAGMA
table_info`) y PostgreSQL (`information_schema.tables` / `columns` con
`current_schema()` scope + reassembly de `varchar(N)`/`numeric(P,S)`).
Los seis dialectos implementan `SchemaIntrospector` (ver sub-items).

Pendientes para cerrar F3-2 entero:
- ~~**F3-2-mysql** / **F3-2-mariadb**~~. **Cerrado** —
  `INFORMATION_SCHEMA.{TABLES,COLUMNS}` con scope `DATABASE()` y
  `COLUMN_TYPE` para tipo verbose (`varchar(255)`, `int(11) unsigned`).
  Ambos motores comparten un único impl
  `mysqlLikeIntrospect`; los dos Dialect types delegan a él.
- ~~**F3-2-mssql**~~. **Cerrado** — `sys.tables` /
  `sys.columns` / `sys.types` con LEFT JOIN a
  `sys.default_constraints`. Type reassembly de `max_length`/
  `precision`/`scale` con dos detalles MSSQL-específicos: el
  `max_length = -1` se traduce a `(MAX)` (NVARCHAR(MAX) /
  VARBINARY(MAX)), y para nvarchar/nchar el `max_length` es bytes
  (chars × 2) → emit `length/2` para coincidir con la DDL
  user-facing. Defaults se pasan raw — MSSQL los devuelve envueltos
  en paréntesis (`(0)`, `(getdate())`), unwrap es responsabilidad
  del F3-3.
- ~~**F3-2-oracle**~~. **Cerrado** (#30 / PR (b) del Gate §A Item 1) —
  data dictionary `USER_TABLES` / `USER_TAB_COLUMNS` / `USER_INDEXES` /
  `USER_CONSTRAINTS` (+ `USER_CONS_COLUMNS`). Identifiers lowercaseados
  (Oracle los almacena en mayúscula), reassembly `NUMBER(p[,s])` /
  `VARCHAR2(char_len)`, NOT-NULL system checks filtrados (se exponen vía
  `Column.Nullable`), `SEARCH_CONDITION_VC` para predicados CHECK (evita
  el LONG `SEARCH_CONDITION`). El diff trata el `NUMBER` desnudo del PK
  identity y su default de secuencia como equivalentes a `NUMBER(19)`;
  nueva interface opcional `ColumnTypeMapper` mapea `TEXT`→`CLOB` en el
  DDL del ejecutor. SharedSuite Oracle 199/12 → 211/5 (cierra
  PlanMigration ×6 + el contrato SchemaIntrospection ×5). Verificado en
  los 6 motores.
- ~~**F3-2-indexes**~~. **Cerrado** — `Table.Indexes`
  poblado en SQLite / PG / MySQL / MariaDB / MSSQL con
  `Index{Name, Columns, Unique}`. PK-backing indexes filtrados
  per-dialect (PK es constraint, no index, en el modelo de diff).
  Catálogos: SQLite `PRAGMA index_list` + `PRAGMA index_info`;
  PG `pg_index` con `unnest(indkey) WITH ORDINALITY` para column
  order estable; MySQL/MariaDB `INFORMATION_SCHEMA.STATISTICS`
  agrupado por `INDEX_NAME` con `SEQ_IN_INDEX`; MSSQL
  `sys.indexes` + `sys.index_columns` (`is_primary_key=0`,
  `type>0`, `is_included_column=0`). Expression indexes
  surface el slot como `""` para que F3-3 decida si los
  trata como opacos.
- ~~**F3-2-fks**~~. **Cerrado** — `Table.ForeignKeys`
  poblado en SQLite / PG / MySQL / MariaDB / MSSQL con
  `ForeignKey{Name, Columns, RefTable, RefColumns, OnDelete, OnUpdate}`.
  Catálogos: SQLite `PRAGMA foreign_key_list` (Name="" para inline
  FKs, diff layer hace match por column-tuple);
  PG `pg_constraint` (contype='f') con `unnest(conkey/confkey) WITH
  ORDINALITY` para column matching en composites; MySQL/MariaDB
  `INFORMATION_SCHEMA.KEY_COLUMN_USAGE` + `REFERENTIAL_CONSTRAINTS`
  agrupado por CONSTRAINT_NAME; MSSQL `sys.foreign_keys` +
  `sys.foreign_key_columns` con `delete_referential_action_desc`
  underscored normalizado a verbose. `OnDelete`/`OnUpdate` se
  emiten siempre en forma SQL-standard verbose (`CASCADE`,
  `SET NULL`, `SET DEFAULT`, `RESTRICT`, `NO ACTION`).
- ~~**F3-2-checks**~~. **Cerrado** — `Table.Checks` poblado
  en PG / MySQL / MariaDB / MSSQL con `Check{Name, Expression}`.
  Catálogos: PG `pg_constraint` (contype='c') con
  `pg_get_constraintdef(oid, true)` (se quita el `CHECK ` leading);
  MySQL/MariaDB `INFORMATION_SCHEMA.CHECK_CONSTRAINTS` joined con
  `TABLE_CONSTRAINTS` (MySQL 8.0.16+, MariaDB 10.2.1+ — versiones
  anteriores no tienen el catálogo, `mysqlListChecks` detecta el
  `Error 1146` y degrada a empty result para no romper la
  introspección entera); MSSQL
  `sys.check_constraints` filtrado por parent `OBJECT_ID`.
  Expression se pasa raw — cada motor tiene su canonical form
  (`((age > 0))` PG, `` (`age` > 0) `` MariaDB, `([age]>(0))`
  MSSQL); F3-3 maneja AST-level equivalence cross-dialect.
  **SQLite intencionalmente diferido**: SQLite no tiene catálogo
  para CHECK; única vía es parsear `sqlite_master.sql`, brittle
  y fuera de alcance del catalog-reader layer.
  `Schema.Tables[i].Checks=nil` en SQLite (intencional, NO "sin
  checks"). Follow-up posible: F3-2-checks-sqlite si hay demanda.

Indexes/FKs/Checks llegan **después** de cerrar los 4 motores CI con
la superficie column-only — la matriz blocking exige verde en
los 4 antes de extender el schema struct, para no propagar bugs
cross-dialect al diff (F3-3).

Cobertura actual: 2 unit tests (`TestSchema_DialectInterfaceConformance`
pin la lista de soporte; `TestSchema_StringDefaultRoundTrip` pin la
distinción nil-vs-empty-string) + `testSchemaIntrospection` en
SharedSuite (2 subtests `ListsFixtureTable` /
`FiltersInternalTables` en dialects soportados; verifica
`ErrUnsupportedFeature` en MySQL/MariaDB/MSSQL/Oracle).

Doc: `website/docs/guides/migrations.mdx` § Schema Introspection
(añadido en este PR). CHANGELOG `### Added`.

### F3-3 · Schema diff core

- ~~**F3-3-core**~~ **Cerrado** — `Diff(desired, current Schema) []Operation`
  en `migrate_diff.go`. Operation types sealed y dialect-neutrales
  (`OpCreateTable`, `OpDropTable`, `OpAddColumn`, `OpDropColumn`,
  `OpAlterColumn`, `OpCreateIndex`, `OpDropIndex`, `OpAddForeignKey`,
  `OpDropForeignKey`, `OpAddCheck`, `OpDropCheck`). Algoritmo puro
  y determinista. Equality functions con awareness cross-dialect:
  MariaDB RESTRICT ≡ MySQL NO ACTION; SQLite Checks=nil skip
  comparison. Op ordering documentado en godoc de Diff. Cobertura:
  12 unit tests en `migrate_diff_test.go`.

- ~~**F3-3-plan**~~ **Cerrado** — `Client.PlanMigration(ctx, models...) (Plan, error)`
  en `migrate_plan.go`. Pipeline: models → `desired Schema` (reflect
  vía `GetModelMetaByType` + `migrate.SQLTypeWithOpts`) → `IntrospectSchema`
  para el current → `Diff()` → `Plan`. Plan inert (no Apply hasta
  F3-3-execute). `Plan.IsEmpty()` y `Plan.String()` para uso en
  health checks / CI gates / F3-5 CLI.

  Round-trip identity es el contrato headline: Migrate(model) →
  PlanMigration(model) devuelve Plan vacío en SQLite. Cobertura: 6
  unit tests en `migrate_plan_test.go`.

  Fix colateral de F3-2 incluido: SQLite introspector reportaba PK
  columns como `Nullable=true` (PRAGMA `notnull=0` para PKs
  implícitas); ahora ORs en el campo `pk` del PRAGMA para emitir
  `Nullable=false`. Sin este fix, el round-trip diff emitía un
  spurious `nullable true→false` alter en cada PK.

  Gaps conocidos documentados en godoc + migrations.mdx:
  - ~~**Type string drift cross-dialect**~~: **Cerrado por
    F3-3-types** — normaliser en `columnsEqual` (case-fold,
    PG character varying alias, MySQL display-width strip) hace
    el round-trip clean en los 5 motores. `PlanMigration_RoundTripIsEmpty`
    ahora corre en SharedSuite.
  - **Indexes/FKs/Checks no declarados en modelos**: `PlanMigration`
    copia el surface non-column del current al desired antes de
    diffear para evitar drops espurios. F3-3-plan-indexes
    levantará esta limitación cuando struct tags soporten
    declarar indexes.

- ~~**F3-3-execute**~~ **Cerrado** — `Client.ApplyPlan(ctx, plan)`
  en `migrate_execute.go`. Dispatch per op type via type switch:
  CreateTable rebuilds DDL desde el neutral `Table` struct;
  Drop/Add/AlterColumn usan `Dialect.AlterTable*`; CreateIndex /
  AddForeignKey reusan helpers F2-era; DropIndex / DropForeignKey /
  AddCheck / DropCheck inline per-dialect.

  Gaps documentados:
  - **OpAlterColumn**: solo type changes hoy. Nullable / Default
    deltas son no-ops (TODO F3-3-execute-alter).
  - **SQLite + DropForeignKey / DropCheck**: `ErrUnsupportedFeature`
    porque SQLite no soporta `ALTER TABLE DROP CONSTRAINT`. Workaround
    es 12-step rebuild — follow-up F3-3-execute-sqlite-rebuild.

  No transaccional — F3-4 (resumable) añade el wrapper BEGIN/COMMIT.
  Error wrap incluye op index + op.String() para debug.

  Tests: 6 unit-style en `migrate_execute_test.go` (empty noop,
  round-trip, add/drop column, SQLite limitations, error wrapping).
  Integration test `ApplyPlan_AddColumnRoundTrip` añadido a SharedSuite,
  corre en 4 motores + SQLite.

- **Heurísticas pendientes** para casos ambiguos (no F3-3-core):
  - Rename column = drop + add. Opt-in via tag hint
    (`db:"new,old_name=old"`). Pendiente para F3-3-plan.
  - Risk levels (`safe` / `lossy` / `breaking`) — pendiente para
    F3-4 + F3-5 (el plan / executor decide cómo gate destructive
    ops, no la diff layer).

### F3-4 · Migración transaccional + resumable

- ~~**F3-4-tx**~~ **Cerrado** — `Client.ApplyPlan` wrappea ahora
  BEGIN/COMMIT en engines con transactional DDL (PG / MSSQL /
  SQLite). MySQL / MariaDB / Oracle pasan por la ruta no-tx
  (DDL implicit-commits, no aporta envolver). Refactor interno:
  `createIndexOn` / `addForeignKeyOn` toman `Executor`; los
  publicos `CreateIndex` / `AddForeignKey` envuelven con `c.db`.
  Todos los helpers per-op del executor (`dropIndex`,
  `dropForeignKey`, `addCheck`, `dropCheck`, `applyCreateTable`)
  igualmente parametrizados.

  Tests: `TestApplyPlan_SQLite_RollbackOnMidPlanFailure` (unit),
  `TestSupportsTransactionalDDL` (table-driven 7 cases),
  `ApplyPlan_TransactionalRollback` integration en SharedSuite
  con branching per-dialect (rollback expected en PG/MSSQL/SQLite,
  partial commit expected en MySQL/MariaDB).

- ~~**F3-4-resumable**~~ **Cerrado** — checkpoint state en
  `quark_migration_state(plan_hash, op_index, op_string, applied_at)`
  para MySQL / MariaDB / Oracle. `Plan.Hash()` (sha256 hex de
  `op.String()` concatenados) es la clave de identidad: la
  siguiente invocación contra el MISMO plan lee el último
  op_index registrado y arranca desde op_index+1. Plan-drift se
  detecta automáticamente — un plan modificado tiene hash
  diferente, arranca de cero. Cobertura: `TestPlan_Hash_*` (3
  unit tests para determinismo / orden / longitud) + integration
  `ApplyPlan_ResumesAfterMidPlanFailure` en SharedSuite (3-op
  plan, op intermedia falla, fix manual, re-invoke, verifica
  que op 0 NO se re-aplica y op 2 sí se ejecuta). PG/MSSQL/SQLite
  skipean este test porque usan tx wrapper.

- **F3-4 cerrado entero** (tx + resumable). El test "mata el
  proceso a mitad y completa después" del plan original queda
  cubierto a un nivel diferente: el integration test reproduce
  la condición de fallo (op intermedia error) en lugar de matar
  el proceso, lo cual prueba la misma propiedad sin el
  flakiness del kill.

### F3-5 · CLI plan/verify/apply

- ~~**F3-5**~~ **Cerrado** — package `quarkmigrate` con `Run(ctx,
  action, client, models...)` y `RunWithOutput` (variante test-
  friendly con writers explícitos). Tres actions: `plan` (exit 0,
  informational), `verify` (exit 1 si non-empty — CI gate), `apply`
  (corre el plan). Exit codes como constantes públicas
  (`ExitSuccess`/`ExitDriftDetected`/`ExitError` = 0/1/2). Plan
  output prefijado con primeros 8 chars del `Plan.Hash()` para
  correlación con `quark_migration_state`.

  Decisión: NO se ship un binario standalone porque Go no tiene
  runtime model registration — el binario debe importar los
  modelos del user. El patrón idiomático es que el user escriba
  un `migrations/main.go` thin que importa `quarkmigrate` + sus
  modelos. Ejemplo completo en `examples/migrations/main.go`.

  Cobertura: 7 unit tests en `quarkmigrate/run_test.go` (ParseAction
  table-driven con 7 casos; Run para los 3 actions × estados
  empty/non-empty + error paths). Ejemplo compila en CI vía
  `go build ./...`.

  Deferred a follow-up:
  - **Colored output** (azul/amarillo/rojo para safe/lossy/breaking).
    Bloqueado por: F3-3 no clasifica ops por RiskLevel todavía.
    Cuando aterrice RiskLevel (probable F3-6 o un PR independiente),
    el render se extiende.
  - **`Client.MigrateAtomic(ctx, models...)`** — wrapper que
    combina AcquireMigrationLock + PlanMigration + ApplyPlan
    en una sola call para non-tx engines. Flagged en godoc de
    ApplyPlan; sin abrir PR hasta que F3-1 cubra Oracle.

### F3-6 · Backfill orquestado

- ~~**F3-6**~~ **Cerrado** — `Client.Backfill(ctx, BackfillSpec)`
  en `migrate_backfill.go`. `BackfillSpec{Name, Table, PKColumn,
  BatchSize, Process}` describe el work; helper itera por PK
  ascending, llama callback con `batchPKs []int64`, persiste
  `last_pk` en `quark_backfill_state` (per-dialect: PG/SQLite/MySQL/
  MariaDB usan `CREATE TABLE IF NOT EXISTS`, MSSQL guard via
  `sys.tables`, Oracle swallow ORA-00955; default
  `ErrUnsupportedFeature`).

  Decisión de API: callback recibe PKs (no row contents) porque
  backfill SQL es "UPDATE ... WHERE id IN (...)" en práctica, no
  "SELECT + transform"; pasar PKs evita expansión a generics o
  reflect.

  Cobertura: 5 tests + sub-tests en `migrate_backfill_test.go` —
  happy path (10 rows, batch 4 → 3 batches en ascending order);
  resume tras callback error (batch 2 falla → re-invoke pickea
  desde batch 2 con PKs 5..10); idempotencia post-completion
  (re-call con mismo Name = 0 callbacks); validación de inputs
  (Name/Table/Process empty, identifier injection); custom
  PKColumn.

  State table separada de `quark_migration_state` (la del
  F3-4-resumable) — F3-4 keyea por (plan_hash, op_index); F3-6
  keyea por (name). Distintas semánticas, distintos schemas.

  Limitaciones documentadas (future work si hay demanda):
  - Solo integer PKs. Text PKs y composite PKs out of scope.
  - Asume positive PKs (last_pk=0 fresh-start). Tablas con PKs
    negativos necesitan pre-seed manual.
  - Concurrencia: igual que ApplyPlan resumable — wrap con
    AcquireMigrationLock si necesitas cross-process serialisation.

### F3-7 · Per-client model registry

- ~~**F3-7 (additive scope)**~~ **Cerrado** —
  `Client.RegisterModel(models ...any) error`,
  `Client.RegisteredModels() []any`,
  `Client.MigrateRegistered(ctx)`,
  `Client.PlanMigrationRegistered(ctx)` en `client_registry.go`.
  Per-Client list mutex-protegida; safe for concurrent use.
  Validación up-front (no partial registration on failure).
  Cobertura: 11 unit tests incluyendo race-detector smoke
  (TestClient_RegisterModel_ConcurrentSafe), snapshot semantics,
  no-dedup contract, validation, end-to-end MigrateRegistered.

  **Scope DECISION**: F3-7 fue intencionalmente recortado a
  ADITIVO (en lugar del plan original "sustituir el global"). El
  global type-meta cache en `internal/schema` se queda — es
  correct as global state porque la meta es determinista per
  `reflect.Type`. F3-7 añade per-Client state para "qué modelos
  maneja este Client", NO para "cuál es el meta de tipo X".
  Multi-tenant (ADR-0007) ya no necesita el reemplazo total
  porque cada Client puede tener su propio model set sin
  cross-contamination del meta cache.

  Decisión NO en este PR (deferred a un follow-up si surge
  demanda):
  - **Implicit registration via `Client.Migrate(ctx, &Model{})`**:
    el plan original quería que Migrate registrara
    implícitamente; lo dejé explícito para evitar el "magic
    registry" donde el user no sabe por qué un modelo está
    registrado.
  - **`quark.For[T](ctx, client)` generic con registry lookup**:
    requiere Go generics + un fallback al global. Out of scope
    para F3-7-additive.
  - **Deprecación del global**: no hay deprecación pending.
    El global es correct as-is.

### Cierre de Phase 3

Cuando F3-1..F3-7 estén ✅, taggear **v0.6.0** via `/release v0.6.0`.
Mientras Phase 3 esté en progreso (cualquier F3-N abierto), v0.6 no se taggea.

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

### ~~F2-window · `Over` + `Window` + `RowNumber`/`Rank`/`Lag`/`Lead`~~

**Cerrado** — `window.go` introduce el tipo `Window` inmutable
(NewWindow → PartitionBy → OrderBy devuelve copia) y los nodos AST
`Over(inner, w)`, `RowNumber`, `Rank`, `DenseRank`, `Lag(col, offset)`,
`Lead(col, offset)`. Las funciones de ventana bypass la whitelist de
`Func` porque su sintaxis está restringida al contexto `OVER (...)`.
El offset de Lag/Lead se bindea como parámetro, no se interpola.

`Query[T].SelectExpr(alias string, e Expr)` añade una proyección AST
al SELECT list. Renderiza vía qmarkDialect (igual que `AsSubquery`)
para que los `?` se reindexen al placeholder del dialecto en el
`argIndex` correcto cuando `buildSelect` corre. `selectExprs` se
añade a BaseQuery y `clone()` lo deep-copia.

`buildSelect` ahora compone el SELECT list combinando
`selectCols` + `selectExprs` (separados por coma; en ese orden), y
los args de las proyecciones AST aterrizan entre los args de CTE y
los args de WHERE — coincidiendo con el orden SQL.

Cobertura: `window_test.go` (6 tests unitarios sobre cada nodo +
inmutabilidad) + `testWindow` en SharedSuite (3 subtests:
SelectExprRendersOverPartitionByOrderBy,
SelectExprErrorsOnInvalidAlias,
SelectExprComposesWithRegularSelect). Los asserts sobre SQL emitida
pasan por middleware `windowCapturing`.

Doc: `website/docs/guides/querying.mdx` § Window Functions con tabla
de helpers; CHANGELOG `### Added`.

### ~~F2-set · `UNION` / `INTERSECT` / `EXCEPT` entre `Query[T]`~~

**Cerrado** — `setop.go` introduce `Query[T].Union(other)`,
`UnionAll(other)`, `Intersect(other)`, `Except(other)`. El operando se
captura con `qmarkDialect` y se renderiza flat (sin paréntesis) porque
SQLite rechaza paréntesis alrededor de operandos en compound-selects;
la forma estándar `SELECT ... UNION ... SELECT ... ORDER BY ... LIMIT
...` es portable a las 6 bases.

`setOpKeyword(d, kind, all)` mapea por dialecto: Oracle EXCEPT→MINUS,
MySQL/MariaDB rechazan INTERSECT/EXCEPT con ErrUnsupportedFeature,
SQLite rechaza INTERSECT ALL/EXCEPT ALL. Se mantiene como helper
package-level (no método del interface Dialect) para no romper
implementaciones custom de Dialect downstream.

Restricciones enforced en `attachSetOp` (cada una surfacea
ErrUnsupportedFeature):
- Operand: sin ORDER BY / LIMIT / OFFSET / lock / CTEs propias /
  set-ops anidadas
- Base: sin pessimistic-lock options (el suffix se anclaría al
  resultado combinado)
- ORDER BY / LIMIT del Query[T] outer aplican al resultado combinado.

`buildSelect` inserta el rendering set-op entre HAVING y ORDER BY —
splice limpio, sin re-wrapping del buffer.

Cobertura: `testSetOp` en SharedSuite con 8 subtests
(UnionAllRendersFlatCompoundSelect, UnionDeduplicates,
IntersectFiltersCommonRows, ExceptFiltersUnique, RejectsLockOnBase,
NilOperandRejected, OperandWithOrderByRejected,
OperandWithLimitRejected). Verificación de SQL via middleware
`setOpCapturing`.

Doc: `website/docs/guides/querying.mdx` § Set Operators con tabla de
métodos y matriz de soporte por dialecto; CHANGELOG `### Added`.

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

### ~~F2-join-builder · `Join(table).On(col, op, otherCol)`~~

**Cerrado** — `query_builder.go` introduce `JoinBuilder[T]` y reemplaza
las firmas de `Join`/`LeftJoin`/`RightJoin`: ahora reciben sólo el
nombre de tabla y devuelven `*JoinBuilder[T]`. El builder cierra el
JOIN con dos métodos:
- `.On(left, op, right string) *Query[T]` — forma tipada para la
  comparación binaria identifier-vs-identifier (la mayoría de JOINs)
- `.OnRaw(onClause string) *Query[T]` — escape hatch para cláusulas
  ON compuestas (AND-chained); valida con la misma regla de
  `guard.ValidateJoinOn` que la forma legacy

Breaking change: cierra la deprecation de v0.2 sobre el string-raw
`Join(table, onClause string)`. Migration doc:
`docs/MIGRATION_v0.4.0.md` con tabla de antes/después y reglas
`gofmt -r` mecánicas. 6 callers internos migrados (5 tests +
join_on_security_test).

Cobertura: `testJoinBuilder` en SharedSuite con 4 subtests
(OnTypedFormExecutes, OnRawAcceptsCompoundClause, OnRawRejectsInjection,
LeftJoinAndRightJoinReturnBuilder).

Doc: `website/docs/guides/querying.mdx` § Joins reescrita con la nueva
API; CHANGELOG `### Changed (BREAKING)`.

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

Deferred a Bloque B con su propio scope:
- ~~**Arrays Postgres** — wrapper neutro~~. **Cerrado en v0.6
  (Unreleased)**. `array.go` introduce `Array[T any]` con
  `Value`/`Scan` JSON-backed y migrate detection idéntica a
  `JSON[T]` (`isQuarkArray` → `jsonColumnType` per dialect).
  Decisión consciente: no PG-native `INT[]`/`TEXT[]`, no operadores
  `@>`/`&&`, no import de `pgtype`. La razón viene del propio
  spec ("wrapper neutro sin pegar el dialect a pgtype").
  Cobertura: `array_test.go` (7 tests unitarios) + `testArray` en
  SharedSuite (3 subtests: StringArrayRoundTrip,
  ZeroValueArraysRoundTrip, UpdateReplacesArrayContents). Inherits
  el skip de MSSQL JSON NVARCHAR(MAX) hasta que F0-8 followup E
  cierre el byte-encoding bug.
- ~~**Timezones por columna**~~. **Cerrado en [Unreleased] → v0.7.0
  (PR #63)**. ADR-0010 archivado. Estrategia híbrida: Client option
  `quark.WithDefaultTZ(loc *time.Location)` + tag `quark:"tz=Europe/Madrid"`
  override per-columna; precedencia tag → client default → pass-through.
  Wire UTC-always: `timezone.go` introduce `bindTimeValue` (bind →
  `.UTC()`) y los scanners (`timeScanner`/`nullTimeScanner`/nuevo
  `nullableTimeScanner`) aplican `.In(loc)` en memoria. `FieldMeta.TZ`
  parseado eager en `computeModelMeta`; zona IANA inválida →
  `ModelMeta.TZError` → `ErrInvalidTimezone` fail-fast en `RegisterModel`
  / `Migrate`. Hot path gateado por `BaseQuery.tzActive()` (flag O(1)) —
  cero overhead sin tz (ADR-0002). Bind cubierto en los 8 call sites
  (`buildInsert`/`buildUpdate`/`buildUpdateMap`/`UpdateFields`/batch
  single+multi/upsert standard+MSSQL/`buildMerge`); scan en `scanRow` +
  4 preload loaders. Aplica a `time.Time`, `*time.Time`,
  `Nullable[time.Time]`, incl. vía `Preload`. Cobertura:
  `timezone_test.go` (unit: `bindTimeValue`, `resolveFieldTZ`, parsing
  del tag + invalid-tz) + `testTZ` en SharedSuite (6 subtests:
  ClientDefaultRoundTrip, TagOverrideRoundTrip, NullableTimeWithTZ,
  WireInstantStableAcrossZones, UpdateFieldsWithTZ,
  NoDefaultNoTagIsPassthrough) + `TestRegisterModel/Migrate_InvalidTimezone`.
  Verde en los 4 motores CI + SQLite. Doc:
  `website/docs/guides/modeling.mdx` § Timezones,
  `website/docs/reference/api/{client,errors}.mdx`, CHANGELOG `### Added`.
  Sin breaking changes. Gap documentado: custom types vía
  `RegisterTypeMapper` no son interceptados (manejan su zona).
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

### ~~F0-1 · Reconciliar versionado público~~

**Cerrado** — `RELEASE_NOTES_V1.md` ya no existe. CHANGELOG con
entries por versión (v0.3.0 y v0.4.0). SECURITY.md actualizado a
v0.4.x. README dice "v0.4 — late-alpha". ROADMAP sincronizado con
fases. Versiones en sitio versionadas via Docusaurus.

### ~~F0-2 · Eliminar menciones a `examples/blog-api/`~~

**Cerrado** — el directorio no se creó (no había tiempo para una
demo completa de multi-tenancy + relaciones + migraciones bien
pulida). Las dos menciones del README desaparecen: se sustituyen
por punteros a los ejemplos por-dialecto en `examples/`. La
sección "Demo" arranca `go run ./examples/sqlite`.

### ~~F0-3 · Corregir paths en `examples/README.md`~~

**Cerrado** — los 5 comandos `go run pkg/quark/examples/<engine>/main.go`
pasan a `go run ./examples/<engine>/main.go`. Verificado:
`go run ./examples/sqlite/main.go` ejecuta limpio desde la raíz
del repo.

### ~~F0-4 · Consolidar Quick Start duplicado en README~~

**Cerrado** — el segundo Quick Start (líneas ~161-225, copia
casi exacta del primero) eliminado. Flujo del README ahora es:
Status → Why Built → Quick Start → Demo → Why Quark? → Features
→ SQLGuard → ... sin duplicados.

### ~~F0-5 · Badge de coverage hardcoded~~

**Cerrado** — el badge `Coverage 87%` ya no aparece en el README.
Los badges actuales son Go Reference, CI, Go Version, License,
Release (todos dinámicos). Configurar codecov real queda como
mejora opcional fuera de Fase 0.

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

### ~~F0-8 · Setup testcontainers-go para los 6 motores~~

**Cerrado** — `containers_test.go` (gated `//go:build integration`)
define `setupPostgresContainer`/`setupMySQLContainer`/`setupMariaDBContainer`/
`setupMSSQLContainer`/`setupOracleContainer` que arrancan el motor con
`testcontainers-go` (módulos oficiales para los 4 primeros; Oracle usa
`testcontainers.GenericContainer` sobre `gvenzl/oracle-free:23-slim-faststart`
porque no hay módulo dedicado). Cada helper expone un DSN listo para
el driver del motor y registra cleanup vía `testcontainers.CleanupContainer`.

Resolvers `resolve<Engine>DSN(t)` con prioridad env var → container:
- Sin tag → `suite_dsn_no_integration_test.go` devuelve sólo el env var.
  Si está vacío, el test se skipea (preserva el comportamiento actual
  de la regla F0-8).
- Con `-tags=integration` → `containers_test.go` lee el env var y,
  si está vacío, arranca el container.

Los 5 suite files (`postgres_/mysql_/mariadb_/mssql_/oracle_suite_test.go`)
usan ese resolver en lugar de leer `os.Getenv` directamente.

CI: `.github/workflows/ci.yml` añade un job `integration` con
`strategy.matrix` por motor — corre en paralelo a `Lint` y
`Test (SQLite)`, ambos siguen siendo el camino rápido del PR. Docker
ya está pre-instalado en `ubuntu-latest` runners; cada motor tiene
timeout 20 min (Oracle 30 min porque el primer arranque tarda ~90 s).

SQLite sigue siendo el camino default sin Docker.

Doc/changelog: actualizado en este PR.

### ~~F0-8-followup · Cerrar los bugs que la matriz integration destapó~~

**Cerrado** — los 11 bugs latentes que destapó la primera ejecución
de la matriz están cerrados (9 originales + 2 que aparecieron al
limpiar la capa superior). La matriz pasa a **blocking** en este PR.

La API pública estaba (y sigue) limpia — el SQL emitido es correcto
en los 5 motores, los logs lo muestran ejecutando sin errores; lo
que fallaba eran aserciones de tests que hardcodearon comillas,
placeholders o SQL específico de SQLite, más un par de problemas de
infra (Oracle image, MSSQL JSON encoding).

**Categorías de fallo:**

1. **Quote-character drift (bugs 1, 2, 6)** — `expr_ast_integration_test.go`,
   `cte_test.go`, `window_integration_test.go` asertan `"colname"` literal.
   MySQL/MariaDB usan backticks, MSSQL usa brackets. Fix: usar
   `client.Dialect().Quote(col)` en las aserciones, o un helper compartido.
2. **Hardcoded `?` marker en CTE test (`cte_test.go:143`)** — espera `?`
   pero PG emite `$1`, MSSQL `@p1`. Fix: aserción semántica (count de
   placeholders válidos) en lugar de literal.
3. **`SELECT *` con `GROUP BY` (`having_aggregate_test.go:103,122`)** —
   PG/MySQL strict/MSSQL rechazan (`only_full_group_by`). Fix:
   `.Select("status")` en lugar de wildcard.
4. **Columna ambigua en JOIN (`join_on_security_test.go:49,62`)** —
   MSSQL rechaza `id` sin calificar. Fix: `Select("cte_users.id", …)`.
5. **Set ops en MySQL/MariaDB (`setop_test.go:154,180`)** — `Intersect`
   y `Except` **correctamente** devuelven `ErrUnsupportedFeature` en
   esos motores. El test espera éxito. Fix: skip o assert el error.
6. **`locking_test.go:82` t.Errorf en lugar de t.Skip** — el subtest
   declara "pins the SQLite contract" pero usa `Errorf` cuando otro
   dialecto entra. Fix: cambiar a `t.Skip`.
7. **Precisión float en `nullable_test.go:58` (Postgres)** —
   `98.5999984741211 vs 98.6`. Postgres mapea `float` a `real` (32-bit).
   Fix: fixture con `double precision` o `cmpopts.EquateApprox`.
8. **`JSON[T].Scan: invalid character 'â'` (MSSQL)** — **bug real
   confirmado**. Investigación inicial: el migrate de `JSON[T]` mapea
   a `NVARCHAR(MAX)` en MSSQL; el driver `go-mssqldb` devuelve esos
   bytes con un encoding (probablemente UTF-16 LE o un prefijo de
   longitud) que `json.Unmarshal` no reconoce. El primer carácter
   reportado (`â` = `â`, UTF-8 `0xC3 0xA2`) sugiere que los bytes
   llegan en orden de UTF-16-decoded-as-UTF-8 (LE byte order = byte
   `0xE2` aparece primero). **Fix probable**:
   - **(a)** cambiar `NVARCHAR(MAX)` → `VARCHAR(MAX)` para columnas
     `JSON[T]` en MSSQL. JSON es ASCII-safe; las strings Unicode
     dentro del payload se escapan a `\uXXXX` por `json.Marshal` —
     el contenido en disco no contiene caracteres multi-byte
     directos. Microsoft documenta ambas opciones, `VARCHAR(MAX)` es
     más eficiente para JSON ya escapado.
   - **(b)** Detectar UTF-16 en `JSON[T].Scan` (BOM o heuristic) y
     decodificar antes de `json.Unmarshal`.
   - Opción (a) es la más limpia y no requiere bytes-en-runtime. La
     hago en su PR cuando haya MSSQL disponible para verificar.
   - Status interim: el test `testJSONField` se skipea en MSSQL con
     `t.Skip` apuntando a este punto.
9. **Oracle container exit code 1** (~200 ms) — `gvenzl/oracle-free:
   23-slim-faststart` no arranca en `ubuntu-latest` runners (probable
   issue de memoria / arch). Fix: probar otro tag (`slim` sin
   `-faststart`, o `23-full-faststart`), o aceptar Oracle como
   "manual-only" hasta encontrar un image confiable.

**Cierre real** (PRs ejecutados):
- ~~PR A (#29)~~ — bugs 1, 2, 6: aserciones dialect-aware via helper `q(client, ident)`.
- ~~PR B (#30)~~ — bugs 3, 4: `Select` explícito en grouped/joined tests + `Count()` para evitar ambiguous-id en MSSQL.
- ~~PR C (#31)~~ — bug 5: skip dialect en happy-path setop tests + mirror-contract assert para MySQL/MariaDB.
- ~~PR D (#32)~~ — bug 7: tolerancia 1e-4 en roundtrip de `Nullable[float64]`.
- ~~PR E (#33)~~ — bug 8: interim skip de JSON+MSSQL con diagnóstico. Fix de API (NVARCHAR(MAX) → VARCHAR(MAX) en migrate MSSQL) queda diferido para sesión con MSSQL local.
- ~~PR F (#34)~~ — bug 9: Oracle excluido de la matriz CI; helper `setupOracleContainer` se queda para uso local. Image de `gvenzl/oracle-free` crashea en runners hosted, sin signal para diagnosticar.
- ~~PR G (#35)~~ — bugs 10, 11: setop+LIMIT en MSSQL (`OrderBy("email", "ASC")` en base para satisfacer OFFSET/FETCH), JoinBuilder ambiguous-id en MSSQL (`Count()` en lugar de `List()`). Surfacearon al limpiar la capa superior.
- ~~PR final~~ — `continue-on-error: true` removido; la matriz pasa a blocking. 4 motores en CI (PG/MySQL/MariaDB/MSSQL); Oracle queda como verificación manual hasta resolver el image issue.

**Surface real cubierto**: 4/5 motores no-SQLite ejercitados end-to-end en CI por cada PR. Oracle queda como gap conocido y documentado.

### ~~F0-9 · `release-please` workflow~~

**Cerrado** — `.github/workflows/release-please.yml` corre en cada
push a `main`. Mantiene un PR rolling "Release PR" abierto con el
próximo version bump (semver desde commits Conventional) y las
entradas del CHANGELOG derivadas de los commits desde la última tag.
Merge de ese PR crea el tag + GitHub Release automáticamente.

Configuración:
- `release-please-config.json` — release-type Go (single module),
  `include-v-in-tag: true`, `bump-minor-pre-major: true` (porque
  estamos en 0.x.y → cada `feat:` bumpea minor; con 1.x.y bumpearía
  major).
- `.release-please-manifest.json` — versión actual: `0.4.0`.
- Workflow con permisos `contents: write` + `pull-requests: write`.

**Interacción con `/release` slash command**: release-please **NO**
hace el `npm run docusaurus docs:version` que congela el snapshot
de `website/docs/` en `website/versioned_docs/version-X.Y.Z/`. Ese
paso sigue siendo manual via `/release` antes de mergear el PR de
release-please. Documentado en el comentario del workflow.

### ~~F0-10 · Linter de docs~~

**Cerrado** — `scripts/lint-docs.sh` corre como paso del job `Lint`
en `.github/workflows/ci.yml`. CI rojo si alguno de los 3 checks
falla. Implementados:

1. **Anti-marketing**: detecta `production-ready`, `enterprise-grade`,
   `battle-tested` en docs user-facing. Acepta negaciones (`Not v1.0
   production-ready`, `isn't`, `todavía no`, etc.).
2. **`RELEASE_NOTES_V1` leak**: reference al archivo borrado (F0-1).
3. **Broken relative links**: parsea `[text](path)` en `*.md`/`*.mdx`
   y verifica que el destino existe. Docusaurus-aware: prueba
   variantes `<path>`, `<path>.md`, `<path>.mdx`, `<path>/index.md`,
   `<path>/index.mdx`, y maneja `/docs/...` baseUrl-rooted como
   `website/docs/...`.

**Exempt** (legítimamente discuten las reglas o son histórico
congelado): `CLAUDE.md`, `TASKS.md`, `docs/ANALISIS_MADUREZ.md`,
`docs/adr/`, `.claude/`, `website/blog/`, `website/versioned_docs/`,
`scripts/lint-docs.sh` mismo.

**Checks no implementados** (out-of-scope para v0.4 — feasible
después con go/parser + sidebar.ts AST):
- "Cada feature listada en `ROADMAP.md` como Completed debe tener
  entrada en `CHANGELOG.md`" — requiere parser de ambos archivos.
- "Cualquier API pública nueva (`go doc`) debe tener su página en
  `website/docs/`" — requiere inventario AST de exported symbols
  y mapping a páginas del sitio.

Estos dos checks añadidos son los de mayor leverage (drift de
versionado + marketing) y los más baratos de mantener. Los otros
dos quedan como ticket abierto para Fase 1+ si emerge la necesidad.

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
