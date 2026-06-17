# Handoff a Claude Code — superapp de aceptación cross-engine

> Para la sesión de Code que continúe este trabajo. **Lee primero**:
> `examples/superapp/README.md` (blueprint), `TASKS.md` § "Superapp", y las
> firmas/tags que se citan abajo. Arranca con `/next-session auto` — este trabajo
> es el foco propuesto (no es P0; no gatea por la regla 4 de `CLAUDE.md`).

## Objetivo

Arnés **headless** en `examples/superapp/` que ejerce TODA la superficie pública
de Quark contra los 6 motores y **demuestra** la cobertura reconciliándola
contra un manifiesto generado del código. Es la versión permanente del bug-bash
F1–F14 conducida por una capa servicio→Quark y gateada por manifiesto.
Complementa, no sustituye, la suite del repo.

## Premisas (no negociables)

1. **Cobertura demostrada, no afirmada.** Cada símbolo del manifiesto queda
   invocado en cada motor o justificado en `allowlist.json`. El gate estricto
   falla si no se cumple.
2. **Headless**, dentro de `examples/superapp/`, sin framework web y **sin deps
   nuevas** si se puede (stdlib: `runtime`, `database/sql` DBStats).
3. **6 motores.** PG/MySQL/MariaDB/MSSQL por testcontainers (ya en `go.mod`);
   **Oracle por `docker run gvenzl/oracle-free:23-slim`** (NO testcontainers —
   crashea), con `GRANT EXECUTE ON DBMS_LOCK` y pool corto (ORA-12516). Replica
   `.github/workflows/ci.yml:138-172`.
4. **Capacidad desigual ≠ fallo.** `RowLevelSecurityNative` y `LISTEN/NOTIFY`
   son PG-only; el lock de migración no está en SQLite/Oracle. Espera
   `quark.ErrUnsupportedFeature` ahí (matriz en `control/capability.go`).
5. **Reglas del repo (`CLAUDE.md`).** Conventional Commits sin mezclar tipos;
   `code-reviewer` + `docs-auditor` antes de PR; **sin lenguaje de marketing**;
   API+docs en el mismo PR; los 6 motores verdes antes de merge a `main`; nada
   de `t.Skip` por env var (build tags / testcontainers). Di **archivo:línea**
   antes de tocar.
6. **Slices compilables.** Cada paso termina compilando y corriendo al menos en
   SQLite. El slice 1 se escribió **sin toolchain Go** en el entorno de origen:
   el primer `go build ./examples/superapp/...` (Go 1.25.7) es tuyo — corrige
   firmas si algo no cuadra.

## Hecho

- **S1** — `README.md` (blueprint), `control/{capability,report,manifest}.go`
  (solo stdlib, compila aislado), `domain/models.go` (tags verificados vs
  `website/docs/guides/modeling.mdx`). Compila con Go 1.25.7.
- **S2 · `recorder/`** — `recorder.Recorder` engancha por las DOS vías de Quark:
  `quark.Middleware` (tiene `context` → símbolo autoritativo por SQL, duración,
  filas exactas en exec/query_row) y `quark.QueryObserver` (sin `context`, pero
  da el conteo de filas exacto del SELECT multi-fila que el middleware no puede
  contar sin consumir `*sql.Rows`). Cobertura por `Mark`/`Note` → `control.Invoked`
  vía `Collect`/`ContributeTo`; captura SQL vía `Statements`; `Count`/`Reset` para
  las aserciones de conteo. Asserts de compilación garantizan conformidad con la
  API. e2e contra SQLite real verde (`recorder_test.go`).
  - **Aprendido (vale para S5):** en SQLite `Create` es `INSERT … RETURNING`
    (vía `query_row`, NO `exec`) y `First` es `SELECT … LIMIT 1` (vía `query`, NO
    `query_row`). El `Op` del `Statement` es la VÍA de ejecución, no el verbo SQL;
    los exercisers no deben asumir el verbo por el método. `Delete`/`Update` sí
    van por `exec`. Otros dialectos divergirán — el `Op` por motor es justo lo que
    los golden snapshots deben capturar.
  - **Pendiente para S5 (anotado por `code-reviewer`):** el Recorder es
    mutex-safe pero su test es secuencial. Cuando `exercise/ha.go` corra goroutines
    concurrentes contra un mismo Recorder, añade un test `-race` con N goroutines y
    verifica coherencia de `Count()`/`Statements()` al final.

- **Verificación de infra (observabilidad + caché) — `recorder/infra_test.go`,
  build tag `superapp_infra`.** Prueba Docker-backed que monta sobre un mismo
  Client, A LA VEZ: el recorder (S2) + el `otel.Middleware` de Quark (spans →
  Jaeger real vía OTLP/HTTP) + `WithLogger`+`WithSlowQueryThreshold(1ns)` (Quark
  narra CADA query, SQL parametrizado **sin** valores de bind) + `WithCacheStore`
  con `cache/redis` real. Verde contra `redis:7-alpine` + `jaegertracing/all-in-one`.
  Asserts demostrados: **cache hit = 0 SQL** (2ª `List` idéntica no incrementa
  `recorder.Count()`), **redacción** (el valor secreto del bind nunca aparece en
  el log), y **export OTel** (4 spans `quark.query`/`quark.query_row` en Jaeger,
  conteo idéntico a `recorder.Telemetry()`). Correr:
  `go test -tags=superapp_infra -run TestObservabilityAndCacheInfra ./examples/superapp/recorder/`.
  - **Aclaración de diseño (la pregunta del logger):** el Recorder NO usa el
    logger de Quark, y es correcto que no lo haga. El logger/OTel/Redis son
    superficie pública **bajo test** que el arnés EJERCE y ASERTA (mecanismos #4
    caché y #8 observabilidad del README), no la captura del propio arnés: el
    recorder es la vía máquina-legible (observer+middleware → cobertura + SQL)
    para el gate, estrictamente más rica que el slog para ese fin. S5
    `observability.go`/`cache.go` heredan este test como base; la pila real (2
    middleware + observer + logger + redis) ya está probada compatible.

- **Workload de alto volumen + informe ejecutivo — `workload/` + `cmd/workload/`.**
  `go run ./examples/superapp/cmd/workload [-scale -driver -dsn -out -slow-ms]`
  siembra datos relacionados a volumen, ejerce queries/tx/cache, y el recorder
  mide cada statement → `REPORTS/workload-<stamp>/{executive-report.md,metrics.json,quark.log}`.
  SQLite ×10 = 310k filas / 0 errores / 8.1s / cache 100%. `REPORTS/` está
  gitignored. Reusa `domain` + `recorder` + `cache/memory`. Cuando S4 (engine
  runner) exista, este workload puede correr cross-engine reusando los DSN de la
  matriz (ya acepta `-driver`/`-dsn`). Pendiente opcional: OTel real (hoy usa
  slog + recorder; el OTLP→Jaeger ya está probado en `recorder/infra_test.go`).

## Orden de trabajo

> Con S2 listo, `control.Invoked` ya tiene quién lo alimente (el recorder). El
> siguiente paso es el DENOMINADOR (el manifiesto) — **S3**.

**S3 · `cmd/gen-apisurface/` — HECHO.** `go/packages`+`go/types` sobre `quark` y los
6 subpaquetes públicos → `apisurface.json` (**655 símbolos**, determinista sin
timestamp, vía `go:generate go run . -out=../../apisurface.json`). `allowlist.json`
con `Symbol.Key→razón` (alias deprecado `RowLevelSecurity`). Cadena del gate
verificada e2e (`LoadManifest`+`LoadAllowlist`+`Reconcile` → 654 MISSING − 1).
- **Aprendido (para S5/S6):** los diferidos v1.2 (F6-3b binder, scatter-gather,
  stampede x-instancia) **no son símbolos exportados** → no van en allowlist; la
  allowlist es para símbolos que existen pero no se ejercen. El grueso del
  denominador: `Query[T]` (65 métodos), `Client` (26), y los 6 dialectos (~21-26
  c/u, ~135 métodos) — decidir en S5 si los métodos de dialecto se ejercen
  transitivamente (vía cada query) o se allowlistean en bloque.

**S4 · `engine/` — HECHO.** `Up`/`Down`/`waitReady` + `Run()` con anti-fugas.
Decisión clave: **docker-run, NO testcontainers** (el comentario de
`bugbash/tools/docker.go` lo justifica: el reaper de testcontainers tumba Oracle
en runners; ADR-0018) — el HANDOFF original decía testcontainers para 4 motores,
pero la experiencia probada del repo es docker-run para todos. Contenedores
`superapp-*` en puertos propios (5435/3310/3311/1435/1523); override
`SUPERAPP_DSN_<ENGINE>`. `leak.go` abre client por motor → corre fn → `Close` →
verifica `pool InUse/Open==0` + goroutines estables. Verde en SQLite in-process
(suite normal) y **Postgres docker-run real** (tag `superapp_engine`).
- **Hallazgo (flageado `task_cb2e7d92`):** el dominio no migraba en PG —
  `Account.Active bool default:"1"` → el migrator emite `DEFAULT 1` verbatim y PG
  rechaza un bool con default int. No hay literal de bool portable a los 6.
  Workaround: el dominio quitó el DEFAULT de los bools (Active/Done); el caller
  fija el valor. El fix real del migrator (normalizar bool defaults por dialecto)
  es la tarea spawn.
- **Para S5:** `engine.Run(conns, tol, newClient, fn)` es el harness por-motor que
  los exercisers reusan; `newClient` instala recorder+cache+logger; cada
  `exercise/*.go` es un `fn`. La paridad cross-engine compara resultados de `fn`
  entre los `conns`. Empieza ejerciendo SQLite+PG (los que ya validan), añade el
  resto cuando levantes sus contenedores.
  - **Tolerancia por-motor (anotado por `code-reviewer`):** `tol` es hoy un único
    int para todos. Cuando S5 corra los 6 a la vez, un `tol` alto (p.ej. 4 para
    pgx) esconde fugas de 1-3 goroutines en SQLite (sin driver). Cambiar a
    `map[control.Engine]int` con fallback antes de correr la matriz completa.
  - El check de fugas ya estabiliza (`Settle()`) ANTES de leer el pool, así que es
    fiable aunque `fn` devuelva error con conexiones en cierre asíncrono.

**S5 · `exercise/` — EN CURSO (part 1 hecho).** El patrón canónico está montado:
- `suite.go` — `Run(conns, tol, exercisers)`: instala un recorder por motor,
  migra el dominio, corre cada `Exerciser`, y pliega la cobertura a
  `control.Invoked` (vía `recorder.Collect`). Reusa `engine.Run` (lifecycle +
  anti-fugas). Helpers de key `QM`/`CM`/`QF` que casan EXACTO con `apisurface.json`
  (`QM("Create")` → `…quark.(*Query[T]).Create`).
- `crud.go`, `tx.go`, `builder.go`, `relations.go`, `security.go`, `cache.go`, `tenant.go`, `tenant_rls_native.go`, `tenant_schema_per.go`, `tenant_db_per.go` (+`tenant_dsn.go` rewriters), `migrate.go` entregados — verdes en SQLite **y PG real**
  (`-tags=superapp_engine`, **79 símbolos / 99 statements**). **Tenant: 4/4 estrategias cubiertas.** `tenant.go` cubre **la modalidad RowLevelSecurityClient**
  (aislamiento cross-tenant no-leak + propagación a Or-groups [regresión P0-1] + el aislamiento es del
  router [client base ve todo, como `Raw()`/`Exec()`] + rechazo de tenant_id inválido/ausente); builder-only →
  portable 6 motores; añadió el helper de key `TRM` (métodos de `*TenantRouter`). El `cache` exerciser **destapó BB-15** (un `Create`
  no invalidaba el table tag en los motores RETURNING/OUTPUT → caché L2 stale; fix #175). El suite
  instala `WithCacheStore(memory.New())` por motor y **cierra la goroutine `cleanupLoop` en `fn`
  antes del leak-check** (`Client.Close()` no cierra el store; `WithOptions` descarta el recorder).
  **Para añadir un exerciser:** copia la
  forma de `crud.go` — un `Exerciser{Name, Fn}` que `rec.Mark(ctx, QM("X"))` antes
  de cada llamada terminal (atribuye el SQL al símbolo) y `rec.Note(QM("Y"))` para
  builders/funcs sin SQL propio, con asserts funcionales que devuelven error.
- **Gotchas de portabilidad (los cazó el run en PG; valen para todos los
  exercisers):** (1) `GroupBy(col)` **exige** `Select(col)` — sin él, `List()`
  emite `SELECT * … GROUP BY`, que SQLite tolera pero PG/SQL-estándar rechaza.
  (2) Compara columnas `bool` con un **bool**, nunca con `0`/`1` — pgx es estricto
  y no encodea int→bool (SQLite sí lo tolera). En general: escribe SQL portable y
  pasa los tipos exactos; el motor laxo (SQLite) esconde lo que el estricto (PG)
  rechaza. No son bugs de Quark — son del query mal escrito.
- **Tenant — las 4 estrategias HECHAS** (full scope pedido por el usuario).
  Decisiones y gotchas por estrategia:
  - **RLSNative** — ✅ **HECHO** en `tenant_rls_native.go` (var `RLSNATIVE`, PR #179).
    Decisión de firma: se pasó `engine.Conn` al exerciser (alias `Conn` en `suite.go`;
    los 6 exercisers previos lo ignoran con `_ Conn`) — más limpio que derivar roles por
    `Raw()`. En PG: admin client (`AllowRawQueries`) crea rol no-superuser + `CREATE
    POLICY` + `FORCE ROW LEVEL SECURITY`, el sujeto es un client no-superuser, y el
    aislamiento forzado por el motor se aserta vía `router.Tx`; en no-PG: rechazo con
    `ErrUnsupportedFeature` (mirror `rls_native_test.go`). **Gotcha (vale para `ha.go` y
    cualquier exerciser que abra tx con ctx propio):** NO uses el path implicit-tx de
    `For[T]` bajo Native con un ctx no-cancelable — `nativeRLSExecutor` deja la tx abierta
    y el commit depende de `context.AfterFunc(ctx, …)`, que nunca dispara con un ctx
    Background → conexión retenida + goroutine `awaitDone` parada → cuelga el leak-check
    (timeout). Usa `router.Tx` (commit síncrono, camino recomendado por `rls_native.go`)
    con ctx cancelable + `defer cancel`.
  - **SchemaPerTenant** — ✅ **HECHO** en `tenant_schema_per.go` (var `SCHEMAPERTENANT`).
    Admin `CREATE SCHEMA` ×2 + onboarding caller-side (el playbook: no se auto-crea):
    un client efímero con `search_path=<schema>` en el DSN (pgx pasa los query-params
    desconocidos como runtime params) migra la tabla DENTRO de cada schema. El DML va
    por el BaseClient del harness (instrumentado) → la **regresión BB-8 se aserta sobre
    el SQL emitido** (`rec.Statements()`: el INSERT debe mencionar el schema). Capability
    nueva `FeatSchemaPerTenant` {PG,MSSQL} — OJO: **no gateada por Quark** con
    `ErrUnsupportedFeature` (el exerciser SALTA donde no hay schemas, no aserta error;
    capability.go documenta las dos semánticas). **MSSQL es TODO ruidoso**: soporta
    schemas pero no hay `search_path` por DSN — al habilitar MSSQL en la matriz, el
    exerciser falla con el error TODO hasta implementar su migrate-into-schema (DDL
    cualificado vía admin, o default_schema por usuario). No es skip: no infla cobertura.
  - **DBPerTenant** — ✅ **HECHO** en `tenant_db_per.go` (var `DBPERTENANT`) +
    `tenant_dsn.go` (rewriters de DSN **puros**, unit test en `tenant_dsn_test.go` sin
    motor). Factory instrumentado con `rec.Options()` y **tracking de clients abiertos**
    (el router NO tiene `Close()`; el exerciser cierra todo antes del leak-check; el
    doble-Close con la evicción del LRU es inocuo). `MaxCachedPools=1` prueba el contrato
    LRU determinista: 2 tenants alternados → factory ×4 (sin evicción serían 2),
    `ActiveTenants()` == el pool vivo, y los datos persisten tras evicción→re-open
    (aislamiento físico). Aprovisionamiento: SQLite ficheros derivados del DSN base;
    PG `CREATE DATABASE` vía `admin.Exec` (va directo a `db.ExecContext`, sin tx — PG lo
    exige); MySQL/MariaDB/MSSQL rewriters listos sin ejercitar (la matriz aún no los
    bootea); Oracle skip documentado (`FeatDBPerTenantProvision`: un PDB queda fuera del
    alcance del harness).
  - **`migrate.go` — ✅ HECHO** (var `MIGRATE`; verde SQLite + PG real, 79
    símbolos / 99 statements). Cubre: round-trip `Migrate`→`PlanMigration`
    **módulo drift conocido**, diff de tabla faltante + `Plan.Hash`,
    `ApplyPlan` (add/drop column + drop table), `mergeNonColumnSurface`
    (índice manual sin drops), registry per-Client, `Sync`
    (dry-run/add/uso-end-to-end/drop), `Backfill` (resume tras fallo
    inyectado), lock por capability (contención→`ErrLockTimeout`;
    `ErrUnsupportedFeature` en SQLite — y OJO: `capability.go` ganó
    `Oracle: true` en `FeatMigrationLock`, estaba stale vs ADR-0018), y el
    ciclo versionado completo sobre un client dedicado `AllowRawQueries:true`
    (requisito documentado en `migrations.mdx` § "Raw SQL Requirement" — el
    exerciser es su regresión e2e). **Destapó 2 findings de core** (TASKS §
    findings, tasks `task_20d5f912`/`task_b03f2155`): (A) ~~`ApplyPlan` crea
    tablas SIN PK~~ — **RESUELTO** (F3-2-pk: `Column.PrimaryKey` end-to-end;
    el paso 2 del exerciser volvió al diseño original — crea la tabla vía
    `ApplyPlan` y el INSERT con id autogenerado es el assert);
    (B) ~~`PlanMigration` propone drift falso sobre BD recién migrada~~ —
    **RESUELTO** (join tables m2m sintetizadas en el desired + equivalencias
    de tipo/default por catálogo en el diff; `RoundTrip_RichFixture` lo
    pinnea en la SharedSuite de los 6 motores). **El arnés quedó estricto**:
    `filterKnownDrift` eliminado, asserts a `IsEmpty()` a secas, converge
    aplica el plan crudo. **Gotchas para los siguientes:** el
    exerciser converge al entrar (re-entrante en motores persistentes; deja
    la BD canónica al salir), las columnas añadidas a tablas con filas van
    `Nullable[T]` (el Scan de un NULL en `string` revienta), y las
    mutaciones de un client secundario NO invalidan la caché del client del
    harness — los asserts de conteo van por el client que mutó.
  - **`ha.go` — ✅ HECHO** (vars `REPLICAS`/`SHARDING`/`DEADLOCK`; verde
    SQLite + PG real, 101 símbolos / 161 statements). Réplicas por
    presencia-de-dato (marcadores distintos por base; round-robin/Sticky/
    tx-pin/write-solo-primary/Count ruteado), sharding con shards
    aprovisionados (`provisionHADBs` reusa los rewriters de
    `tenant_dsn.go`; Oracle skip vía `FeatDBPerTenantProvision`), deadlock
    real con barrera F12 en servidores (capability nueva `FeatDeadlock`;
    SQLite ejercita la opción en camino feliz). El test `-race` del
    recorder pedido en S2 vive en `recorder/recorder_race_test.go` (OJO:
    `:memory:` da una BD vacía por conexión del pool — usa fichero; y los
    workers hacen Counts, no writes, para no contender en SQLite).
    Failover/cooldown de réplicas citado a `replicas_postgres_test.go` +
    bug-bash F11 (necesita tumbar instancias). **Gotcha S7/Oracle:** el
    exerciser DEADLOCK abre un client propio con `WithMaxOpenConns(8)`;
    con el techo de sesiones de gvenzl (ORA-12516, vísto en el soak F14),
    bajarlo a ≤4 al encender Oracle en la matriz.
  - **`observability.go` — ✅ HECHO** (var `OBSERVABILITY`; verde SQLite +
    PG real, 115 símbolos / 166 statements). OTel in-memory vía providers
    GLOBALES del SDK (tracetest + ManualReader, restore con defer — el
    middleware resuelve tracer por llamada e instrumentos por sync.Once).
    Redacción asertada por ambos lados (RedactArgs default / IncludeArgs
    opt-in), db.system, codes.Error y quark.queries.total. **Gotchas:** el
    error portable va por List/QUERY (query_row difiere el error al Scan y
    su span no puede marcarse — limitación de database/sql); y una columna
    inexistente NO falla en SQLite (DQS degrada `"col"` a literal string) —
    usa tabla inexistente como trigger.
  - **`builder_advanced.go` — ✅ HECHO** (var `BUILDERADV`; verde SQLite +
    PG real, 168 símbolos / 217 statements — los 65 métodos de Query[T]
    cubiertos). **Gotchas:** Where/Select NO aceptan identificadores
    cualificados (sólo la grammar del ON los acepta; con JOIN quark emite
    las columnas del modelo cualificadas — usa List() plano, patrón
    cte_test.go); Tracked.Save corre BeforeUpdate ANTES del diff, así que
    "sin cambios → sin SQL" no aplica a modelos que mutan UpdatedAt en el
    hook; WhereSubquery está gateado por AllowRawQueries (asertar AMBOS
    lados); UpsertBatch sigue sin chunkear (lotes pequeños); ForShare no
    existe en MSSQL (tolerar ErrUnsupportedFeature); los counts del
    exerciser van SIEMPRE scoped al marcador badv- (el dominio lleva
    residuo de otros exercisers).
  - **`parity.go` — ✅ HECHO (cierra S5)**: el oráculo de paridad.
    `RunParity(conns, tol)` → payload canónico por motor (9 sondas sobre
    dataset determinista de claves naturales) + `CompareParity` → lista de
    divergencias byte-a-byte. Canonicalización: `''`≡NULL (Oracle) → `∅`,
    tiempos UTC truncados al segundo, floats %.6f, nunca IDs autoincrement.
    Paridad SQLite↔PG verificada 9/9 (`TestParityDockerSQLiteVsPostgres`).
    Para sumar motores: añadirlos al slice de engines del test. El
    determinismo run-a-run está pinneado en `parity_test.go` — si una sonda
    nueva no es determinista, el oráculo da falsos positivos: ordena SIEMPRE
    por clave natural y canoniza tiempos/floats. Al encender MySQL en S7,
    verificar el scan de `flag bool` (TINYINT del driver — el struct tipado
    de quark lo coerce, pero es el gotcha bool conocido de builder.go).
  - ~~Luego: builder-avanzado~~ — S5 COMPLETO; ~~siguiente: **S6**~~ — S6 HECHO
    (main.go: Reconcile/Render/Gate); siguiente: **S7** (CI 6-motores).
  (CTE/window/setops/locking — los ~30 métodos de `Query[T]` que el builder común
  no cubre; varios necesitan la matriz de capacidad por motor). Y el **oráculo de
  paridad**: hoy los asserts son por-motor; falta comparar el RESULTADO de cada
  `fn` entre motores (normalizando Oracle `''`→NULL, MSSQL uuid, UTC) para
  detectar divergencias silenciosas.
  - **Patrón cache reusable** (para `tenant`/`observability` que también necesitan
    conteo de statements): diff de `rec.Count()` alrededor de la operación — un hit
    no incrementa; una invalidación-por-mutación sí; un Preload de N hijos suma 1
    (IN), no N. El store por-motor lo provee el suite, no el exerciser.
- **Follow-up trivial:** endurecer el assert de identificador en
  `exercise/security.go` de `strings.Contains(...,"identifier")` a
  `errors.Is(err, quark.ErrInvalidIdentifier)` (ya posible tras el fix #173).

**S6 · `main.go` — ✅ HECHO.** `examples/superapp/main.go` (root, `package main`)
+ `main_test.go`. Flags `-engines` (lista por comas o `all`), `-gate` (`strict`/
`off`), `-out`, `-manifest`, `-allowlist`, `-keep`. Blank-importa los 5 drivers
(engine.Up sólo entrega driver+DSN). Flujo: `parseEngines` → `engine.Up` (defer
`Down` salvo `-keep`) → `exercise.Run(conns, tol, exercise.AllExercisers())` →
`exercise.Coverage` → `LoadManifest`/`LoadAllowlist` → `buildReport` → matriz a
`REPORTS/superapp-<stamp>/matrix.txt` (vía `control.Report.Render`) + `summary.json`
máquina-legible → `Report.Gate`. **`AllExercisers()` es ahora la única fuente de
verdad** de la lista de 16 exercisers (extraída a `suite.go`; los dos tests la
consumen — antes estaba duplicada literal en `exercise_test.go:28` y
`exercise_docker_test.go:32`). **Decisiones de diseño:**
- **Fila de salud sintética** `!! engine-run` (ordena la primera; no es un
  símbolo): PASS si el motor terminó sin error funcional ni fuga, FAIL si no. Hace
  que `Report.Gate` cuente los errores funcionales (que no son celdas de método) →
  un assert rojo o una fuga fallan el gate aunque no sea estricto.
- **Filtrado a motores corridos**: `Manifest.Reconcile` recorre los 6 motores
  internamente; `buildReport` se queda sólo con las celdas de los motores de
  `-engines`, para no marcar como gap un motor no pedido. Con `-engines=all` no
  filtra nada (el caso del gate real, S7).
- **Partición exacta** en `summary.json`/stdout: cada símbolo es covered |
  gating-missing | allowlisted (suman Total); `missing` excluye allowlisted →
  coincide byte a byte con lo que cuenta `Report.Gate`. Las claves invocadas
  fuera del manifiesto (typos de key-helper) se cuentan como `stray` y se avisan
  (su consecuencia —el símbolo real queda MISSING— ya aflora en la matriz).
- **tol**: 2 si sólo SQLite, 4 si hay algún motor con servidor (mismo criterio que
  los tests). El follow-up `map[Engine]int` de S4 sigue pendiente para la matriz
  completa.

Verificado en SQLite: `go run ./examples/superapp -engines=sqlite` → 167/655
cubiertos, gate `off` exit 0; `-gate=strict` exit 1 (lista los gaps de sqlite);
motor desconocido / manifiesto ausente → exit 1; `main_test.go` cubre
`parseEngines`, `buildReport` (partición + filtrado + gate), `perEngine` y la
fila de salud con fuga. **Para S7:** el `summary.json` ya da el veredicto
máquina-legible; el job CI corre `go run ./examples/superapp -engines=all
-gate=strict` con Oracle docker-run (bajar `WithMaxOpenConns` del exerciser
DEADLOCK a ≤4 por ORA-12516, ver `ha.go`).

**S7 · CI** — job que corre la superapp en los 6 (patrón `integration` de
`ci.yml`; Oracle docker-run). Gate estricto bloqueante. **EN CURSO.**
- **S7-fix-harness (2026-06-17) — HECHO.** Los 4 fallos harness-side del 2026-06-15
  cerrados: (1) MySQL zero-time — `domain.Project`/`Membership` ganaron `BeforeCreate`
  (sólo `Account` lo tenía; el zero-time rompía MySQL 8 strict); (2) BD dedicada
  `superapp` en MySQL/MariaDB/MSSQL (`engine.go`: `serverDSN`+`ensureDBSQL` crean la
  base tras el ping — antes el DSN caía en `mysql`/`master` y el migrator borraba
  tablas de sistema); (3) MSSQL migrate-into-schema (`tenant_schema_per.go`:
  `provisionSchema` por-motor — PG vía search_path, MSSQL vía DDL schema-cualificado,
  que es lo que el exerciser ASERTA: la cualificación schema.table del router, no el
  migrator); (4) Oracle conns del exerciser DEADLOCK 8→4 (ORA-12516). Resultado:
  **mariadb 142❌→168✅**; mysql/mssql/oracle avanzaron a builder-advanced.
- **S7-fix-harness destapó 3 bugs de CORE** (TASKS § findings F/G/H) — todos en
  `builder-advanced`, todos `fix:` con regresión 6-motores en la SharedSuite:
  **F** `WithRecursive` emite `WITH RECURSIVE` en Oracle (ORA-02000; `query_exec.go:728`);
  **G** `CreateBatch` no rellena PKs en MySQL/MSSQL (hermano de Finding C, `query_crud.go:1814`);
  **H** `CreateBatch`/`UpdateBatch` no corren hooks (decisión owner: que los corran).
  Van antes de S7-coverage/S7-ci. Tras F/G/H, el harness queda estricto (revierte el
  workaround de timestamps del seed si se hubiera necesitado) y la fila funcional verde
  en los 6 desbloquea el cableado de CI.
- **Reusar Oracle persistente:** `SUPERAPP_DSN_ORACLE=oracle://system:quark@localhost:1521/FREEPDB1`
  (el grant `DBMS_LOCK` se hace a mano como SYS — el path override salta `grantOracleLock`).

**S8 · cierre** — snapshots SQL golden estables, paridad completa, página
pública si el sidebar lo pide (regla 3: docs en el mismo PR).

**S9 · `cli/` — cobertura del binario `cmd/quark` (smoke entregado).** El CLI es
superficie pública (v1.1.0) y el charter dice "ejerce TODA la superficie", pero
NO encaja en el gate de símbolos de S3: `cmd/quark` es `package main` y su
contrato público es la interfaz de COMANDOS cobra, no símbolos Go. Mecanismo
paralelo, a nivel comando:
- **denominador** = árbol de comandos cobra (enumerable de `Use:`).
- **numerador** = comandos ejercidos: build del binario → exec → assert.
- **gate** = exit-code + golden output; allowlist para comandos diferidos.
- **Hecho:** `cli/doc.go` (diseño) + `cli/cli_test.go` (tag `superapp_cli`):
  `TestMain` compila `cmd/quark` una vez; `TestCLICoverage` ejerce los **21 paths
  de comando** contra SQLite real con gate de reconciliación (falla si un comando
  del inventario queda sin cubrir y no está en allowlist). 20/21 cubiertos;
  `tenant provision` en allowlist (CREATE DATABASE/SCHEMA + DSN admin, no SQLite).
  - **Database-first ejercido:** `model generate --from-table` introspecciona la
    BD → emite modelos Go que **compilan** (`assertGoBuilds`); luego `gen
    --dry-run` corre el codegen forward sobre ellos. Esquema sembrado rico
    (int PK / text not-null / int·real·bool·timestamp nullable / json) para
    ejercer el mapeo SQL→Go.
  - **Aprendido:** el binario abre SQLite/PG/MySQL por drivers transitivos (no hace
    falta tocar `cmd/quark`); config por env `QUARK_DATABASE_DEFAULT_{DRIVER,DSN}`
    (viper AutomaticEnv) o `.quark.yml`. Migraciones del CLI son ficheros Go, así
    que `migrate up/down` son inertes para ficheros creados por el CLI (assert de
    exit, no de efecto). `sync`/`seed run`/`seed list` a nivel binario son
    advisory (no hay structs/seeders compilados) — exit 0 con guía.
  - **Bug encontrado (flagueado, `task_657121df`):** `model generate <Name>
    --fields` no hace `MkdirAll` del `--out` (a diferencia de `--from-table`) y
    sale 0 aunque falle. El subtest pre-crea el dir como workaround; quítalo al
    arreglar el CLI.
- **Pendiente S9 full:** inventario de comandos enumerado de cobra (no hardcoded),
  golden output por comando (snapshots), y la matriz cross-engine (reusa el runner
  de S4 — el mismo binario, distinto `QUARK_DATABASE_DEFAULT_*`).

## Definición de hecho (gate)

`apisurface.json` reconciliado al **100% in-scope** en los 6 motores (o
allowlist justificada), todos los asserts funcionales/seguridad/paridad en
verde, matriz emitida a `REPORTS/`, y CI verde.

## No te dejes

- ~~Doc-sync DS-1..DS-5~~ — cerrados (PR #178, 2026-06-09). Queda **DS-6** (BAJO,
  decisión del owner): `roadmap.mdx` "four testcontainers CI engines" — ver
  `TASKS.md` § "Doc-sync". No bloquea la superapp.
