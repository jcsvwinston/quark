# Quark — instrucciones para Claude Code

> Este archivo se carga automáticamente al inicio de cada sesión. Mantenlo conciso. El detalle vive en `docs/ANALISIS_MADUREZ.md`, `TASKS.md` e issues de GitHub.

## Qué es Quark

ORM en Go pensado para el ecosistema **Nucleus** (framework MVC/REST), pero también consumible como módulo independiente (`github.com/jcsvwinston/quark`). Soporta SQLite, PostgreSQL, MySQL, MariaDB, MSSQL y Oracle.

## Estado real del proyecto (importante)

**Quark NO es production-ready a pesar de que `docs/RELEASE_NOTES_V1.md` lo sugiera.** El estado real es **alpha-late / MVP avanzado** (~v0.2). Hay un análisis crítico completo en `docs/ANALISIS_MADUREZ.md` que debes leer antes de trabajar en features nuevas; resume los bugs P0 vivos, las brechas estructurales y el plan de fases hasta un v1.0 honesto.

No uses lenguaje de marketing ("enterprise-grade", "production-ready") en commits, PRs, issues, ni docs hasta que el v1.0 honesto esté liberado.

## Estructura del repo

```
quark/
├── *.go                         ← código del ORM (paquete raíz)
├── cache/, internal/, migrate/, otel/, cmd/quark/  ← subpaquetes
├── examples/                    ← ejemplos por motor (sqlite/postgres/mysql/mssql/oracle)
├── docs/                        ← markdown fuente (ROADMAP, ARCHITECTURE, ANALISIS_MADUREZ…)
├── website/                     ← sitio Docusaurus que se publica a jcsvwinston.github.io/quark-docs
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

1. **Tests deben pasar en los 6 motores antes de mergear a `main`.** SQLite corre por defecto; los demás se levantan con testcontainers (ver `TASKS.md` para el setup pendiente). Si tu cambio toca SQL, abre PR sólo cuando los 6 estén verdes.
2. **Conventional Commits obligatorio** (`feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, `test:`, `BREAKING CHANGE:` en el footer). Ya está documentado en `CONTRIBUTING.md`. No mezcles tipos en un commit.
3. **API y docs se modifican en el mismo PR.** Cualquier cambio que añada/cambie/elimine API pública requiere su entrada en `website/docs/` y en `CHANGELOG.md` dentro del mismo PR. PRs sin esto los rechaza el `code-reviewer` (`.claude/agents/code-reviewer.md`).
4. **Bugs P0 antes que features.** Mientras `TASKS.md` tenga items en `## Bugs P0`, no se trabaja en Fase 1+ del plan. Cualquier feature con un P0 abierto se rechaza.
5. **No introduzcas reflect en hot paths sin discutirlo.** Reflect-everywhere es deuda conocida (ver §1.1 de ANALISIS_MADUREZ); el codegen es la salida, planificada para Fase 6. No añadas reflect adicional sin abrir issue primero.
6. **Validación de identifiers SIEMPRE.** Cualquier columna, tabla o expresión que provenga de input del usuario debe pasar por `internal/guard.SQLGuard` antes de concatenarse a SQL. La inconsistencia detectada en `JOIN ON` (que no se valida) está en TASKS como P0 — no la repliques en sitios nuevos.
7. **No uses `t.Skip` para gatear tests por motor.** Usa testcontainers o etiquetas `//go:build integration`. Skips por env var crearon la situación actual de "sólo SQLite cubierto".

## Regla de release: docs SIEMPRE al día con la versión

> **Esta regla es la razón principal de unificar docs y código en el mismo repo. Hacerla saltar rompe la coherencia que estamos intentando recuperar.**

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
10. Tras mergear, taggear `vX.Y.Z` y disparar la GitHub Action que publica `website/build/` al repo `quark-docs` (rama `gh-pages`).

El comando `/release vX.Y.Z` (`.claude/commands/release.md`) automatiza el checklist y verifica cada paso. **Úsalo siempre.** No taggees a mano.

Si encuentras una sesión abriendo PRs que tocan API pero no `website/docs/`, recházalos sin discusión.

## Decisiones arquitectónicas tomadas (no las cuestiones sin abrir issue)

- **Active Record, no Data Mapper.** Modelos son structs con tags + hooks. Nada de Unit of Work / Identity Map al estilo Hibernate.
- **Reflect por defecto, codegen opt-in en Fase 6.** No bifurcar la API.
- **Multi-tenancy: tres estrategias coexisten** (DBPerTenant / SchemaPerTenant / RowLevelSecurityClient — antes `RowLevelSecurity`, alias deprecado hasta v1.0). La modalidad cliente es WHERE-injection en el builder; `RowLevelSecurityNative` (Fase 5, F5-2, PG-only) entrega aislamiento por motor (`SET LOCAL app.tenant_id` + `CREATE POLICY`).
- **Caché L2 integrada** (memory/redis), no plugin externo. Stampede protection y singleflight llegan en Fase 4.
- **No NoSQL.** Quark es relacional.
- **Sin GraphQL/admin auto-generado.** Eso es territorio ent.

## Comandos frecuentes

```bash
# Tests
go test -count=1 -short ./...                         # SQLite + unit tests
QUARK_TEST_POSTGRES_DSN=... go test -count=1 ./...    # añade postgres
make test-all                                         # los 6 motores con testcontainers (cuando esté setup)

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

8 ADRs en formato MADR. Léelos cuando necesites **justificar o cuestionar** un patrón de Quark. Una decisión aceptada no se reabre sin un ADR sucesor.

- [`docs/adr/README.md`](docs/adr/README.md) — índice.
- ADR 0001 — Active Record, no Data Mapper.
- ADR 0002 — Reflect default, codegen opt-in en Fase 6.
- ADR 0003 — RLS hoy es WHERE-injection cliente; motor real en Fase 5.
- ADR 0004 — Caché L2 integrada (no plugin externo).
- ADR 0005 — Sólo relacional (no NoSQL).
- ADR 0006 — Sin GraphQL ni admin auto-generado.
- ADR 0007 — Multi-tenancy: tres estrategias coexisten.
- ADR 0008 — Documentación se modifica en el mismo PR que la API.

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

- **Gate v1.0 (lo que falta para taggear honesto)**: [`docs/V1_GATE.md`](docs/V1_GATE.md) — **este es el bloqueante actual a v1.0**. Formaliza el checklist que ADR-0017 §3 delegó tras retirar el gate ≥3× p99 de ADR-0002. Léelo antes de pensar en un `/release v1.0.0`.
- **Backlog vivo**: `TASKS.md` en raíz + issues de GitHub.
- **Roadmap público**: `docs/ROADMAP.md` (mantén alineado con el plan de fases del análisis).
- **Comparativa con otros ORMs**: §2 de ANALISIS_MADUREZ y `docs/comparison.md`.
- **Definition of Done de release**: `.claude/commands/release.md`.
- **Arranque de sesión enfocado en pendiente**: `.claude/commands/next-session.md`.
- **Anti-patterns codificados**: invoca el subagente `code-reviewer` (`.claude/agents/code-reviewer.md`) antes de cerrar cualquier PR.
- **Auditoría docs↔código**: subagente `docs-auditor` (`.claude/agents/docs-auditor.md`); pasada periódica vía `/doc-sync`.

## Cómo arrancar una sesión productiva

1. **Invoca `/next-session [foco]`** (definido en `.claude/commands/next-session.md`). El comando audita el estado real del repo y te ancla a un foco concreto (`fase6` mientras Fase 6 esté abierta; `doc-sync` para saneamiento documental; `auto` para que el comando proponga). Si tras leerlo necesitas saltarlo, justifícalo en el primer mensaje.
2. Si `TASKS.md ## Bugs P0` tiene items vivos, **abandona el foco del slash command** y trabaja un P0 primero — esa regla manda sobre todo lo demás.
3. **Si la sesión va a empujar Quark hacia v1.0**, lee `docs/V1_GATE.md` antes de elegir item. Los items del §A son los únicos que bloquean v1.0; cualquier otro trabajo es legítimo pero no acerca el tag.
4. Identifica el módulo donde vas a tocar y **lee su playbook** (`docs/playbooks/<modulo>.md`).
5. Si el playbook menciona una decisión arquitectónica que te resulta extraña, lee el ADR correspondiente (`docs/adr/`).
6. Di explícitamente qué archivo:línea vas a tocar y pega el extracto antes de proponer el cambio. No "exploras"; vas con un objetivo concreto.
7. Tras cada cambio en API: invoca `code-reviewer` antes del PR (delega automáticamente a `docs-auditor` para coherencia docs↔código); usa `/release` cuando toque tag. Cierra la sesión con la plantilla del `/next-session` (items cerrados / heredados / próximo foco) para no romper el contexto a la siguiente sesión.

**No sintetices el análisis al usuario.** Si el playbook ya cubre una trampa, cita la línea: "Según `docs/playbooks/query-builder.md` §Bugs P0, P0-1 está vivo en `query_builder.go:175-186`. Voy a aplicar el patrón `cloneForGroup` que sugiere."
