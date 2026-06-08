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

**S3 · `cmd/gen-apisurface/`** (siguiente) — `go/packages`+`go/types` sobre `quark` y los
subpaquetes públicos (`cache/memory`, `cache/redis`, `otel`, `migrate`,
`quarkmigrate`, `quarktenant`) → `apisurface.json` (vía `go:generate`). Crea
`allowlist.json` con los diferidos a v1.2 (claves exactas `Symbol.Key`): F6-3b
(binder codegen UPDATE/partial/batch), scatter-gather + shard-key-from-entity,
stampede cross-instancia.

**S4 · `engine/`** — Runner SQLite en proceso + matriz. Luego testcontainers
(PG/MySQL/MariaDB/MSSQL) y Oracle docker-run. Teardown + chequeo de fugas
(goroutines, `DBStats.InUse==0`).

**S5 · `exercise/`** — Empieza por `crud.go` como patrón canónico (asserts
funcionales + hook de paridad), luego `builder.go` (CTE/window/setops/locking),
`relations.go` (**confirma tags m2m/polimórfica vs
`website/docs/guides/relations.mdx`**), `tx.go`, `cache.go` (query-count:
hit=0 SQL, N+1 acotado), `tenant.go`, `migrate.go` (round-trip
`Migrate`→`PlanMigration` vacío), `security.go` (attack suite SQLGuard),
`ha.go` (replicas/sharding/deadlock), `observability.go` (OTel in-memory).

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
