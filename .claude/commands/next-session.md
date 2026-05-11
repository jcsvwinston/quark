---
description: Arranca una sesión de Quark con foco en cerrar el pendiente real (auditoría F0, tipos diferidos de Fase 1, apertura de Fase 3). Sustituye al "exploro a ver qué hay".
argument-hint: [foco opcional: f0 | tipos | fase3 | auto]
---

# /next-session $ARGUMENTS

Estás arrancando una sesión sobre Quark con el repo en estado **post-v0.4.0** (release del 2026-05-10 que cerró Fase 2 completa: AST `Expr`, subqueries, CTEs, window functions, set ops, `HavingAggregate`, nested preload, JoinBuilder tipado, pessimistic locking, IN-chunking automático). **No hay bugs P0 abiertos.** Lo que queda son tres bloques de pendiente real, cada uno con su orden de ataque.

> **Antes de cualquier cosa:** lee `CLAUDE.md` (Reglas duras + Regla de release) y `TASKS.md`. Si `TASKS.md ## Bugs P0` ha cambiado y tiene items vivos, **abandona este flujo y trabaja un P0 primero** — esa regla manda.

## Cómo usar este comando

`$ARGUMENTS` admite uno de cuatro valores. Si está vacío, usa `auto` (presenta menú y pregunta).

| Foco       | Trabaja en                                                  | Cuándo elegirlo                                              |
| ---------- | ----------------------------------------------------------- | ------------------------------------------------------------ |
| `f0`       | Bloque A — auditar y cerrar limpieza/infra de Fase 0        | Sesión corta o si CI sigue siendo SQLite-only                |
| `tipos`    | Bloque B — tipos diferidos de Fase 1 (arrays PG, timezones) | Sesión media; útil para abrir v0.5.0 menor sin tocar infra   |
| `fase3`    | Bloque C — apertura formal de Fase 3 (migraciones serias)   | Sesión larga; sólo si Bloque A está REALMENTE cerrado        |
| `auto`     | Audita primero, propone foco, espera confirmación           | Default                                                      |

## Bloque A — Cerrar Fase 0 de verdad (PRIORIDAD ALTA)

> **Por qué es prioridad:** el header de `TASKS.md` declara "Fase 0 cerrada (2026-05-10)" pero los ítems F0-1..F0-10 (`TASKS.md:542-624`) **no están tachados**. Esa discrepancia es exactamente el tipo de incoherencia pública que motivó esta regla en primer lugar (ver el "Recordatorio final" de `/release`). Hasta que esto cierre con honestidad, la regla de no-marketing y la promoción a Fase 3 quedan en arena movediza.

**Pasos del bloque:**

1. **Auditoría exhaustiva** — para cada F0-N, verifica con comandos concretos si el item está hecho. No te fíes del header. Tabla de checks:

   | Item   | Comando de verificación                                                                                       | Done significa                                                                |
   | ------ | ------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
   | F0-1   | `grep -rn "v1\.0\.0\|RELEASE_NOTES_V1\|production-ready\|enterprise-grade" --include="*.md" .`                | Cero matches fuera de git history; README/SECURITY/CHANGELOG todos en v0.4.0  |
   | F0-2   | `ls examples/blog-api/ 2>/dev/null && cd examples/blog-api && go build ./...`                                 | Existe Y compila; o cero menciones en README                                  |
   | F0-3   | `grep -n "pkg/quark/" examples/README.md`                                                                     | Cero matches                                                                  |
   | F0-4   | `grep -c "^## Quick Start" README.md`                                                                         | Devuelve `1`                                                                  |
   | F0-5   | `grep -n "Coverage" README.md` y revisar si el badge enlaza a un reporte real                                 | Badge dinámico (codecov/codeclimate) o eliminado                              |
   | F0-6   | `ls .github/workflows/deploy-docs.yml`                                                                        | Existe y referencia secret `DOCS_DEPLOY_TOKEN`                                |
   | F0-7   | `cat website/versions.json`                                                                                   | Lista al menos `0.2.0`, `0.3.0`, `0.4.0`                                      |
   | F0-8   | `grep -rn "testcontainers" --include="*.go" .` y `cat .github/workflows/*.yml | grep -A2 strategy`            | Suite de los 6 motores arrancada por testcontainers; CI con job-matrix       |
   | F0-9   | `ls .github/workflows/release-please.yml`                                                                     | Existe y configurado para `release-type: go`                                  |
   | F0-10  | `grep -n "lint-docs" Makefile .github/workflows/*.yml`                                                        | Hay target/job que ejecuta los 4 checks de F0-10                              |

   Para cada item: si está hecho, **táchalo en `TASKS.md`** con `~~F0-N · ...~~` siguiendo el patrón de los P0 cerrados (líneas `:388-509`) y añade el bloque "**Cerrado** — descripción + ruta del fichero que lo prueba". Si no está hecho, déjalo en pie.

2. **Atacar el item de mayor impacto sin cerrar** — ranking sugerido por bloqueo: **F0-8** (testcontainers, sin esto la Regla Dura #1 es honor system) > **F0-6** (pipeline docs, sin esto la regla de release no se ejecuta) > **F0-9** (release-please, automatiza el resto) > F0-10 > F0-1..F0-5.

   - Para **F0-8**: usa `github.com/testcontainers/testcontainers-go` y los módulos por motor. Refactor de `*_suite_test.go` (`mariadb_suite_test.go`, `mssql_suite_test.go`, `mysql_suite_test.go`, `oracle_suite_test.go`, `postgres_suite_test.go`, `sqlite_suite_test.go`) — helper `setupContainer(t)` que devuelve DSN; eliminar los `t.Skip` por env var (regla CLAUDE.md #7). Build tag `//go:build integration` para tests caros. Job-matrix en `.github/workflows/test.yml`.
   - Para **F0-6**: leer `website/docusaurus.config.ts`, confirmar `baseUrl/organizationName/projectName/deploymentBranch`, generar PAT y guardarlo como secret `DOCS_DEPLOY_TOKEN`, escribir `.github/workflows/deploy-docs.yml` (trigger en `push: tags: 'v*'`, build `website/`, push a `quark-docs:gh-pages`).

3. **No abrir Bloque B/C hasta que Bloque A esté cerrado.** Esto cumple la coletilla del CLAUDE.md "Cuándo pasar a Fase 1: cuando los puntos de Setup de infraestructura estén verdes en CI. Antes no" — extendida a Fase 3.

## Bloque B — Tipos diferidos de Fase 1

> **Estado:** F1-2 cerrado en parte (JSON typed, []byte, Duration registrados), pero `TASKS.md:329-336` defiere explícitamente:

- **Arrays Postgres** — wrapper neutro que abstraiga sin pegar `dialect.go` a `pgtype` directamente. Patrón a seguir: el de `quark.JSON[T]` en `json_field.go` (Scanner/Valuer + detección por package+name prefix en `internal/migrate.SQLType`). Sólo PG emite tipo nativo (`INTEGER[]`, `TEXT[]`); resto cae a JSON o falla con `ErrUnsupportedFeature`. Test contra el SharedSuite con skip explícito en non-PG.
- **Timezones por columna** — diseño abierto. Dos alternativas a evaluar:
  - Tag `quark:"tz=UTC"` interpretado en `internal/schema.parseDBTag` y propagado a `FieldMeta.TZ`. `query_crud.go` aplica conversión en bind/scan.
  - Option `Client.WithDefaultTZ(loc)` global + override per-model.

  **Antes de implementar, abre issue para discutir** (regla #5: no añadir reflect adicional sin issue; aplica también a comportamiento implícito en el path de bind).

- **`shopspring/decimal` y `google/uuid` pre-registrados** — explícitamente NO se hace (ver `TASKS.md:333-336`); el usuario los registra con `RegisterTypeMapper` en su `init()`. Si surge presión, abrir issue antes; no añadir dependencias obligatorias por iniciativa propia.

**Salida del bloque:** PR `feat(types): postgres arrays via pgtype wrapper` y/o PR `feat(types): per-column timezone override`. Ambos requieren `code-reviewer` + entradas en `website/docs/guides/modeling.mdx` + CHANGELOG `### Added`.

## Bloque C — Apertura de Fase 3 (migraciones serias)

> **Lee `docs/ANALISIS_MADUREZ.md` §4 Fase 3 antes de tocar nada.** El plan completo está allí; no lo dupliques aquí ni lo reinventes.

Resumen del scope (no exhaustivo):

- **Schema diff real** — introspección completa (tipos, NOT NULL, defaults, índices, FKs, checks) y comparador estructural; `quark schema diff` que emite migration up+down candidata. Hoy `sync.go` sólo compara nombres de columnas; ver `docs/playbooks/migrations.md` para anti-pattern actual.
- **Lock distribuido por dialecto** — PG `pg_advisory_xact_lock`, MySQL `GET_LOCK`, MSSQL `sp_getapplock`, Oracle `DBMS_LOCK.REQUEST`. Necesario para CI multi-instance.
- **Migración transaccional** donde el motor lo permita (`Dialect.SupportsTransactionalDDL`); MySQL → resumable con state checkpointing.
- **Dry-run con plan de cambios** estilo `terraform plan` (DDL up/down + warnings: drop columns, narrowing types, lossy conversions).
- **Backfill orquestado** — `Migration.Backfill(fn func(*Tx) error, batchSize int)` con resume token por PK.
- **Registry per-Client** en lugar de global (ver `migrate/migrate.go`); regla CLAUDE.md ADR 0007 multi-tenancy lo presupone.

**Antes de empezar:** abre **issue de planning** que liste sub-items con orden, estimado y ADR si alguna decisión rompe lo asumido. Esto NO es un bloque para hacer en una sesión; es un bloque para descomponer en F3-1, F3-2, … en `TASKS.md` con la misma granularidad que F1/F2.

## Reglas de la sesión (independientemente del bloque)

1. **Primer paso siempre:** `git status` + `git log --oneline -5` para confirmar que estás en `main` post-v0.4.0 limpio. Si no, para y resuelve.
2. **Lee el playbook del módulo donde vayas a tocar** antes de cualquier edit (`docs/playbooks/{query-builder,dialects,migrations,tenant,cache,security}.md`). Cita línea concreta.
3. **`code-reviewer` obligatorio antes de cerrar PR** (ver `.claude/agents/code-reviewer.md`). Aprobación bloqueante.
4. **Conventional Commits + docs sincronizadas en el mismo PR** (ADR 0008). Sin esto, el reviewer rechaza.
5. **Cero lenguaje de marketing.** Repaso al final de cada PR: `grep -rn "production-ready\|enterprise-grade\|battle-tested" .` debe estar vacío.
6. **Si el cambio toca SQL: 6 motores verdes antes de mergear.** Si F0-8 no está cerrado todavía, corre con DSN env-vars y deja el log en el PR — no es excusa para mergear con sólo SQLite.
7. **Cuando termines:** actualiza `TASKS.md` (tachar item cerrado, añadir nuevos si surgen), corre `/release` sólo si toca taggear.

## Plantilla de cierre de sesión

Al final de la sesión, deja un comentario al usuario con esta forma:

```
Sesión cerrada — foco: <f0|tipos|fase3>

Items cerrados:
- <ID> · <título> — PR #<n>, commit <hash>, doc <ruta>

Items abiertos heredados a próxima sesión:
- <ID> · <razón por la que no cerró>

Próximo /next-session sugerido: <auto|f0|tipos|fase3> (motivo en una línea)
```

Esto deja al siguiente Claude (o al humano del lunes) un puntero limpio sin tener que reconstruir contexto.

---

**Razón de existir de este comando:** los archivos `release.md` y `code-reviewer.md` cubren el cierre de un PR y el cierre de una release. Faltaba el otro extremo: el arranque de sesión. Sin él, cada nueva sesión empieza con "déjame mirar el estado", lee TASKS, lee análisis, propone tres cosas distintas y termina haciendo media. Este comando ancla el arranque a una decisión binaria (`f0|tipos|fase3`) y al estado real del repo, no al recuerdo del último Claude.
