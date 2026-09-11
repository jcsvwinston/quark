# Quark — instrucciones para Claude Code

> Este archivo se carga automáticamente al inicio de cada sesión. Mantenlo conciso. El detalle vive en `docs/ANALISIS_MADUREZ.md` e issues de GitHub.

## Qué es Quark

ORM en Go pensado para el ecosistema **Nucleus** (framework MVC/REST), pero también consumible como módulo independiente (`github.com/jcsvwinston/quark`). Soporta SQLite, PostgreSQL, MySQL, MariaDB, MSSQL y Oracle.

## Estado real del proyecto (importante)

Quark está en **v1.14.0**. <!-- x-release-please-version -->

Historial reciente (las tres últimas; el historial completo, una versión por párrafo, en [`.claude/HISTORIAL.md`](.claude/HISTORIAL.md) — al cortar una release, añade la entrada nueva aquí Y en `.claude/HISTORIAL.md` (allí ya están todas), y borra de aquí la más vieja): v1.12.0 (tag 2026-09-08; minor del arco A2 —starter de suite—: `quark init --with nucleus` escribe `internal/<app>/module.go` (un módulo de Nucleus que envuelve el `*quark.Client`: modelo `Note` migrado en OnStart, `GET`/`POST /api/notes` vía `quark.For[Note]`, políticas propias con `create` como default de desarrollo documentado, exención CSRF y el driver del dialecto importado en blanco) y el `nucleus.yml` mínimo para arrancar, sin sobrescribir nada; sale como TEXTO FUENTE porque Quark no depende del framework (QADR-0001/0006) y este repo no puede compilarlo contra Nucleus. Ejemplos `examples/{chi,echo,gin}` como módulos propios con test y guía `guides/frameworks.mdx`. `drivers/postgres` v0.1.3 adopta `quarkdriver.ListenerFactory` (paso 2 de QK-8). El historial de este fichero vive desde ahora en `.claude/HISTORIAL.md` y el esqueleto de release mueve el puntero del README) v1.11.0 (tag 2026-09-05; minor que paga la parte de Quark en la auditoría de madurez 2026-09-03 —arco A1—: el migrador toma por defecto el candado de esquema del motor (`quark:schema`, 30 s: `pg_advisory_lock`, `GET_LOCK`, `sp_getapplock`, `DBMS_LOCK`; SQLite sin candado porque tiene un solo escritor) antes de tocar el esquema —dos réplicas corriendo `migrate up` a la vez aplicaban la misma migración pendiente—, con `WithoutLock`/`WithLockTimeout`/`WithLockName`/`WithLogger`; migraciones transaccionales `UpTx`/`DownTx` que reciben un `*sql.Tx` y comitean la migración y su fila del ledger juntas donde el motor hace rollback del DDL (PostgreSQL, SQLite, SQL Server) y sólo la fila donde el DDL se comitea solo (MySQL, MariaDB, Oracle), dicho a nivel debug; `Client.Logger()` para que el migrador informe por el logger del cliente y no por stdout (los comandos `quark migrate` siguen imprimiendo sus frases: `migrateProgress`); y el contrato público del listener —`quarkdriver.ListenerFactory` sobre la interfaz `IdentifierValidator`, con `RegisterListenerFactory`/`MustRegisterListenerFactory`/`LookupListenerFactory`— porque el `NewListenerFunc` anterior nombraba el tipo interno del guard y ningún módulo fuera del repo podía implementarlo; los nombres viejos quedan deprecados y adaptados, `drivers/postgres` v0.1.1 sigue funcionando y adopta el contrato en su siguiente release. QK-14 (el `go.mod` raíz requiere los cinco drivers porque el CLI enlaza todos los motores) se DOCUMENTA y se difiere al arco A3: sacarlos exige mover el CLI a módulo propio. Los mensajes de commit y los títulos de PR pasan a inglés con guard `check_pr_title_english.sh`. Los `drivers/*` requieren v1.10.1: el suelo de los hermanos sube como PRIMER commit de cada tren) v1.10.1 (tag 2026-09-04; patch de la auditoría de madurez 2026-09-03: `IsRegistered` aceptaba solo el alias, así que lib/pq y mattn/go-sqlite3 —enlazados y registrados— se rechazaban pidiendo un módulo; ahora acepta nombre y alias, y avisa con WARN cuando el driver está enlazado sin clasificador (unicidad/deadlock/transitorio contestaban false en silencio). Los cinco `drivers/*` requieren v1.10.0 y compilan solos, con lane sin workspace; README/guías/ejemplos importan `drivers/<x>`; los ejemplos con driver son módulos propios porque la raíz no puede requerir sus satélites; fuera el binario `superapp` de 42 MB trackeado. Desde este tren release-please corta raíz y drivers en UN solo PR, sin `release-as` pegajoso) v1.10.0 (tag 2026-09-02; minor pequeña que cierra lo que v1.9.0 dejó a medias: al faltar el módulo de un driver, el error lo NOMBRA. v1.9.0 sacó cada motor a su módulo pero abrir una base de datos sin él seguía fallando con el mensaje de Go —«unknown driver "sqlite" (forgotten import?)»—, que no nombra ni el módulo ni la línea; ahora imprime el `go get` y el `import _` y resuelve los alias que la gente teclea (postgresql/postgres/pq → drivers/postgres, mariadb → drivers/mysql, mssql → drivers/mssql). Para un motor que este proyecto no publica devuelve vacío A PROPÓSITO y deja pasar el error original: inventarle un `go get` mandaría al lector a algo que no existe. La tabla es DATOS —quark lleva el nombre del módulo satélite y nada de su código— y los alias se resuelven sin distinguir mayúsculas al construir la pista pero SÍ distinguiéndolas al comprobar el registro, porque lo primero ayuda a una persona y lo segundo responde por database/sql. Incluye además la deuda de doc que v1.9.0 se dejó: su snapshot de documentación —cortado a mano, porque Docusaurus no arranca en el entorno y docs:version es copia literal—, las notas narrativas y las menciones de versión; el snapshot va DESPUÉS de las notas o archivaría una versión anunciando otra, que es lo que rechaza check_versioned_docs_markers.sh)

**Checklist de release (desde v1.5.2, semi-automatizado)**: en una MINOR, lo PRIMERO es cortar el snapshot de documentación —`bash scripts/release/cut_docs_snapshot.sh X.Y.0`— en un PR que entre ANTES del de release: el paraguas sirve la doc del TAG pinado, así que un snapshot añadido después no llega al lector hasta la release siguiente (`check_docs_archive_freshness.sh` falla si una minor se publica sin él; un patch no lo necesita). Después: las MENCIONES de versión de los TRES `extra-files` (README/SECURITY/cabecera del sitio) las bumpa release-please solo (extra-files + x-release-please-version), y OJO: en SECURITY.md eso es SOLO la línea del marcador, no la tabla de versiones soportadas; ESTE fichero NO está en `extra-files` a propósito —el updater genérico reescribe TODAS las apariciones de la versión anterior y falsearía las que cita la línea de historial—, así que su línea marcada la bumpa el esqueleto, no release-please; los ESQUELETOS de la sección del sitio y de `docs/RELEASE_NOTES_vX.Y.Z.md`, la línea marcada de este fichero, el puntero de notas del README y las FILAS de esa tabla de SECURITY.md los escribe `bash scripts/release/gen_release_notes_skeleton.sh` en la rama del release PR (idempotente, material crudo del CHANGELOG en comentarios, filas derivadas del manifest con el conjunto que dicta el propio guard); la PROSA final y la entrada de historial de este fichero se redactan a mano — la regla anti-hype no se delega. El guard `check-version-coherence.sh` sigue siendo el juez (en v1.2.0 el checklist no se corrió y el drift lo cazó la auditoría — H-Q6).

Para el estado **vivo** trabaja desde los issues de GitHub y [`docs/ROADMAP.md`](docs/ROADMAP.md) (fases entregadas + deferrals a v1.2+); [`TASKS.md`](TASKS.md) es un registro **histórico** de la era v0.x–v1.1.x (congelado 2026-06-20) — contexto, no backlog. [`docs/ANALISIS_MADUREZ.md`](docs/ANALISIS_MADUREZ.md) es la referencia narrativa de fondo (análisis crítico, comparativa, el plan de fases que llevó a v1.0); léela en onboarding, no para consulta operativa.

**Sin lenguaje de marketing.** No uses superlativos de hype ("enterprise-grade", "production-ready", "battle-tested", "blazing fast") en commits, PRs, issues ni docs — describe lo que hace con precisión técnica. La regla es **incondicional**: que v1.0/v1.1 estén liberadas no la levanta; la cultura anti-hype del proyecto se mantiene (el grep de `production-ready\|enterprise-grade\|battle-tested` debe seguir vacío en `/release` y `/next-session`).

## Estructura del repo

```
quark/
├── *.go                         ← código del ORM (paquete raíz)
├── cache/, internal/, migrate/, otel/  ← subpaquetes de la biblioteca
├── cmd/quark/                   ← el CLI, MÓDULO PROPIO (ADR-0024), tags cmd/quark/vX.Y.Z
├── drivers/                     ← un módulo por motor (ADR-0023)
├── internal/enginesuite/        ← las suites por motor, MÓDULO PROPIO sin publicar (ADR-0024)
├── examples/                    ← ejemplos por motor (sqlite/postgres/mysql/mssql/oracle); superapp es módulo propio
├── docs/                        ← markdown fuente (ROADMAP, ARCHITECTURE, ANALISIS_MADUREZ…)
├── website/                     ← sitio Docusaurus publicado en GitHub Pages del repo quark (jcsvwinston.github.io/quark/) vía .github/workflows/deploy.yml
│   ├── docusaurus.config.ts
│   ├── sidebars.ts
│   ├── docs/                    ← contenido versionable del sitio
│   ├── versioned_docs/          ← snapshots por versión (cuando exista)
│   └── src/, static/, blog/
└── .claude/
    ├── commands/                ← slash commands custom (/release, …)
    └── agents/                  ← subagentes especializados (code-reviewer, …)
```

`docs/` (markdown plano) y `website/docs/` (fuente del sitio) **pueden divergir intencionalmente**: `docs/` contiene material interno (ANALISIS_MADUREZ, ROADMAP), `website/docs/` contiene el material público. Los documentos públicos deben vivir en `website/docs/` y enlazarse desde `docs/` con un puntero, no duplicarse.

## Reglas duras

1. **Tests deben pasar en los 6 motores antes de mergear a `main`.** SQLite corre in-process; PostgreSQL/MySQL/MariaDB/MSSQL vía testcontainers y Oracle vía `docker run` (`gvenzl/oracle-free`; ver `.github/workflows/ci.yml`). La matriz por-motor ya es bloqueante en CI (F0-8 cerrado). Si tu cambio toca SQL, abre PR sólo cuando los 6 estén verdes.
2. **Conventional Commits obligatorio** (`feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`, `BREAKING CHANGE:` en el footer), **en inglés**: el producto habla inglés (QADR-0007) y el título del squash es la línea del changelog y de la release — CI rechaza un título de PR en español (`scripts/ci/check_pr_title_english.sh`). Ya está documentado en `CONTRIBUTING.md`. No mezcles tipos en un commit. Un `!` o un `BREAKING CHANGE:` es una MAJOR de Quark y, por QADR-0002, de toda la suite: se decide antes del merge.
3. **API y docs se modifican en el mismo PR.** Cualquier cambio que añada/cambie/elimine API pública requiere su entrada en `website/docs/` y en `CHANGELOG.md` dentro del mismo PR. PRs sin esto los rechaza el `code-reviewer` (`.claude/agents/code-reviewer.md`).
4. **Bugs P0 antes que features.** Mientras haya bugs P0 abiertos (issues de GitHub), no se trabaja en Fase 1+ del plan. Cualquier feature con un P0 abierto se rechaza.
5. **No introduzcas reflect en hot paths sin discutirlo.** Reflect-everywhere es deuda conocida (ver §1.1 de ANALISIS_MADUREZ); el codegen es la salida, entregada en Fase 6 (v1.0.0). No añadas reflect adicional sin abrir issue primero.
6. **Validación de identifiers SIEMPRE.** Cualquier columna, tabla o expresión que provenga de input del usuario debe pasar por `internal/guard.SQLGuard` antes de concatenarse a SQL. La inconsistencia detectada en `JOIN ON` (que no se valida) está en TASKS como P0 — no la repliques en sitios nuevos.
7. **La cobertura de los 6 motores la garantiza la matriz `integration` bloqueante, no un veto al `t.Skip`.** Las suites por-motor (`TestSuitePostgres`, …) resuelven el DSN por precedencia: (1) `QUARK_TEST_<MOTOR>_DSN`; (2) fallback a testcontainers, **compilado sólo bajo `-tags=integration`** (`containers_test.go`). El build **sin `-tags=integration`** (`go test ./...` o `go test -short ./...`) no compila el fallback de containers → la suite hace `t.Skip`. **En CI eso NO resta cobertura:** la matriz `integration` corre `-tags=integration` sobre los 6 (PG/MySQL/MariaDB/MSSQL por testcontainers in-process; Oracle por `docker run` + `QUARK_TEST_ORACLE_DSN`) y es **bloqueante para mergear** (F0-8) — ahí el contenedor arranca y el skip nunca dispara. El `t.Skip` por env-var es, por tanto, una **conveniencia de dev local sin Docker** (corre un motor rápido contra tu propia BD, o salta), no un agujero de cobertura. **Lo prohibido:** sacar un motor de la matriz bloqueante, o hacer que la cobertura *en CI* dependa de un env-var. (Histórico: antes de que la matriz fuera bloqueante, los skips por env-var sí dejaron "sólo SQLite cubierto"; hoy eso lo previene la matriz, no un veto al skip.)

## Regla de release: docs SIEMPRE al día con la versión

> **Esta regla es la razón principal de unificar docs y código en el mismo repo. Hacerla saltar rompe la coherencia que estamos intentando recuperar.**

> **Qué dispara un release (release-please):** sólo `feat`/`fix`/`perf`/`refactor`/`revert` (cambios de código). `docs` y `test` están `hidden` en `release-please-config.json` (junto a `chore`/`ci`/`build`/`style`) — **no disparan un release** ni entran en el CHANGELOG de la librería. Los docs se publican por el pipeline del sitio; un cambio docs-only no corta versión (si no, el propio bump de docs de esta regla generaría un release circular — lo que pasó con la PR #209 de v1.1.5, ya cerrada).

Cuando se taggea una nueva versión `vX.Y.Z`, **el mismo PR que bumpea la versión** debe:

1. Actualizar `CHANGELOG.md` con todas las entradas desde el último tag (formato Keep a Changelog).
2. Actualizar la versión en `README.md` (badges, snippets de instalación, ejemplos).
3. Actualizar `go.mod` si aplica (cambio de major: nuevo path `/v2`, etc.).
4. **Versionar la documentación de Docusaurus**:
   ```bash
   cd website
   npm run docusaurus docs:version X.Y.Z
   git add docs/ versioned_docs/ versioned_sidebars/ versions.json
   ```
   Esto congela el contenido actual de `website/docs/` como `website/versioned_docs/version-X.Y.Z/` y deja `website/docs/` como "next".
5. Revisar `website/sidebars.ts` por si hay nuevas páginas no enlazadas.
6. Validar que todos los ejemplos (`examples/*/main.go`) siguen compilando con la nueva API.
7. Si la release tiene breaking changes, escribir/actualizar `docs/MIGRATION_vX.Y.Z.md` y enlazarlo desde el sidebar y el release.
8. Escribir/actualizar `docs/RELEASE_NOTES_vX.Y.Z.md`. **No añadas marketing.** Lista features, fixes, breaking changes con referencia al issue/PR.
9. Verificar que el badge de coverage en README refleja un reporte real (no un número hardcoded).
10. Tras mergear, taggear `vX.Y.Z`. Las docs se publican en el **sitio unificado de la suite** (https://jcsvwinston.github.io/quantum/quark/), que el repo `quantum` ensambla desde `website/docs` de este repo en cada deploy; `.github/workflows/deploy.yml` de quark publica solo un REDIRECTOR en Pages de quark hacia el sitio unificado (no el build de Docusaurus — eso fue así hasta el traslado al paraguas).

El comando `/release vX.Y.Z` (`.claude/commands/release.md`) automatiza el checklist y verifica cada paso. **Úsalo siempre.** No taggees a mano.

Si encuentras una sesión abriendo PRs que tocan API pero no `website/docs/`, recházalos sin discusión.

## Decisiones arquitectónicas tomadas (no las cuestiones sin abrir issue)

- **Active Record, no Data Mapper.** Modelos son structs con tags + hooks. Nada de Unit of Work / Identity Map al estilo Hibernate.
- **Reflect por defecto, codegen opt-in (entregado en Fase 6, v1.0.0).** No bifurcar la API.
- **Multi-tenancy: tres estrategias coexisten** (DBPerTenant / SchemaPerTenant / RowLevelSecurityClient — antes `RowLevelSecurity`, alias deprecado desde v1.0; se retira en v2.0). La modalidad cliente es WHERE-injection en el builder; `RowLevelSecurityNative` (Fase 5, F5-2, PG-only) entrega aislamiento por motor (`SET LOCAL app.tenant_id` + `CREATE POLICY`).
- **Caché L2 integrada** (memory/redis), no plugin externo. Stampede protection y singleflight llegan en Fase 4.
- **No NoSQL.** Quark es relacional.
- **Sin GraphQL/admin auto-generado.** Eso es territorio ent.

## Comandos frecuentes

```bash
# Tests
go test -count=1 -short ./...                         # SQLite + unit tests
QUARK_TEST_POSTGRES_DSN=... go test -count=1 ./...    # añade postgres
make test-all                                         # matriz de motores por DSN (testcontainers con -tags integration; Oracle: make oracle-up)

# Docs site (durante desarrollo)
cd website && npm install && npm run start            # localhost:3000
cd website && npm run build                           # genera build/

# Versionado de docs
cd website && npm run docusaurus docs:version X.Y.Z   # congela versión actual

# Release (usa el slash command)
/release v=0.3.0

# Arranque de sesión (usa el slash command)
/next-session            # auto: audita estado y propone foco
/next-session f0         # bloque A: auditar y cerrar Fase 0 (limpieza/infra)
/next-session tipos      # bloque B: tipos diferidos de Fase 1 (arrays PG, timezones)
/next-session fase3      # bloque C: apertura formal de Fase 3 (sólo si A está cerrado)
```

## Memoria estructurada — léeme ANTES de tocar código

> Los siguientes archivos son la **memoria operativa de Code para este proyecto**. Están pensados para que los consultes selectivamente, no para que los leas todos cada sesión. Cada uno tiene frontmatter parseable con metadata.

### Capa 1 — Decisiones arquitectónicas (`docs/adr/`)

24 ADRs en formato MADR. Léelos cuando necesites **justificar o cuestionar** un patrón de Quark. Una decisión aceptada no se reabre sin un ADR sucesor.

- [`docs/adr/README.md`](docs/adr/README.md) — índice (24 ADRs, 0001-0024).
- ADR 0001 — Active Record, no Data Mapper.
- ADR 0002 — Reflect default, codegen opt-in (Fase 6, v1.0.0; gate ≥3× retirado por 0017).
- ADR 0003 — RLS cliente vía WHERE-injection (superseded por 0012).
- ADR 0004 — Caché L2 integrada (no plugin externo).
- ADR 0005 — Sólo relacional (no NoSQL).
- ADR 0006 — Sin GraphQL ni admin auto-generado.
- ADR 0007 — Multi-tenancy: tres estrategias coexisten.
- ADR 0008 — Documentación se modifica en el mismo PR que la API.
- ADR 0009 — Migrations: diff por introspección, no sólo ficheros versionados.
- ADR 0010 — Timezones por columna (Client default + tag, wire UTC).
- ADR 0011 — Cache stampede protection vía wrapper sobre CacheStore.
- ADR 0012 — RLS real Postgres (`SET LOCAL app.tenant_id` + `CREATE POLICY`).
- ADR 0013 — Hooks transaccionales + EventBus síncrono en commit-phase.
- ADR 0014 — Codegen coexiste vía registry tipado con fallback a reflect.
- ADR 0015 — Read replicas: routing en ejecución, opt-in, sticky read-your-writes.
- ADR 0016 — Sharding: ShardRouter por shard key, sin cross-shard implícito.
- ADR 0017 — Codegen es type-safety, no velocidad; retira el gate ≥3× p99.
- ADR 0018 — Lock de migración Oracle vía `DBMS_LOCK` (session-scoped).
- ADR 0019 — Inbound LISTEN/NOTIFY (PG) sobre `*sql.Conn` dedicada del pool.
- ADR 0020 — Cache-stampede cross-instancia vía capacidad opcional `CacheLocker` (opt-in `WithCacheCrossInstance`, wait-and-reread).
- ADR 0021 — Shard key desde la entidad vía interfaz `ShardKeyer` (`WithShardKeyOf` caller-side, no un hook del router).
- ADR 0022 — Scatter-gather cross-shard reads vía funcs explícitas (`ScatterGather`/`ScatterCount`); merge caller-side (`ScatterMerge`), agregados no-COUNT diferidos.
- ADR 0023 — Los drivers salen a módulos propios; el contrato vive en `quarkdriver` y los tres predicados de clasificación viajan juntos.
- ADR 0024 — El CLI a su propio módulo, con el superapp y las suites por motor. La build list del consumidor baja de 123 a 39 y el binario no cambia. El CLI se construye SIEMPRE dentro de un workspace (no puede llevar `replace`: `go install` lo rechaza) y tiene su propia serie de versiones; `quark version` imprime las dos. Notas de ejecución al final del ADR.

### Capa 2 — Playbooks operativos por módulo (`docs/playbooks/`)

6 cheat sheets. **Lee el playbook del módulo donde vayas a tocar antes de escribir código.** Cada uno lista bugs P0 vivos, anti-patterns, decisiones aplicables, y archivo:línea concretos.

- [`docs/playbooks/README.md`](docs/playbooks/README.md) — índice.
- [`docs/playbooks/query-builder.md`](docs/playbooks/query-builder.md) — `query_builder.go`, `query_exec.go`, `query_crud.go`.
- [`docs/playbooks/dialects.md`](docs/playbooks/dialects.md) — `dialect.go`.
- [`docs/playbooks/migrations.md`](docs/playbooks/migrations.md) — `migrator.go`, `sync.go`, `migrate/`.
- [`docs/playbooks/tenant.md`](docs/playbooks/tenant.md) — `tenant_router.go`, `client.go`.
- [`docs/playbooks/cache.md`](docs/playbooks/cache.md) — `cache.go`, `cache/memory/`, `cache/redis/`.
- [`docs/playbooks/security.md`](docs/playbooks/security.md) — `internal/guard/`, `security.go`.

### Capa 3 — Referencia narrativa humana

- [`docs/ANALISIS_MADUREZ.md`](docs/ANALISIS_MADUREZ.md) — análisis crítico completo: estado, comparativa con otros ORMs, plan de fases. Léelo en onboarding o cuando necesites contexto de fondo. **No es para consulta operativa** — para eso están los playbooks.

## Otros punteros

- **Gate v1.0 (cerrado)**: [`docs/V1_GATE.md`](docs/V1_GATE.md) — los 5 items §A están en verde, v1.0.0 está taggeada (2026-05-27). Lo dejamos como referencia histórica del proceso; ya no bloquea trabajo nuevo.
- **Bug-bash post-v1.0 (herramienta operativa)**: [`docs/BUGBASH_PLAN.md`](docs/BUGBASH_PLAN.md) + [`bugbash/DOMAIN.md`](bugbash/DOMAIN.md). Slash command `/bugbash`, subagente `bugbash-reporter`. **Antes de taggear cualquier v1.0.x patch: F0+F1+F13 obligatorios.** Antes de cualquier v1.x.0 minor: pasada completa F0-F13. Los fallos aparecen en `TASKS.md` § "Bug-bash hallazgos".
- **Backlog vivo**: issues de GitHub. `TASKS.md` en raíz es histórico (congelado en la era v1.1.x); solo sus secciones de tooling reciben escrituras.
- **Roadmap público**: `docs/ROADMAP.md` (mantén alineado con el plan de fases del análisis).
- **Comparativa con otros ORMs**: §2 de ANALISIS_MADUREZ y `docs/comparison.md`.
- **Definition of Done de release**: `.claude/commands/release.md`.
- **Arranque de sesión enfocado en pendiente**: `.claude/commands/next-session.md`.
- **Anti-patterns codificados**: invoca el subagente `code-reviewer` (`.claude/agents/code-reviewer.md`) antes de cerrar cualquier PR.
- **Auditoría docs↔código**: subagente `docs-auditor` (`.claude/agents/docs-auditor.md`); pasada periódica vía `/doc-sync`.
- **Superapp de aceptación cross-engine (en construcción)**: arnés headless que ejerce toda la superficie pública en los 6 motores con cobertura demostrada por manifiesto. Instrucciones de continuación en [`examples/superapp/HANDOFF.md`](examples/superapp/HANDOFF.md) (+ blueprint `examples/superapp/README.md`); backlog en `TASKS.md` § "Superapp". **Versionado**: la versión de la librería refleja sólo cambios de la librería. Un PR que sólo toca harness (`examples/superapp/`, `bugbash/`, `benchmarks/`, `TASKS.md`) usa `test(superapp):` o `chore(...):`, **nunca `feat:`/`fix:`** — esos types bumpean la versión y entran en el CHANGELOG. (`release-please-config.json` lista esas rutas en `exclude-paths` como segunda barrera, pero release-please 17.3.0 NO la aplica al paquete raíz — verificado empíricamente en #180→#156 — así que la convención de types es la barrera efectiva.)

## Cómo arrancar una sesión productiva

1. **Invoca `/next-session [foco]`** (definido en `.claude/commands/next-session.md`). El comando audita el estado real del repo y te ancla a un foco concreto (`auto` post-v1.0, el comando deriva el foco de TASKS.md; `doc-sync` para saneamiento documental). Si tras leerlo necesitas saltarlo, justifícalo en el primer mensaje.
2. Si hay bugs P0 abiertos (issues de GitHub), **abandona el foco del slash command** y trabaja un P0 primero — esa regla manda sobre todo lo demás.
3. **Si la sesión va a empujar Quark hacia v1.0**, lee `docs/V1_GATE.md` antes de elegir item. Los items del §A son los únicos que bloquean v1.0; cualquier otro trabajo es legítimo pero no acerca el tag.
4. Identifica el módulo donde vas a tocar y **lee su playbook** (`docs/playbooks/<modulo>.md`).
5. Si el playbook menciona una decisión arquitectónica que te resulta extraña, lee el ADR correspondiente (`docs/adr/`).
6. Di explícitamente qué archivo:línea vas a tocar y pega el extracto antes de proponer el cambio. No "exploras"; vas con un objetivo concreto.
7. Tras cada cambio en API: invoca `code-reviewer` antes del PR (delega automáticamente a `docs-auditor` para coherencia docs↔código); usa `/release` cuando toque tag. Cierra la sesión con la plantilla del `/next-session` (items cerrados / heredados / próximo foco) para no romper el contexto a la siguiente sesión.

**No sintetices el análisis al usuario.** Si el playbook ya cubre una trampa, cita la línea: "Según `docs/playbooks/query-builder.md` §Bugs P0, P0-1 está vivo en `query_builder.go:175-186`. Voy a aplicar el patrón `cloneForGroup` que sugiere."
