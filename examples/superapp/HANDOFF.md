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
- `crud.go`, `tx.go`, `builder.go`, `relations.go`, `security.go`, `cache.go`, `tenant.go` entregados — verdes en SQLite **y PG real**
  (`-tags=superapp_engine`, **40 símbolos**). `tenant.go` cubre **sólo la modalidad RowLevelSecurityClient**
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
- **Falta (orden sugerido):** `tenant.go` **3 estrategias restantes** (el usuario
  pidió full scope; RLSClient ya entregado). Patrones canónicos en el repo:
  - **RLSNative** (PG-only, `control.Supports(FeatRLSNative, rec.Engine())`): mirror
    `rls_native_postgres_test.go` — necesita un **rol no-superuser** (los superusers
    saltan RLS) + DSN URL-form para hacer `SET ROLE` + `ALTER TABLE … ENABLE ROW
    LEVEL SECURITY` + `CREATE POLICY` vía un admin client, luego `router.Tx(ctx, fn)`
    (`TRM("Tx")`) y `For[T](ctx, router)` ven sólo las filas del tenant — enforced
    por el motor incluso desde `Raw()`. En no-PG, `For[T](ctx, nativeRouter)` debe
    rechazar (mirror `rls_native_test.go:TestRowLevelSecurityNativeRejectsNonPostgresViaForT`).
    El admin/non-super client necesita el DSN → pasar `engine.Conn` al exerciser
    (cambio de firma de `Exerciser.Fn` o un campo en el suite) **o** derivar roles
    del client base por `Raw()`.
  - **SchemaPerTenant** (PG/MSSQL, `control.Supports`?): mirror
    `schema_per_tenant_write_test.go` — `CREATE SCHEMA` por tenant; `q.schema=tenantID`
    enruta. MySQL no tiene schemas → rechazo/skip por capability.
  - **DBPerTenant**: factory `func(tenantID) (*Client, error)` que abre un `*Client`
    por tenant con DSN propio (SQLite: ficheros distintos; servers: CREATE DATABASE +
    DSN admin). Necesita el `engine.Conn`/DSN → mismo cambio de firma que RLSNative.
  - Luego: `migrate.go` (round-trip `Migrate`→`PlanMigration`
  vacío, `Sync`, `Backfill`), `ha.go` (replicas/sharding/deadlock + el test
  `-race` de concurrencia del recorder que `code-reviewer` pidió en S2),
  `observability.go` (OTel in-memory + redacción), y **builder-avanzado**
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

**S6 · `main.go`** — flags `-engines`, `-gate`; corre exercisers por motor,
`Reconcile`, `Render` la matriz a `REPORTS/`, `Gate`.

**S7 · CI** — job que corre la superapp en los 6 (patrón `integration` de
`ci.yml`; Oracle docker-run). Gate estricto bloqueante.

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

- Los desfases **Doc-sync DS-1..DS-5** (`TASKS.md` § "Doc-sync") siguen
  pendientes de verificación: `cd website && npm run build`, confirmar el mínimo
  real de Go con compilador (DS-4), y la propagación de `quark-docs` en
  release-notes históricas (DS-3). Ciérralos.
