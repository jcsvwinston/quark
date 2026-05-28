# Bug-bash plan — Quark post-v1.0

> **Fecha:** 2026-05-28
> **Versión bajo prueba:** `v1.0.0` (HEAD `main`).
> **Origen:** [V1_GATE.md §B Item 6](V1_GATE.md) — bug-bash externo se difirió
> al cierre del gate; este documento es su rescate. ADR-0013 / ADR-0011 /
> ADR-0017 reconocen explícitamente que la mirada externa es la prueba que
> falta para que v1.0 no sea sólo *"v1.0 según el mantenedor"*.

## Filosofía

El bug-bash **no es una segunda suite de tests** — la suite existente
(`*_test.go`, 109 archivos) valida que las features funcionan en aislamiento.
El bug-bash valida que **funcionan juntas, con datos reales, en los 6
motores, bajo cargas que no caben en un test unitario**. Las cosas que
busca:

1. **Regresiones cross-engine**: lo que pasa en SQLite y MSSQL pero falla
   en Oracle/MariaDB.
2. **Dialect-specific gaps**: SQL que el dialecto emite mal con cierta
   combinación (CTE recursivo + window + locking + paginación).
3. **Acumulación de allocs / leaks** que la suite no detecta (la suite
   crea ~10 filas; el bug-bash crea 100k-1M).
4. **Race conditions** en multi-tenancy concurrente, replicas + sharding,
   stampede + per-row invalidation.
5. **Edge cases de tipos**: `Nullable[time.Time]` con timezone tag bajo
   per-column TZ, `Array[T]` con T = struct con json tag, JSON path con
   keys que coinciden con reserved words por motor.
6. **Comportamientos sorprendentes**: lo que la doc promete vs lo que el
   API hace cuando el usuario combina cosas que la doc no documenta junto.

Filosofía operativa:

- **Cada fase es independiente**. Se puede correr una fase suelta sin las
  anteriores.
- **Cada fase es reproducible**. Mismo seed → mismo dataset → mismo
  resultado.
- **Cada fallo es reproducible**. El report incluye el reproducer mínimo
  (commit + comando + datos).
- **No hay flaky tests**. Si un test es flaky, es un bug del bug-bash, no
  un fallo intermitente del producto.

## Objetivos cuantitativos

Una pasada completa debe:

- Ejercitar **≥95% de la superficie pública de Quark** (definida como cada
  método exportado en el paquete raíz + `cache/*` + `migrate/*` +
  `cmd/quark/*` + `quarktenant/*` + `quarkmigrate/*`).
- Correr en los **6 motores** (SQLite, PG, MySQL, MariaDB, MSSQL, Oracle)
  con la misma matriz que CI bloqueante.
- Crear **≥1M registros agregados** entre dominio principal y datos de
  carga.
- Cubrir las **4 estrategias de multi-tenancy** y el routing
  replicas/sharding.
- Generar un **report estructurado** con cada fallo clasificado por tipo,
  motor afectado, severidad, y reproducer.

## Dominio de prueba

Ver [`bugbash/DOMAIN.md`](../bugbash/DOMAIN.md) para el detalle. Resumen:

> Un **ERP-SaaS multi-tenant** (15-20 tablas relacionadas) que combina
> los dos vectores que Quark se vende: muchos motores + multi-tenancy
> seria. Núcleo:
>
> - `organizations` (tenants), `users` (con polimorfismo), `roles` (m2m).
> - `products`, `categories` (jerárquico, autorelacionado), `inventory`,
>   `warehouses`.
> - `customers`, `orders`, `order_lines`, `payments`, `refunds`.
> - `invoices`, `invoice_lines`, `tax_rules`.
> - `audit_events` (tipo polimórfico — `commentable_id` style).
> - `attachments` (BLOB), `notes` (TEXT).
>
> Tipos que tocamos a propósito: `decimal.Decimal` (precio), `uuid.UUID`
> (external IDs), `Array[string]` (tags), `JSON[T]` (metadata), `Nullable[T]`
> en cada nullable, `time.Time` con timezone tag (`tz=Europe/Madrid`),
> `time.Duration` (SLAs), `[]byte` (attachments).

## Fases

Cada fase es un directorio bajo `bugbash/phases/FN-name/` con su `README.md`
y sus tests. Las fases son **acumulativas en estado** (los datos de F2
sobreviven a F3) pero **independientes en ejecución** (F3 puede correr sin
F2 si los datos ya están sembrados).

### F0 — Install & boot

**Qué prueba:** que un consumidor externo puede instalar y arrancar Quark
desde cero, en los 6 motores, con sólo el `go get` documentado.

**Ejercita:** `go get github.com/jcsvwinston/quark@v1.0.0`,
`quark.New(dialect, dsn)`, conexión, `Migrate(...)`, ping.

**Verifica:**
- Cada motor levanta su contenedor (docker run, no testcontainers — sigue
  el patrón de CI).
- `quark.New` con DSN típicos de producción no falla por config implícita.
- `Migrate(&Model{})` sobre el dominio entero no falla.
- El binario `cmd/quark` instala vía `go install` y `quark gen --version`
  responde.
- `quark gen` sobre `bugbash/domain/` emite `quark_gen.go` que compila.

**Criterio done:** 6/6 motores arrancan, dominio entero migrado, codegen
funcional.

### F1 — Smoke per engine

**Qué prueba:** que las primitivas CRUD básicas funcionan en cada motor
contra el dominio real (no contra `bench_users`).

**Ejercita:** `Create` / `First` / `Find` / `List` / `Update` /
`UpdateFields` / `Delete` / `HardDelete` / `Count` / aggregates sobre las
15-20 tablas del dominio.

**Verifica:**
- Round-trip de cada tipo (decimal, uuid, JSON[T], Array[T], time.Time TZ,
  Duration, []byte).
- `Nullable[T]` ↔ NULL en motor.
- Identifier escaping (tablas con nombres reservados como `order`, `user`).

**Criterio done:** cero diffs entre INSERT y SELECT para cada tipo en
cada motor. Cero "ORA-XXXXX" / "mysql: error 1xxx" salvo los documentados
como esperados.

### F2 — API surface coverage

**Qué prueba:** **cobertura del 95% de la API pública**. Para cada método
exportado, un caso que lo ejerza con datos del dominio.

**Ejercita:**
- Query builder completo: `Where` (todos los operadores válidos), `WhereIn`,
  `WhereBetween`, `WhereNot`, `WhereJSON`, `Or` (grupos anidados),
  `Join`/`LeftJoin`/`RightJoin`, `GroupBy`+`Having`, `HavingAggregate`,
  `Distinct`, `Select`, `OrderBy`, `Limit`/`Offset`, `Apply(scopes…)`,
  `Sum`/`Avg`/`Min`/`Max`, `Count`, `Find`, `First`, `List`, `Iter`,
  `Cursor`, `Paginate`.
- Expr AST: `Col` / `Lit` / `Func` / `And`/`Or`/`Not` / `Cast` / `In(subq)`
  / `Exists(subq)`.
- CTEs: `With`, `WithRecursive` (jerarquía de categorías).
- Window: `OVER (PARTITION BY tenant_id ORDER BY created_at)`,
  `RowNumber`/`Rank`/`Lag` sobre `orders`.
- Set ops: `Union` / `Intersect` / `Except` entre subqueries tipadas.
- Locking: `ForUpdate` / `ForShare` / `SkipLocked` / `NoWait`.
- Typed columns (codegen): `WhereP(OrderColumns.Status.In(...))`.
- Preload anidado: `User.Orders.Lines.Product.Category` (5 niveles).
- Batches: `CreateBatch` / `UpsertBatch` / `UpdateBatch` / `DeleteBatch`
  con chunking IN (Oracle 1000, MSSQL 2100).
- Soft delete: `WithTrashed` / `OnlyTrashed` / `Restore`.
- Optimistic locking: collision real con `quark:"version"`.

**Verifica:** cada método produce SQL válido en cada motor y devuelve el
resultado esperado.

**Criterio done:** coverage report `bugbash/coverage.json` lista ≥95% de
los métodos exportados ejercitados. Los métodos no ejercitados se reportan
con razón documentada.

### F3 — Relaciones

**Qué prueba:** las 5 relaciones que Quark soporta, en combinaciones
realistas.

**Ejercita:**
- `belongs_to`: `Order` → `Customer` → `Organization`.
- `has_many`: `Order.Lines`, `Category.Children` (autoreferencial).
- `has_one`: `User.Profile`.
- `many_to_many`: `User.Roles` con join table `user_roles`.
- `polymorphic`: `AuditEvent.Subject` (puede apuntar a `Order` /
  `Invoice` / `User`).

**Verifica:**
- Preload con cláusulas `Where` en la relación.
- Preload anidado con dotted paths (`Orders.Lines.Product.Category.Parent`).
- Persistencia recursiva: `client.Create(&order)` con `order.Lines`
  poblado en memoria.
- Eager loading IN chunking por motor (Oracle 1000).
- Tenant propagation a través de loads.

**Criterio done:** cero N+1 (verificado vía `quark.queries.total` métrica
OTel — debe coincidir con N preloads + 1, no con N * filas).

### F4 — Volumen

**Qué prueba:** que la librería se comporta bajo dataset realista de
empresa mediana.

**Ejercita:**
- Sembrar 1M `orders`, 5M `order_lines`, 100k `users`, 50k `products`,
  10k `customers` distribuidos por 100 `organizations` (tenants).
- `List` con `Limit(50)` + `Offset(N)` en N=100k.
- `Cursor` para iteración completa sin OOM.
- `Iter` con cancelación.
- `Paginate` con count exacto.
- `CreateBatch(10000)` con chunking automático.

**Verifica:**
- Memoria: el proceso no debe crecer más allá de **2× el peak** entre
  fase y fase (medido con `runtime.MemStats`).
- Latencias: p50 < 50ms, p99 < 500ms para queries indexadas sobre 1M
  filas en cualquier motor.
- Allocs: regresión vs baseline (`benchmarks/PROFILING.md`).

**Criterio done:** memoria y latencia dentro de presupuesto. Si no, el
report flagea el motor + operación.

### F5 — Multi-tenancy

**Qué prueba:** las 4 estrategias en concurrencia con tráfico realista.

**Ejercita:**
- `DatabasePerTenant` con 50 tenants (50 SQLite files, 10 PG schemas en
  database-per-schema, etc.).
- `SchemaPerTenant` con 50 schemas en PG.
- `RowLevelSecurityClient` con 50 tenants en una tabla compartida
  (`tenant_id`); 50 goroutines concurrentes haciendo CRUD.
- `RowLevelSecurityNative` (PG only): mismo escenario con
  `set_config('app.tenant_id', ...)` + policy. **El test crítico**:
  inyectar tenant_id en context, ejecutar `client.Raw()` (que debería
  fallar por policy) y verificar que el motor lo bloquea.
- `Or()` bajo RLS Client: el bug P0-1 ya cerrado — regresión específica.

**Verifica:**
- Aislamiento perfecto entre tenants (tenant A no ve filas de tenant B
  ni siquiera bajo concurrencia + `Or()` complejo).
- Switch entre estrategias (mismo dominio, distinta config).
- `Tx` bajo RLS Native respeta `set_config` durante toda la tx.

**Criterio done:** cero fugas entre tenants en 10k operaciones
concurrentes por estrategia.

### F6 — Migraciones (schema-as-code)

**Qué prueba:** que el ciclo de schema evolution funciona en producción.

**Ejercita:**
- `PlanMigration(model)` sobre el dominio base → empty plan.
- Cambiar el dominio (añadir col, cambiar tipo, drop FK, añadir índice)
  → `PlanMigration` detecta el diff exacto.
- `ApplyPlan` transaccional (PG/MSSQL/SQLite) y resumable (MySQL/MariaDB/
  Oracle) con caída simulada a mitad — verificar que `quark_migration_state`
  permite resumir.
- `Backfill` orquestado: añadir `legacy_id` con default null,
  `Backfill(spec)` que rellena por PK, verificar resume tokens.
- Migración versionada con `migrate.Up/Down` sobre el registry global.
- `AcquireMigrationLock` con 2 procesos concurrentes (dos goroutines
  simulan dos pods) — verificar serialización.

**Verifica:**
- Round-trip identity en los 6 motores.
- Resume tokens funcionan tras kill -9 simulado.
- Lock distribuido serializa estrictamente.

**Criterio done:** cero diffs falsos positivos. Migración interrumpida
se completa al re-ejecutar.

### F7 — Caché

**Qué prueba:** stampede protection + per-row invalidation bajo carga.

**Ejercita:**
- 1000 goroutines piden la misma key cacheada simultáneamente — verificar
  que la DB recibe **1 sola** query.
- 100 mutations por segundo invalidando por PK — verificar que la cache
  hit ratio se mantiene.
- TTL jitter visible: 10000 entradas con misma TTL nominal → distribución
  real ±10%.
- XFetch refresh probabilístico: entrada cerca de TTL → algunos lectores
  triggerean refresh, otros sirven cache fresca.
- Redis backend: tag-set TTL con NX+GT (max).
- Negative caching: `First` que devuelve no-rows — segundo `First` no
  toca DB.

**Verifica:**
- Singleflight in-process: 1000 → 1.
- Cross-instance: 3 procesos paralelos, 1000 cada uno → **3** queries
  (el gap documentado). Esto **NO** es bug; el report lo marca como
  "comportamiento documentado, verificado".

**Criterio done:** singleflight efectivo, invalidación granular, TTL
jitter medible.

### F8 — Hooks / Eventos / Audit

**Qué prueba:** semántica transaccional bajo combinaciones complejas.

**Ejercita:**
- `Client.Tx` con savepoints anidados de 5 niveles. RollbackTo en el
  nivel 3 → hooks de 4 y 5 descartados, hooks de 1, 2, 3 permanecen.
- `OnCommit` + `OnRollback` con error en el callback (no debe romper
  el commit, sí debe loguear).
- `EventBus.Publish` con bus que devuelve error → el dato persiste, el
  bus loguea.
- `EnableAuditLog` con 100k writes — verificar que `quark_audit` tiene
  exactamente 100k filas con `diff` válido por operación.
- `BeforeFind` que muta el query (`q.Where(...)`) → reflejado en SQL.
- `AfterFind` con error en una fila — el batch entero falla.

**Verifica:**
- Atomicidad audit + write (kill -9 a mitad de tx — ni audit ni write
  persisten).
- Orden FIFO de hooks `After*` post-commit.
- `TxFromContext` accesible desde hooks de lifecycle.

**Criterio done:** cero side-effects de tx revertidas. Audit log
exactamente coherente con writes.

### F9 — Codegen

**Qué prueba:** que `quark gen` mantiene paridad con reflect path.

**Ejercita:**
- `quark gen` sobre `bugbash/domain/` (15-20 modelos).
- Para cada modelo, comparación reflect-path vs generated-path en F1+F2.
- Round-trip idéntico de cada tipo con codegen activo.
- Cambiar un modelo (añadir campo) sin regenerar — el gate `//quark:gen
  vN` debe degradar a reflect sin error silencioso.
- `WhereP(Columns.Foo.Eq(...))` vs `Where("foo", "=", ...)` produce SQL
  byte-idéntico.
- `quark gen --dry-run` no escribe archivos.

**Verifica:**
- Cero drift entre AST hash y reflect hash.
- Modelo con PK no-entero (UUID, string) → binder cae a reflect
  conscientemente.

**Criterio done:** todos los tests de F1+F2 pasan con y sin codegen.

### F10 — Sharding

**Qué prueba:** routing per shard key bajo carga.

**Ejercita:**
- 4 shards (4 SQLite files o 4 PG schemas) con `HashShardFunc`.
- 100k operaciones distribuidas, cada una con `WithShardKey(customer.ID)`
  en ctx.
- Verificar distribución estadística (chi-square test → distribución
  uniforme).
- Operación sin shard key → error (cross-shard fan-out no implícito,
  ADR-0016).
- Cross-shard transaction → error claro.

**Verifica:**
- Cero cross-shard leaks (filas de un shard no aparecen en queries de
  otro).
- Resharding simulado: añadir 5° shard, redistribuir keys con función
  nueva — verificar que la API no rompe.

**Criterio done:** distribución uniforme + cero leaks + errores claros
en los casos no soportados.

### F11 — Replicas

**Qué prueba:** read-write split + Sticky + failover.

**Ejercita:**
- 1 primary + 3 replicas (4 PG instances).
- 1000 read ops + 100 write ops mezcladas — verificar que reads van round-
  robin a replicas, writes siempre a primary.
- `Sticky(ctx)` tras write — verificar que el read posterior va a primary.
- `Tx` — todo va a primary durante la tx.
- Tirar una replica (docker stop) — verificar cooldown + failover a las
  otras dos.
- Tirar el primary — verificar que writes fallan (no hay failover
  primary→replica por diseño).

**Verifica:**
- Métricas OTel reflejan el routing real (etiqueta `db.host` por op).
- Cooldown respetado (no martillea una replica caída).

**Criterio done:** routing correcto, failover funcional, métricas
coherentes.

### F12 — Resiliencia & concurrencia

**Qué prueba:** comportamiento bajo carga adversa.

**Ejercita:**
- Deadlocks reales: 2 tx con locks en orden inverso → `WithDeadlockRetry(3)`
  debe recuperar.
- Connection pool exhaustion: `MaxOpenConns(5)` + 50 goroutines — verificar
  que esperan, no crashean.
- `ctx.Cancel` en mitad de query — la conexión vuelve al pool.
- `panic` en `BeforeUpdate` — rollback, conexión liberada, audit no
  escrito.
- 1000 goroutines abriendo `Tx` con savepoint, 10% pánica aleatoriamente
  — verificar no hay leak de transacciones zombie.
- Reconexión tras drop de red (docker network disconnect).

**Verifica:**
- `Client.Stats()` (pool stats) coherente.
- No goroutine leaks (`runtime.NumGoroutine` estable).
- No connection leaks (`Stats().InUse == 0` al final).

**Criterio done:** stress de 30 min sin leaks ni panics no atrapados.

### F13 — Negative tests (security)

**Qué prueba:** que SQLGuard bloquea lo que debe bloquear.

**Ejercita:**
- `Where(maliciousInput, "=", x)` con identifiers maliciosos.
- `Where("col", maliciousOp, x)` con operadores no whitelisted.
- `WhereJSON("col", "$.evil'; DROP--", v)` — bloqueado por
  `ValidateJSONPath`.
- `Join(t, raw)` con `raw` malicioso — bloqueado por `ValidateJoinOn`.
- `RawQuery` sin `AllowRawQueries` — bloqueado.
- `RawQuery` con regex anti-injection del guard (UNION SELECT, OR 1=1,
  comentarios `--`/`/**/`).
- Tenant ID inyectado: `^[a-z0-9_-]+$` debe rechazar `'; DROP--`.

**Verifica:**
- Cada bloqueo devuelve `Err*` tipado.
- Cero queries fugadas al motor con payload malicioso.

**Criterio done:** 100% de payloads conocidos bloqueados. Si alguno
pasa, **es bug P0** y va al top de TASKS.md inmediatamente.

### F14 — Soak / long-run

**Qué prueba:** que 12h continuas en los 6 motores no degradan.

**Ejercita:**
- Workload mixto (60% read, 30% write, 10% complex con JOINs) durante
  12h en cada motor.
- Cache + replicas + sharding activos.
- Métricas snapshot cada 5 min.

**Verifica:**
- Latencias estables (no creciente).
- Memoria estable.
- Cero panics no esperados.
- Métricas OTel sin gaps.

**Criterio done:** 12h x 6 motores = 72h-engine soak completado sin
incidencias no documentadas.

## Estructura física

```
bugbash/
├── README.md                 # Cómo correr (lo lee Code al arrancar /bugbash)
├── DOMAIN.md                 # Las 15-20 tablas del ERP-SaaS
├── go.mod                    # Módulo propio con replace ../
├── domain/                   # Los structs Go del dominio
│   ├── organization.go
│   ├── user.go
│   ├── product.go
│   └── ... (15-20 archivos)
├── seed/                     # Generadores de datos con seed determinista
│   ├── seed.go
│   ├── distributions.go
│   └── faker.go
├── phases/                   # Una carpeta por fase
│   ├── f00_install/
│   ├── f01_smoke/
│   ├── f02_api_surface/
│   ├── ...
│   └── f14_soak/
├── REPORTS/                  # Reports versionados por timestamp
│   └── run-2026-MM-DD-HHMM/
│       ├── summary.md
│       ├── failures.json
│       └── per-engine/
└── tools/                    # Helpers (container boot, metrics scraper)
    ├── docker_up.go
    └── coverage_check.go
```

## Cómo se ejecuta

El slash command [`/bugbash`](../.claude/commands/bugbash.md) es la entrada
recomendada. Sintaxis:

```
/bugbash [fase | all] [--engines=<list>] [--seed=<n>] [--report-only]
```

Equivalente bash directo (lo que el slash command tira):

```bash
cd bugbash
go test -tags=bugbash -timeout 60m \
  -engines=$ENGINES -seed=$SEED \
  ./phases/f${N}-${NAME}/...
```

Una pasada **completa** (F0-F13, sin F14 soak) cabe en 2-3 horas en una
máquina decente con los contenedores ya cacheados. **F14 soak** es opt-in
y se corre overnight. La estimación por fase:

| Fase | Tiempo aprox. (6 motores en paralelo) |
| --- | --- |
| F0 install/boot | 3-5 min (la mayoría es boot de contenedores) |
| F1 smoke | 5 min |
| F2 API surface | 15-20 min |
| F3 relaciones | 10 min |
| F4 volumen | 30-45 min (1M registros seed + queries) |
| F5 multi-tenancy | 15 min |
| F6 migraciones | 10 min |
| F7 caché | 10 min |
| F8 hooks/eventos/audit | 10 min |
| F9 codegen | 5 min |
| F10 sharding | 10 min |
| F11 replicas | 15 min |
| F12 resiliencia | 30 min |
| F13 negative | 5 min |
| F14 soak | 12h × 6 motores |

## Cómo se reportan fallos

Cada fase produce un `bugbash/REPORTS/run-<ts>/<fase>/<engine>.json` con
shape:

```json
{
  "phase": "f02_api_surface",
  "engine": "oracle",
  "timestamp": "2026-05-28T15:00:00Z",
  "duration_ms": 942301,
  "tests_total": 187,
  "tests_passed": 184,
  "tests_failed": 3,
  "failures": [
    {
      "test": "TestCTERecursiveWithWindow",
      "severity": "P1",
      "category": "dialect-specific",
      "engine_only": ["oracle"],
      "error": "ORA-32104: cannot reference …",
      "reproducer": {
        "commit": "70c9b25f",
        "command": "go test -tags=bugbash -run TestCTERecursiveWithWindow ./phases/f02_api_surface/...",
        "seed": 42,
        "dataset_sql": "REPORTS/run-…/oracle/dataset.sql"
      },
      "stack": "…",
      "suspected_files": ["window.go:142", "cte.go:88"]
    }
  ]
}
```

El **subagente `bugbash-reporter`** lee el directorio `REPORTS/run-<ts>/`,
clasifica los fallos por categoría y severidad, y emite:

1. **Resumen humano** en `REPORTS/run-<ts>/summary.md` con tablas y
   estadísticas.
2. **Tareas en `TASKS.md`** bajo § "Bug-bash hallazgos" — una entrada
   por fallo con su severidad y motor.
3. (Opcional, si `gh` está disponible) **Issues en GitHub** con label
   `bug-bash` y la severidad.

### Clasificación de fallos

| Categoría | Significado | Acción |
| --- | --- | --- |
| `regression` | Pasa SQLite pero falla otro motor que antes pasaba | P0 — bloquea v1.0.x si afecta motor en CI |
| `dialect-specific` | SQL inválido en un motor concreto | P1-P2 según afectación |
| `gap` | Comportamiento no implementado pero la doc lo promete | P1 — bug doc o feature |
| `doc-drift` | El test esperaba lo que la doc dice; el código hace otra cosa | P2 — alinear (escala docs-auditor) |
| `test-only` | El test es incorrecto (asume case sensitivity etc.) | P3 — arreglar test |
| `flaky` | No reproducible | **bug del bug-bash**, no del producto |

### Severidad

- **P0** — security (F13), corrupción silente, fuga de tenants, deadlock
  no recuperado. **Bloqueante**.
- **P1** — feature roto en ≥1 motor con CI bloqueante; degradación de
  perf >2× vs baseline; gap entre doc y código que afecta uso típico.
- **P2** — feature roto en motor secundario o caso edge; cosmético en
  doc.
- **P3** — mejora deseable, test-only fixes.

## Integración con el flujo existente

- **Subagente `code-reviewer`**: los PRs que arreglan fallos del bug-bash
  pasan por el reviewer normal. El reviewer comprueba que el PR linkea al
  fallo (`Fixes BUG-BASH-<run>-<fase>-<test>`) y que el test correspondiente
  pasa en la próxima pasada.
- **Subagente `docs-auditor`**: si un fallo es `doc-drift`, el reviewer
  delega al docs-auditor para confirmar que la corrección alinea código y
  doc.
- **`TASKS.md`**: § "Bug-bash hallazgos" lista los activos. Cuando se
  cierran, se tachan con `~~BUG-BASH-...~~` siguiendo el patrón de los P0
  históricos.
- **`/release`**: una v1.0.x patch release no se taggea con BUG-BASH P0
  abiertos.

## Política de cadencia

- **Primera pasada completa**: en cuanto el plan esté implementado.
  Bloquea nada — el resultado define qué es v1.0.1 / v1.1.
- **Cada release minor (v1.x.0)**: pasada completa antes de taggear.
- **Cada release patch (v1.0.x)**: F0+F1+F13 obligatorios. Resto si el
  patch toca módulos no triviales.
- **Semanal automático**: F1+F2 nocturno en CI scheduled (`bug-bash-nightly`
  workflow, opt-in).

## Lo que el plan NO cubre

Por claridad — fuera de scope deliberadamente:

- **Fuzzing** (`go-fuzz` o `Go 1.18 fuzzing`): cubierto parcialmente por
  F13, pero no exhaustivo. Si el bug-bash levanta P0 en F13, considerar
  fuzzing dedicado en v1.x.
- **Property-based testing** (rapid/gopter): no en v1.0. Útil para Phase
  6 cuando el AST esté más cubierto.
- **Tests de regresión de performance vs versiones anteriores**: el plan
  asume comparación contra el baseline actual (`benchmarks/PROFILING.md`),
  no contra historic tags.
- **Pen-testing real**: F13 cubre los vectores conocidos; un pen-test
  real es trabajo de un especialista externo.
