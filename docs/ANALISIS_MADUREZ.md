# Análisis de madurez de Quark ORM

> **Fecha:** 2026-05-10
> **Alcance:** auditoría estática del repositorio `github.com/jcsvwinston/quark`, comparativa con ORMs Go y de otros lenguajes, y plan de fases para cerrar las brechas detectadas.
> **Tono:** crítico, sin marketing. Escrito a petición del autor para tener un mapa honesto del estado real.

---

## 0. TL;DR — el veredicto en una página

Quark **no es un "toy ORM"**. Tiene cuidado por encima de la media en aspectos donde la mayoría de ORMs Go fallan: dialectos MSSQL/Oracle (con `MERGE`, `OFFSET/FETCH`, `last_insert_rowid()`, `SCOPE_IDENTITY`), guard anti-inyección con whitelist de identifiers/operadores, multi-tenancy con tres estrategias, savepoints reentrantes en transacciones, middleware/observers con OpenTelemetry, y caché L2 con invalidación por tags. Eso lo coloca, en superficie, **por encima de bun y al nivel funcional de GORM** en algunas dimensiones.

Pero **no es enterprise-grade en el sentido en que lo es Hibernate, EF Core, SQLAlchemy o ent**. Las carencias son sistémicas, no cosméticas:

1. **El query builder es plantillería con clones, no un AST componible.** Sin CTEs, sin window functions, sin `UNION/INTERSECT`, sin `FOR UPDATE / SKIP LOCKED`, sin subqueries componibles tipadas. Cualquier consulta no-trivial cae en `RawQuery` (gateado por flag de seguridad).
2. **Las migraciones detectan diff sólo a nivel "nombre de columna"**: no perciben cambios de tipo, NOT NULL, defaults, índices ni FKs. No hay locking distribuido (advisory lock / `GET_LOCK`), no hay diff reversible automático estilo Atlas/Alembic.
3. **El RLS es `WHERE` inyectado en cliente**, no Row-Level Security real del motor. Y existe un bug latente: `Or()` no propaga `tenantID/tenantCol`, lo que es **una fuga de aislamiento de seguridad**.
4. **Reflect-everywhere.** Los generics están en la firma (`Query[T]`, `Page[T]`), pero el núcleo (`scanRow`, `loadRelations`, `buildInsert`, `saveAny`) sigue pagando `reflect.Value.Field` por columna y por fila. No hay codegen estilo `sqlc`/`ent`.
5. **Tipos pobres.** Sin `decimal.Decimal`, sin `uuid.UUID` nativo, sin arrays Postgres, sin JSON tipado, sin enum, sin tratamiento serio de timezones. UUID se mapea como `VARCHAR(36)`.
6. **Tests cubren bien SQLite, frágilmente todo lo demás.** Sin testcontainers; los suites de Postgres/MySQL/MSSQL/Oracle/MariaDB están gateados por DSN env-var. Cero tests de NULL, unicode, timezone, deadlocks, savepoints reentrantes, concurrencia real. Los benchmarks no usan `testing.B` ni baseline contra `database/sql`.
7. **Documentación versionada incoherente.** `RELEASE_NOTES_V1.md` anuncia v1.0.0 production-ready; `CHANGELOG.md` sólo tiene 0.1.0/0.1.1; `SECURITY.md` dice "pre-1.0"; el README dice "v0.x". Ejemplos rotos (`examples/blog-api/` no existe). Para un evaluador externo esto es señal de "se vende mejor de lo que se mantiene".
8. **Bugs reales detectables hoy** (sin ejecutar nada): `linkM2M` traga errores silenciosamente; `Or()` pierde RLS; `WhereJSON` concatena el path con `fmt.Sprintf` sin escapar (riesgo de inyección); cache key serializa args con `%v` (colisiones `int64(1)` vs `string("1")`); `isZeroValue` impide poner `bool=false`/`int=0`/`""` con `Update(entity)`.

**Posicionamiento honesto:** Quark está en **estadio MVP avanzado / late-alpha**, no en v1.0 production-ready. Su lugar competitivo defendible no es "GORM mejor", sino **"el ORM idiomático del ecosistema Nucleus con soporte serio de MSSQL/Oracle/multi-tenancy"** — un nicho donde GORM es flojo (Oracle) y ent es pesado (codegen + esquema graph-based).

Llegar a un v1.0 que merezca el nombre requiere **6–12 meses de trabajo enfocado** en seis ejes que detallo en §5.

---

## 1. Estado actual — auditoría módulo a módulo

### 1.1 Query builder (`query_builder.go`, `query_exec.go`, `query_crud.go`)

**Lo implementado:** patrón inmutable con `clone()`; `Where`/`WhereIn`/`WhereBetween`/`WhereNot`/`WhereJSON`/`Or` con grupos anidados; `Join/LeftJoin/RightJoin`; `GroupBy`+`Having`; `Distinct`, `Select`, `OrderBy`, `Limit`/`Offset`, `Apply(scopes…)`; agregados `Sum/Avg/Min/Max`; `Count`, `Find`, `First`, `List`, `Iter`, `Cursor`, `Paginate`. Eager loading **correcto** (segundo SELECT con `IN(…)`, no JOIN cartesiano). Wrapper `timeScanner` para drivers MySQL que devuelven `[]byte` en `DATETIME`. Inyección automática de `ORDER BY` para `OFFSET/FETCH` en MSSQL/Oracle.

**Lo que falta para ser enterprise:**
- **Sin árbol de expresiones componible.** Un `Where` es `column op value`; no se puede hacer `Where(otroQueryComoSubconsultaTipada)`.
- **Sin CTEs (`WITH`/`WITH RECURSIVE`)**, sin `UNION/INTERSECT/EXCEPT`, sin window functions (`OVER (PARTITION BY …)`).
- **Sin locking pesimista** (`FOR UPDATE`, `FOR SHARE`, `SKIP LOCKED`) ni optimista (`version` automático).
- **`Join` recibe `onClause` como string raw que NO pasa por el guard** y se concatena al SQL (`query_exec.go:467`). Inconsistencia de seguridad: `WHERE col` se valida; `JOIN ON` no.
- **`HAVING` sobre agregados (`COUNT(*)`) falla** porque la columna pasa por `ValidateIdentifier` que rechaza paréntesis.
- **`List()` aplica un límite silencioso de 100 filas** si no se llamó a `Limit()`. Útil como red de seguridad pero **trunca sin error**, comportamiento sorpresivo respecto a GORM/ent.
- **Eager loading no chunkea las claves** (Oracle 1000-IN-limit no respetado salvo en `DeleteBatch`). **Sin nested preload** (`User.Orders.Items` no expresable).
- **Reflect por fila** sin codegen.

**Bug real:** `Or()` crea un `BaseQuery` "blanco" sin copiar `tenantID`/`tenantCol`/`cache`/`limits` (`query_builder.go:175-186`). Cualquier query con `Or()` bajo RLS **rompe el aislamiento entre tenants**. Esto no es un edge case — es un bug de seguridad explotable.

### 1.2 Dialectos (`dialect.go`)

**Lo implementado:** PostgreSQL, MySQL, MariaDB (embebe MySQL + sequence + system-versioned), SQLite, MSSQL, Oracle. Cada uno con `Placeholder` correcto (`$1`/`?`/`@p1`/`:1`), `Quote`, `LimitOffset` con la sintaxis específica, `RETURNING`, `LastInsertIDQuery`, `JSONExtract`, DDL básico, `UpsertSQL`, `BuildRoutineQuery`. Registro de dialectos custom (`RegisterDialect`).

**Lo que merece destacarse positivamente:**
- **Upsert por dialecto correcto:** `ON CONFLICT … DO UPDATE` (PG/SQLite), `ON DUPLICATE KEY UPDATE` (MySQL/MariaDB), `MERGE … USING (VALUES …)` construido a mano para MSSQL/Oracle. Esto está por encima de bun y a la par con ent.

**Lo que falla:**
- **`JSONExtract` PG hace sólo `(col)::jsonb->>'path'`** — un nivel, sin `#>`/`@>`/JSONB containment ops.
- **Path JSON concatenado con `fmt.Sprintf` sin escapar** — un `'` en el path rompe el SQL. **Riesgo de inyección si `WhereJSON` recibe path no controlado.**
- **Oracle uppercasea identifiers automáticamente** sin opción de desactivarlo — rompe esquemas case-sensitive entre comillas.
- **Sin arrays Postgres nativos** (`ARRAY[…]`), sin `tstzrange`, sin `hstore`, sin `UUID` nativo, sin `bytea` tipado.
- **La interfaz `Dialect` mezcla SQL builder + DDL + procedures + JSON.** Cuando MariaDB añade `CreateSequence`/`HistoryQuery`, sólo accesibles via type-assert. No es ortogonal.

### 1.3 Migraciones (`migrator.go`, `sync.go`, `migrate/migrate.go`)

**Dos sistemas paralelos:**
1. **Auto-migración** (estilo GORM `AutoMigrate`): `CREATE TABLE IF NOT EXISTS` desde reflect, composite PK, índices, FKs, join tables M2M, `Sync` con introspección.
2. **Migraciones versionadas** (estilo Flyway/Alembic): registry `map[string]*Migration`, tabla `quark_migrations`, `Up/Down/UpDryRun`.

**Carencias graves frente a Flyway/Liquibase/Alembic/Atlas:**
- **No detecta drift real.** Sólo compara nombres de columnas (`sync.go:84`). Cambios de tipo, NOT NULL, defaults, índices y FKs son invisibles. Si cambias `VARCHAR(255)` a `TEXT` o añades `NOT NULL`, `Sync` no nota nada.
- **Sin diff reversible automático.** El `Down` se escribe a mano siempre.
- **Sin locking distribuido.** Dos pods aplicando `Up` en paralelo es race condition garantizada. PG `pg_advisory_lock` o MySQL `GET_LOCK` no se usan.
- **El registry de migraciones es global mutable** — `migrate/migrate.go:19`, `Reset()` para tests. Anti-patrón: si tienes dos clientes en el mismo proceso, comparten registry.
- **El migrator versionado no envuelve cada `Up` en transacción.** En MySQL (sin DDL transaccional), una migración a media puede dejar el schema inconsistente sin marca de versión.
- **`internal/migrate.SQLType` muy pobre:** Strings → `VARCHAR(255)` siempre, sin opción de longitud por tag; no hay `decimal.Decimal`, no hay `uuid.UUID`, no hay `time.Duration`, no hay arrays.

### 1.4 Hooks y eventos

**Hooks:** seis interfaces (`BeforeCreate/AfterCreate/Before/AfterUpdate/Before/AfterDelete`). Carencias: sin `BeforeSave/AfterSave`, sin `BeforeFind/AfterFind`, sin hooks de transacción (`OnCommit/OnRollback`). **Sin orden definido entre hooks múltiples.** **Hooks fuera de la transacción** salvo si el caller la abrió — un `AfterCreate` que falle no revierte el INSERT. `BeforeUpdate` no se llama en `UpdateMap` ni `UpdateBatch` — inconsistencia.

**Eventos (`events.go`):** **placeholder.** `EventBus.CreateListener` devuelve `ErrDialectNotSupported` salvo para PG `pg_notify`. No es una capacidad real.

### 1.5 Cache (`cache.go`, `cache/memory`, `cache/redis`)

**MVP correcto** con interfaz pluggable, TTL, invalidación por tags. **Carencias:**
- **Sin protección contra cache stampede** (singleflight, jitter, probabilistic early expiration). Bajo carga, expirar una clave caliente desencadena tormenta.
- **Invalidación grosera por tabla** — actualizar una fila invalida todas las queries cacheadas de la tabla. Sin invalidación por PK ni por filas afectadas.
- **Cache key serializa args con `%v`** — `int64(1)` y `string("1")` colisionan. Bug latente.
- **TTL del tag-key Redis** = `ttl + 24h`: si el tag se reescribe con TTL distinto, no se actualiza al máximo, leaks potenciales.

### 1.6 SQL Guard / seguridad

**Buena base:** `ValidateIdentifier` con regex estricta + blacklist de keywords; `ValidateOperator` con whitelist; `ValidateRawQuery` con regex anti-`UNION SELECT`/`OR 1=1`.

**Pero:**
- `JOIN ON expr` **se concatena sin validar**. Inconsistencia.
- Anti-injection con regex es heurístico; `UNION/**/SELECT` y comentarios `--` no se filtran. Para defense-in-depth haría falta un parser SQL.
- Blacklist incompleta (no incluye `MERGE`, `WITH`, `WINDOW`).
- `maxIdentifierLen=64` rompe Postgres (63 max). No configurable.

### 1.7 Multi-tenancy (`tenant_router.go`)

Tres estrategias: `DatabasePerTenant` con LRU de Clients, `SchemaPerTenant`, `RowLevelSecurity`.

**Lo gordo:** **el RLS NO es Row-Level Security del motor**, es WHERE-injection en cliente. El propio comentario lo admite. `client.Raw()` y `Exec` se saltan la inyección. **Y el bug de `Or()` mencionado arriba abre fugas entre tenants.** Para un ORM que vende multi-tenancy como bandera, esto es el mayor riesgo.

### 1.8 OpenTelemetry (`otel/otel.go`)

Spans con `db.statement`/`db.operation`. **Sin métricas** (counters, latency histograms). **Sin redacción de PII en `db.statement`** — los argumentos van en el SQL serializado; en jurisdicciones con GDPR/HIPAA esto es problemático sin opción de redaction. `WrapQueryRow` no captura error porque `Scan` ocurre después del span (limitación del API; no se difiere `End()`).

### 1.9 Tests y calidad

- **Sólo SQLite corre por defecto.** Postgres/MySQL/MSSQL/Oracle/MariaDB requieren env vars manuales. **Sin testcontainers.** En CI sin configuración externa, la red de seguridad multi-motor es nula.
- **Tests retrospectivos de bugs** (`p0_fixes_test.go`, `n_fixes_test.go`) confirman que los bugs serios se descubrieron por auditoría externa, no por TDD.
- **Cero tests de NULL genéricos, unicode, timezones, deadlocks, savepoints anidados, concurrencia real.**
- **`stress_test.go`** ejecuta 1000 inserts secuenciales — no estresa nada. **`benchmark_test.go` no contiene `func Benchmark*(b *testing.B)`** — no se compara contra `database/sql` puro ni contra GORM.

### 1.10 Documentación

Versionado contradictorio (v1.0 anunciado vs CHANGELOG en 0.1.1 vs SECURITY pre-1.0 vs README v0.x). `examples/README.md` con paths rotos a un layout de monorepo previo. Quick Start duplicado en README. `examples/blog-api/` referenciado pero inexistente. **Pero**: `docs/comparison.md` y `docs/benchmarks.md` son **honestos** (admiten que GORM/ent también previenen inyección, etiquetan benchmarks como no apples-to-apples). Governance (LICENSE Apache 2.0, CONTRIBUTING, SECURITY, CoC, templates) está bien.

---

## 2. Comparativa con el estado del arte

### 2.1 ORMs en Go

| Capacidad | **Quark** | GORM | ent (Meta) | bun | sqlc | sqlboiler | jet | Diesel/Rust* |
|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| Tipo de aproximación | reflect+tags+generics ligeros | reflect+tags | codegen graph | reflect+tags | codegen SQL→Go | codegen DB→Go | codegen typesafe | codegen typesafe |
| Multi-dialecto | 6 (incl. Oracle/MSSQL) | 6 | 5 | 4 | 5 | 5 | 4 | PG/MySQL/SQLite |
| **Oracle real** | ✅ con `MERGE` | ⚠️ flojo | ❌ | ❌ | ⚠️ | ❌ | ❌ | ❌ |
| **MSSQL real** | ✅ `MERGE`+identity | ✅ | ⚠️ | ❌ | ✅ | ⚠️ | ❌ | ❌ |
| Eager loading | ✅ IN-batch | ✅ + nested | ✅ graph traversal | ✅ | n/a | ✅ | n/a | ✅ |
| **Nested preload** | ❌ | ✅ | ✅ | ✅ | n/a | ✅ | n/a | ✅ |
| **CTEs / window** | ❌ | ⚠️ raw | ⚠️ | ✅ | ✅ (escribes SQL) | ⚠️ | ✅ | ✅ |
| **UNION/INTERSECT** | ❌ | ⚠️ raw | ❌ | ✅ | ✅ | ❌ | ✅ | ✅ |
| **Locking** (`FOR UPDATE`/`SKIP LOCKED`) | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| **Optimistic locking** (version) | ❌ | ✅ plugin | ✅ | ⚠️ | ❌ | ❌ | ❌ | ⚠️ |
| Soft deletes | ⚠️ (DeletedAt manual) | ✅ | ✅ | ✅ | ❌ | ❌ | ❌ | ⚠️ |
| Migrations | esqueleto | AutoMigrate | versionadas + diff | ⚠️ | ❌ | ❌ | ❌ | ✅ Diesel CLI |
| **Schema diff real** | ❌ | ❌ | ✅ con Atlas | ❌ | ❌ | ❌ | ❌ | ✅ |
| **Lock distribuido en migración** | ❌ | ❌ | ✅ | ❌ | n/a | n/a | n/a | ✅ |
| Multi-tenancy | ✅ 3 estrategias | ⚠️ scopes | ⚠️ | ⚠️ | ❌ | ❌ | ❌ | ❌ |
| **RLS real motor** | ❌ (cliente) | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| Caché L2 | ✅ pluggable | ⚠️ plugin | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |
| **Stampede protection** | ❌ | ❌ | ❌ | ❌ | n/a | n/a | n/a | n/a |
| OpenTelemetry | ✅ traces | ✅ plugin | ✅ | ✅ | n/a | n/a | n/a | ✅ |
| **OTel metrics** | ❌ | ⚠️ plugin | ✅ | ⚠️ | n/a | n/a | n/a | ✅ |
| Read replicas | ❌ | ✅ plugin | ✅ | ⚠️ | n/a | n/a | n/a | ⚠️ |
| **Codegen typesafe** | ❌ | ❌ | ✅ | ❌ | ✅ | ✅ | ✅ | ✅ |
| Hooks transaccionales | ❌ | ✅ | ✅ | ✅ | n/a | ⚠️ | n/a | ✅ |
| Tipos ricos (decimal, UUID, arrays, JSON tipado) | ❌ | ⚠️ | ✅ | ✅ | ✅ | ⚠️ | ⚠️ | ✅ |

\* Diesel se incluye como referencia de "lo que un typesafe-first parece" en otro lenguaje.

**Posicionamiento honesto en Go:**
- **Mejor que bun y xorm en multi-dialecto serio** (especialmente Oracle).
- **Comparable a GORM en aproximación reflect-based**, pero GORM tiene ecosistema (plugins read-replicas, sharding, soft delete) que Quark no.
- **Por debajo de ent** en query builder componible, en codegen, en schema diff con Atlas, y en hooks transaccionales.
- **Por debajo de sqlc** en seguridad de tipos sobre queries arbitrarias (sqlc te da un Go method por cada `.sql`, sin reflect).
- **A favor:** caché L2 integrada (nadie en Go la trae out-of-the-box), 3 estrategias de multi-tenancy con LRU de pools, soporte real de Oracle.

### 2.2 ORMs en otros lenguajes (referencia de "qué es enterprise")

| Capacidad | **Quark hoy** | Hibernate / JPA (Java) | EF Core (.NET) | SQLAlchemy 2.x (Python) | Prisma (Node) | ActiveRecord (Rails) | Doctrine (PHP) |
|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| Persistence pattern | Active Record | Data Mapper + UoW | Change tracker + UoW | Data Mapper + Identity Map + UoW | Data Mapper | Active Record | Data Mapper + UoW |
| **Unit of Work / dirty tracking** | ❌ (envío entero el struct) | ✅ | ✅ | ✅ | ⚠️ | ✅ | ✅ |
| **Identity map** | ❌ | ✅ | ✅ | ✅ | ❌ | ⚠️ | ✅ |
| **Lazy/eager loading negociable** | ⚠️ (preload manual) | ✅ | ✅ | ✅ | ⚠️ | ✅ | ✅ |
| Query builder componible | ⚠️ | ✅ Criteria API | ✅ LINQ | ✅ | ⚠️ | ✅ Arel | ✅ DQL+QueryBuilder |
| **Lenguaje de query del ORM** | SQL semi-strings | HQL/JPQL + Criteria | LINQ-to-Entities | Core+ORM expression | Prisma Query | AR DSL | DQL |
| **Locking pesimista/optimista** | ❌ / ❌ | ✅ / ✅ | ✅ / ✅ | ✅ / ✅ | ⚠️ / ✅ | ✅ / ✅ | ✅ / ✅ |
| **Cascadas** (persist/remove/orphan) | ⚠️ recursivo limitado | ✅ JPA cascade | ✅ | ✅ | ⚠️ | ✅ | ✅ |
| Schema migrations | esqueleto | Hibernate-tools / Flyway | EF Core Migrations | Alembic | Prisma Migrate | AR Migrations | Doctrine Migrations |
| **Diff bidireccional schema↔modelo** | ❌ | ⚠️ | ✅ | ✅ Alembic | ✅ | ✅ | ✅ |
| **Read replicas / multi-DC** | ❌ | ✅ | ✅ | ✅ | ⚠️ | ✅ | ✅ |
| **Connection retry / circuit breaker** | ❌ | ✅ | ✅ EF retry policies | ✅ | ⚠️ | ⚠️ | ⚠️ |
| **Multi-tenancy nativa** | ✅ 3 estrategias | ✅ Hibernate Multi-Tenancy | ⚠️ | ⚠️ | ❌ | ⚠️ gem | ⚠️ |
| **L2 cache integrado** | ✅ memory/redis | ✅ Ehcache/Hazelcast | ⚠️ | ⚠️ Dogpile.cache | ❌ | ❌ | ✅ APC/Redis |
| **Stampede / coherencia** | ❌ | ✅ | ⚠️ | ✅ Dogpile | n/a | n/a | ✅ |
| Tipos ricos extensibles | ❌ | ✅ `AttributeConverter` | ✅ Value converters | ✅ TypeDecorator | ✅ | ✅ | ✅ |
| **Eventos transaccionales** | ❌ (placeholder) | ✅ `@PostPersist` etc. | ✅ SaveChanges interceptor | ✅ events | ⚠️ middleware | ✅ callbacks | ✅ lifecycle |
| **Generación de código tipada** | ❌ | n/a (runtime) | ⚠️ scaffolding | n/a | ✅ schema → client | ⚠️ | ⚠️ |
| Madurez | alpha-late | 20+ años | 15 años | 18 años | ~6 años | 20+ años | 15 años |

**Punto clave:** la diferencia entre Quark y Hibernate/EF/SQLAlchemy no es de features visibles — es de **patrones arquitectónicos** (Unit of Work, Identity Map, dirty tracking, cascades) que Quark no implementa. Estos patrones son lo que permite a un dev hacer `entity.Name = "x"; tx.Commit()` y que el ORM emita el `UPDATE` correcto. En Quark hay que pasar el struct entero y `isZeroValue` decide qué actualiza, lo cual rompe con `bool=false`/`int=0`/`""`. Esto no es deuda técnica menor; es una **decisión de diseño que limita el techo del producto**.

---

## 3. Brechas críticas — priorizadas por riesgo

### Bloqueantes (impacto: producción frágil o inseguro hoy)

1. **Bug de RLS en `Or()`** — fuga de aislamiento entre tenants. *Prioridad inmediata.*
2. **`linkM2M` traga errores silenciosamente** — corrupción silenciosa.
3. **`WhereJSON` con path no escapado** — vector de inyección si el path viene de input.
4. **`isZeroValue` en `Update(entity)`** — no se puede poner `false`/`0`/`""`. Sorpresivo, peligroso.
5. **Versionado documental incoherente** — credibilidad pública dañada.

### Estructurales (techo del producto bajo)

6. **Sin AST de queries** → CTEs/window/UNION/lock hints inalcanzables sin raw.
7. **Migraciones que no detectan drift de tipos/constraints** → `Sync` engaña.
8. **Sin lock distribuido en migraciones** → race en clusters.
9. **Cache sin stampede protection ni invalidación granular**.
10. **Tipos pobres** — `decimal.Decimal`, `uuid.UUID`, arrays Postgres, JSON tipado, enums, timezones.
11. **Reflect-everywhere sin codegen** — coste tanto en performance como en seguridad de tipos en compile-time.

### De ecosistema (faltan piezas para "enterprise")

12. **Sin read replicas / failover / sharding routing.**
13. **Sin métricas OTel, sin redacción de PII en spans.**
14. **Sin retry de deadlocks ni circuit breaker.**
15. **Hooks fuera de transacción y sin orden definido; sin `BeforeFind/AfterFind/OnCommit/OnRollback`.**
16. **`EventBus` es placeholder.**
17. **Sin testcontainers**: red de seguridad multi-motor depende de DSNs manuales.
18. **Cero tests de NULL genérico, unicode, timezones, deadlocks, savepoints, concurrencia real.**

---

## 4. Plan de fases para cerrar las brechas

> El plan asume **un mantenedor full-time o equivalente** y **6–12 meses**. Cada fase es coherente por sí misma y entrega un release. **No se pasa a la fase siguiente hasta que la actual está cubierta de tests.**

### **Fase 0 — Honestidad y limpieza (2–3 semanas)**
> *Objetivo: que la documentación describa la realidad y no haya bugs explotables hoy.*

- Reconciliar versionado: degradar `RELEASE_NOTES_V1.md` a `RELEASE_NOTES_v0.2.md`, taggear `v0.2.0`, alinear README/SECURITY/CHANGELOG. Política pública de SemVer.
- Crear `examples/blog-api/` o eliminar las referencias.
- Corregir paths de `examples/README.md`.
- Consolidar Quick Start duplicado.
- Reemplazar el badge de coverage estático por uno real (codecov o `go tool cover` artefacto en CI).
- **Bugfixes obligatorios** antes de cualquier feature nueva:
  - `Or()` debe propagar `tenantID/tenantCol/cache/limits/schema`.
  - `linkM2M` debe diferenciar duplicados de errores reales (parsear código del driver, no `errors.Is(err, …)` por substring).
  - `WhereJSON` debe escapar el path o validarlo contra `^[a-zA-Z_][a-zA-Z0-9_.]*$`.
  - Documentar explícitamente la trampa de `isZeroValue` en `Update(entity)` y exponer `Updates(map[string]any)` como API recomendada para mutaciones parciales — o, mejor, introducir `UpdateChangedFields(entity, prev)`.
  - Validar `JOIN ... ON` con un parser mínimo (al menos: `[id.][id] op [id.][id]` o referencia a columnas conocidas).
- **Suite de tests con testcontainers-go** para los 6 motores. CI corriendo los 6.
- Tests específicos para los bugs fixados (regresión).

**Salida:** v0.2.0 sin marketing inflado, sin bugs P0, con CI multi-motor.

---

### **Fase 1 — Tipos ricos y dirty tracking ligero (4–6 semanas)**
> *Objetivo: dejar de ser un mapeador de structs ingenuo.*

- **Tipos ricos**: integrar `shopspring/decimal`, `google/uuid`, `time.Duration`, mapeo correcto de timezones (default UTC + override por columna), `[]byte` como `BLOB/BYTEA/VARBINARY`, JSON tipado vía generics (`JSON[T any]`).
- **Arrays Postgres** (`pgtype.Array`) detrás de un wrapper neutro.
- **`Nullable[T]`** genérico que reemplace los hacks `*time.Time` / `sql.NullString`.
- **Migrar `internal/migrate.SQLType`** a un sistema extensible: `RegisterTypeMapper(reflect.Type, dialect, fn)`. Permitir longitud por tag (`db:"name,size=512"`).
- **Dirty tracking ligero**: snapshot del struct al cargar (vía `Find/First/List` opcional con `Track()`) y `Save()` que sólo emite UPDATE de campos cambiados. Esto cierra la herida de `isZeroValue` sin pedir Unit of Work completo.
- **Soft delete real** con scope `WithTrashed()` / `OnlyTrashed()` automático cuando el modelo tiene `DeletedAt *time.Time`.
- **Optimistic locking** con tag `quark:"version"`: incrementa y añade a `WHERE` en cada UPDATE; error tipado `ErrStaleEntity`.

**Salida:** v0.3.0 con tipos completos y mutaciones parciales correctas.

---

### **Fase 2 — Query builder componible y locking (6–8 semanas)**
> *Objetivo: que cualquier consulta enterprise se escriba sin caer en `RawQuery`.*

- **AST de expresiones**: tipo `Expr` con `Col(...)`, `Lit(...)`, `Func(name, args…)`, `And/Or/Not`, `Cast`, `In(subquery)`, `Exists(subquery)`. `Where` y `Having` aceptan `Expr`.
- **Subqueries tipadas componibles**: `For[Order](ctx, c).Where(...).AsSubquery()` integrable en otro `Where`/`Join`.
- **CTEs**: `With("t", subq).For[T]().Where(...)`. `WithRecursive`.
- **Window functions**: `OverWindow(name).PartitionBy(...).OrderBy(...)` y método `RowNumber()/Rank()/Lag()`.
- **`UNION/INTERSECT/EXCEPT`** entre `Query[T]`.
- **Locking**: `.ForUpdate()`, `.ForShare()`, `.SkipLocked()`, `.NoWait()` por dialecto (con fallback `error UnsupportedFeature`).
- **`HAVING` sobre agregados**: `Having(Func("count", Col("*")), ">", 5)`.
- **Nested preload**: `.Preload("Orders.Items.Product")` con planificación batch.
- **Chunking automático** de `IN(...)` por dialecto (Oracle 1000, MSSQL 2100 params).

**Salida:** v0.4.0 con un query builder al nivel de bun/Hibernate Criteria.

---

### **Fase 3 — Migraciones serias y schema-as-code (6–8 semanas)**
> *Objetivo: emparejarse con Alembic/EF Migrations/Atlas.*

- **Schema diff real**: introspección completa (tipos, NOT NULL, defaults, índices, FKs, checks) y comparador estructural con el modelo Go. `quark schema diff` que emite migración up+down candidata.
- **Lock distribuido**: PG `pg_advisory_xact_lock`, MySQL `GET_LOCK`, MSSQL `sp_getapplock`, Oracle `DBMS_LOCK.REQUEST`.
- **Migración transaccional** donde el motor lo permita; en MySQL, emitir resumable migrations con state checkpointing.
- **Dry-run con plan de cambios** (estilo `terraform plan`) que muestra DDL up/down y warnings (drop columns, narrowing types, lossy conversions).
- **Estilo expand-contract** documentado y soportado vía guía.
- **Backfill orquestado**: `Migration.Backfill(fn func(*Tx) error, batchSize int)` que itera por PK con resume token.
- **Sustituir registry global** por registry por cliente.

**Salida:** v0.5.0 con migraciones que un equipo serio aceptaría.

---

### **Fase 4 — Observabilidad y caché de producción (3–4 semanas)**
> *Objetivo: que en prod sepas qué pasa y la caché no se incendie.*

- **OTel metrics**: counter `quark.queries.total`, histogram `quark.queries.duration` y `quark.queries.rows`, etiquetados por `db.system`, `db.operation`, `db.table`.
- **Redacción de SQL en spans**: opción `WithSpanRedaction(quark.RedactArgs)` que sustituye args por `?` en `db.statement` (default ON; opt-out explícito).
- **Slow query log** estructurado con threshold configurable.
- **Caché**:
  - **Singleflight** para cache stampede.
  - **TTL con jitter** (±10% configurable).
  - **Invalidación granular por PK** además de tabla. Mutaciones registran las PKs afectadas y emiten invalidaciones precisas.
  - **Cache key** con args serializados con `gob` o longitud-prefijada (no `%v`).
  - **Negative caching** opcional para `Find` que devuelve "no rows".
- **Retry de deadlocks** detectado por código de error del driver (`pq.Error.Code == "40P01"`, MySQL 1213, MSSQL 1205, Oracle ORA-00060). Exponential backoff con jitter, máx N intentos. Disabled por default; `WithDeadlockRetry(3)` lo habilita.

**Salida:** v0.6.0 con observabilidad y caché defendibles en SRE review.

---

### **Fase 5 — RLS real, hooks transaccionales y eventos (4–6 semanas)**
> *Objetivo: que el RLS sea de verdad y los hooks se puedan razonar.*

- **RLS real Postgres**: que `RowLevelSecurity` strategy emita `SET LOCAL app.tenant_id = $1` por transacción y delegue en `CREATE POLICY` del motor. Documentar el setup SQL requerido (policy templates en CLI: `quark tenant install-rls-policies`).
- **Hooks transaccionales**: `BeforeSave/AfterSave/BeforeFind/AfterFind`, `OnCommit(tx, fn)`, `OnRollback(tx, fn)`. Orden estable por declaración. Por defecto **dentro de la transacción del save**.
- **`EventBus` real**: emisor que publica entity events (Created/Updated/Deleted) a un sink (logger, OTel, NATS/Kafka via plugin externo). Reemplazar el placeholder de `pg_notify`-only.
- **`AuditLog` opcional** con tabla `quark_audit` (configurable) que graba operación + diff + usuario (extraído de `ctx`).

**Salida:** v0.7.0 con RLS real y semántica de hooks defendible.

---

### **Fase 6 — Codegen, performance y HA (8–12 semanas)**
> *Objetivo: cerrar la brecha de performance y entrar en territorio enterprise.*

- **Codegen opcional** (`quark gen models`) que emite por cada modelo:
  - Scanner tipado sin reflect (al estilo de sqlc).
  - Insert/Update batch con bind manual.
  - Constructor de `Query[T]` con campos como métodos tipados (`Where().Name().Eq("x")`).
  - Esto lo dejamos opt-in: el reflect path sigue para developers que prefieran simplicidad; el codegen path para los que necesiten cada microsegundo.
- **Read replicas / pool routing**: `WithReplicas(replicaDSNs...)` que enruta SELECT a réplicas (round-robin/random/least-conn) y mutaciones al primary. `Sticky(ctx)` para sesiones que requieren coherencia post-write. Healthcheck pasivo.
- **Failover** con detección de errores transitorios (driver-level `errors.Is(err, driver.ErrBadConn)` + códigos de error).
- **Sharding pluggable**: `ShardRouter` que dada una entidad y operación elige cliente. Útil para Nucleus.
- **Benchmarks proper**: `func Benchmark*(b *testing.B)` reales contra `database/sql` puro, GORM, ent, sqlc en una matriz por dialecto. Publicar en `docs/benchmarks/` con harness reproducible.
- **Stress real**: workload generator con `vegeta`/`hey` patterns: latencias p50/p95/p99 bajo concurrencia, contención de pool, deadlock rate.

**Salida:** v1.0.0 honesto. Production-ready de verdad.

---

## 5. Recomendaciones estratégicas

### 5.1 ¿Por qué existir si están GORM y ent?

Construir un ORM Go más en 2026 sólo se justifica si **se ocupa un nicho que la competencia no cubre bien**. El de Quark — si el plan se ejecuta — es:

> **"El ORM idiomático para aplicaciones empresariales con multi-tenancy real, soporte serio de Oracle/MSSQL, y cache L2 integrada — pensado para los entornos donde GORM se queda corto en motor y ent es demasiado pesado en codegen."**

Eso es defendible. **No** es defendible posicionarse como "GORM mejor", porque GORM ya tiene la red de plugins, la base de usuarios y la velocidad de iteración.

### 5.2 Decisiones arquitectónicas pendientes

- **Active Record vs Data Mapper.** Hoy Quark es Active Record (struct lleva PK y métodos vía hooks). Para llegar a Hibernate-grade haría falta un Identity Map y Unit of Work, lo cual es una refactorización mayor. **Recomendación:** mantenerse en Active Record + dirty tracking ligero (Fase 1). Es lo que esperan los devs Go.
- **Reflect vs codegen.** Dejar reflect como path por defecto y ofrecer codegen opt-in (Fase 6) es lo correcto: no fuerza un workflow nuevo a usuarios, pero da una salida a quien necesita performance.
- **Schema-first vs code-first.** Hoy Quark es code-first. Atlas/Prisma demuestran que schema-first vale para muchos equipos. **No recomiendo introducirlo todavía** — primero Fase 3 con diff bidireccional, luego evaluarlo.

### 5.3 Cómo distribuir el coste

Si esto es un side project, **no caben las 6 fases**. Sugerencia de mínimo viable para que Quark sea defendible públicamente:

- **Imprescindible (3 meses):** Fase 0 + Fase 1 + bugfixes de Fase 2 (validate `JOIN ON`, locking pesimista, nested preload). Versión `v0.3` honesta.
- **Si llegas a 6 meses:** Fase 2 completa + Fase 3 hasta lock distribuido. `v0.5` defendible.
- **El resto puede esperar** si se posiciona honestamente.

### 5.4 Lo que no haría

- **Read/write splitting** antes de Fase 4. Sin métricas serias, optimizar prematuramente.
- **GraphQL/admin auto-generado.** Es lo que ent y Hasura hacen bien; entrar ahí es perder foco.
- **Soporte de NoSQL.** Quark es relacional; mantenerlo así.
- **Vendor lock-in con Nucleus.** El roadmap menciona "Standalone GoFrame Module" — confirmar que el módulo es importable sin Nucleus es crítico para la adopción externa.

---

## 6. Cierre

Quark **tiene fundamentos sólidos** — el cuidado en dialectos MSSQL/Oracle, savepoints reentrantes, multi-tenancy con LRU, caché L2 y observers no son habituales en ORMs MVP. **Pero la documentación está vendiendo un v1.0 que el código no respalda**, y hay bugs explotables (RLS en `Or`, JSON path injection, `linkM2M` silente) que son obligación arreglar antes de cualquier feature nueva.

El camino a un v1.0 honesto pasa por **renunciar al marketing de "enterprise-grade" hoy, taggear realmente como v0.2/v0.3, y ejecutar las Fases 0–3 con disciplina de tests**. Ese Quark — multi-tenancy de verdad, Oracle/MSSQL serios, query builder componible, migraciones con diff — sí ocuparía un nicho real que ni GORM ni ent cubren del todo.

— *Análisis preparado a petición del autor; iterable.*
