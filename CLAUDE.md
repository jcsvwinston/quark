# Quark — instrucciones para Claude Code

> Este archivo se carga automáticamente al inicio de cada sesión. Mantenlo conciso. El detalle vive en `docs/ANALISIS_MADUREZ.md`, `TASKS.md` e issues de GitHub.

## Qué es Quark

ORM en Go pensado para el ecosistema **Nucleus** (framework MVC/REST), pero también consumible como módulo independiente (`github.com/jcsvwinston/quark`). Soporta SQLite, PostgreSQL, MySQL, MariaDB, MSSQL y Oracle.

## Estado real del proyecto (importante)

Quark está en **v1.3.0** (tag 2026-07-15; minor: expone `IntersectAll`/`ExceptAll` — las variantes multiset INTERSECT ALL / EXCEPT ALL — y corrige su soporte por dialecto: SQL Server y Oracle no las tienen y ahora devuelven `ErrUnsupportedFeature` en vez de emitir SQL inválido; se corrige también el godoc que negaba INTERSECT/EXCEPT en MariaDB. Barrido de jerga interna en el sitio publicado + linter `check_docs_product_voice.sh`). v1.2.2 (tag 2026-07-13) ejecutó el backlog de auditoría v1.2.1 — QK-P0-1..4, QK-P1-*, QK-P2-1..6; v1.2.1 curó la CLI/docs/guard — H-Q1..H-Q12. Sobre la línea estable `v1.x` (SemVer). v1.0.0 (tag 2026-05-27) fue el primer release estable — gateado contra el checklist cualitativo de [`docs/V1_GATE.md`](docs/V1_GATE.md) (cerrado 5/5), no contra una métrica de rendimiento —, v1.1.0 (tag 2026-06-06) fue el release de **hardening** (salida del bug-bash post-v1.0, fases F0–F14 completas, BB-1…BB-15 cerrados) y v1.2.0 cierra los deferrals de scaling: stampede cross-instancia (ADR-0020), `ShardKeyer` (ADR-0021) y scatter-gather (ADR-0022), más el pin de seguridad go1.26.5 + pgx v5.9.2. `v1.x` mantiene compatibilidad de API; los breaking changes van a `v2.x` con guía de migración. **Al taggear un minor, corre el checklist de release completo**: README/SECURITY/CLAUDE.md/release-notes del sitio a la versión nueva + `docs/RELEASE_NOTES_vX.Y.0.md` (en v1.2.0 no se hizo y el drift lo cazó la auditoría — H-Q6).

Para el estado **vivo** trabaja desde [`TASKS.md`](TASKS.md) (backlog táctico + hallazgos de bug-bash) y [`docs/ROADMAP.md`](docs/ROADMAP.md) (fases entregadas + deferrals a v1.2+). [`docs/ANALISIS_MADUREZ.md`](docs/ANALISIS_MADUREZ.md) es la referencia narrativa de fondo (análisis crítico, comparativa, el plan de fases que llevó a v1.0); léela en onboarding, no para consulta operativa.

**Sin lenguaje de marketing.** No uses superlativos de hype ("enterprise-grade", "production-ready", "battle-tested", "blazing fast") en commits, PRs, issues ni docs — describe lo que hace con precisión técnica. La regla es **incondicional**: que v1.0/v1.1 estén liberadas no la levanta; la cultura anti-hype del proyecto se mantiene (el grep de `production-ready\|enterprise-grade\|battle-tested` debe seguir vacío en `/release` y `/next-session`).

## Estructura del repo

```
quark/
├── *.go                         ← código del ORM (paquete raíz)
├── cache/, internal/, migrate/, otel/, cmd/quark/  ← subpaquetes
├── examples/                    ← ejemplos por motor (sqlite/postgres/mysql/mssql/oracle)
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
2. **Conventional Commits obligatorio** (`feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`, `BREAKING CHANGE:` en el footer). Ya está documentado en `CONTRIBUTING.md`. No mezcles tipos en un commit.
3. **API y docs se modifican en el mismo PR.** Cualquier cambio que añada/cambie/elimine API pública requiere su entrada en `website/docs/` y en `CHANGELOG.md` dentro del mismo PR. PRs sin esto los rechaza el `code-reviewer` (`.claude/agents/code-reviewer.md`).
4. **Bugs P0 antes que features.** Mientras `TASKS.md` tenga items en `## Bugs P0`, no se trabaja en Fase 1+ del plan. Cualquier feature con un P0 abierto se rechaza.
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
10. Tras mergear, taggear `vX.Y.Z`. La GitHub Action `.github/workflows/deploy.yml` (build de Docusaurus + `actions/deploy-pages`) publica `website/build/` a **GitHub Pages del repo `quark`** en https://jcsvwinston.github.io/quark/. No usa la rama `gh-pages` ni el repo `quark-docs` (el sitio se movió a este repo en v0.3.0).

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
make test-all                                         # los 6 motores (testcontainers; Oracle vía docker run)

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

22 ADRs en formato MADR. Léelos cuando necesites **justificar o cuestionar** un patrón de Quark. Una decisión aceptada no se reabre sin un ADR sucesor.

- [`docs/adr/README.md`](docs/adr/README.md) — índice (22 ADRs, 0001-0022).
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
- **Backlog vivo**: `TASKS.md` en raíz + issues de GitHub.
- **Roadmap público**: `docs/ROADMAP.md` (mantén alineado con el plan de fases del análisis).
- **Comparativa con otros ORMs**: §2 de ANALISIS_MADUREZ y `docs/comparison.md`.
- **Definition of Done de release**: `.claude/commands/release.md`.
- **Arranque de sesión enfocado en pendiente**: `.claude/commands/next-session.md`.
- **Anti-patterns codificados**: invoca el subagente `code-reviewer` (`.claude/agents/code-reviewer.md`) antes de cerrar cualquier PR.
- **Auditoría docs↔código**: subagente `docs-auditor` (`.claude/agents/docs-auditor.md`); pasada periódica vía `/doc-sync`.
- **Superapp de aceptación cross-engine (en construcción)**: arnés headless que ejerce toda la superficie pública en los 6 motores con cobertura demostrada por manifiesto. Instrucciones de continuación en [`examples/superapp/HANDOFF.md`](examples/superapp/HANDOFF.md) (+ blueprint `examples/superapp/README.md`); backlog en `TASKS.md` § "Superapp". **Versionado**: la versión de la librería refleja sólo cambios de la librería. Un PR que sólo toca harness (`examples/superapp/`, `bugbash/`, `benchmarks/`, `TASKS.md`) usa `test(superapp):` o `chore(...):`, **nunca `feat:`/`fix:`** — esos types bumpean la versión y entran en el CHANGELOG. (`release-please-config.json` lista esas rutas en `exclude-paths` como segunda barrera, pero release-please 17.3.0 NO la aplica al paquete raíz — verificado empíricamente en #180→#156 — así que la convención de types es la barrera efectiva.)

## Cómo arrancar una sesión productiva

1. **Invoca `/next-session [foco]`** (definido en `.claude/commands/next-session.md`). El comando audita el estado real del repo y te ancla a un foco concreto (`auto` post-v1.0, el comando deriva el foco de TASKS.md; `doc-sync` para saneamiento documental). Si tras leerlo necesitas saltarlo, justifícalo en el primer mensaje.
2. Si `TASKS.md ## Bugs P0` tiene items vivos, **abandona el foco del slash command** y trabaja un P0 primero — esa regla manda sobre todo lo demás.
3. **Si la sesión va a empujar Quark hacia v1.0**, lee `docs/V1_GATE.md` antes de elegir item. Los items del §A son los únicos que bloquean v1.0; cualquier otro trabajo es legítimo pero no acerca el tag.
4. Identifica el módulo donde vas a tocar y **lee su playbook** (`docs/playbooks/<modulo>.md`).
5. Si el playbook menciona una decisión arquitectónica que te resulta extraña, lee el ADR correspondiente (`docs/adr/`).
6. Di explícitamente qué archivo:línea vas a tocar y pega el extracto antes de proponer el cambio. No "exploras"; vas con un objetivo concreto.
7. Tras cada cambio en API: invoca `code-reviewer` antes del PR (delega automáticamente a `docs-auditor` para coherencia docs↔código); usa `/release` cuando toque tag. Cierra la sesión con la plantilla del `/next-session` (items cerrados / heredados / próximo foco) para no romper el contexto a la siguiente sesión.

**No sintetices el análisis al usuario.** Si el playbook ya cubre una trampa, cita la línea: "Según `docs/playbooks/query-builder.md` §Bugs P0, P0-1 está vivo en `query_builder.go:175-186`. Voy a aplicar el patrón `cloneForGroup` que sugiere."
