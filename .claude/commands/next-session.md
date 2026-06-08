---
description: Arranca una sesión de Quark con foco derivado del estado real del repo. NO lleva el roadmap hardcoded — lee TASKS.md y CHANGELOG.md para decidir el bloque actual. Sustituye al "exploro a ver qué hay".
argument-hint: [foco opcional: v1.2 | doc-sync | auto]
---

# /next-session $ARGUMENTS

Estás arrancando una sesión sobre Quark. **No asumas en qué fase estamos** —
las fases cierran rápido y este comando ha quedado obsoleto antes. Lee el
estado vivo antes de proponer trabajo.

> **Lectura obligatoria antes de cualquier cosa:** `CLAUDE.md` (Reglas duras +
> Regla de release), `TASKS.md` (header del archivo: trae la verdad sobre qué
> fase está abierta y qué items siguen pendientes), `CHANGELOG.md` (últimas
> 3-4 versiones para conocer la superficie ya entregada).
>
> Si `TASKS.md ## Bugs P0` tiene items vivos, **abandona este flujo y trabaja
> un P0 primero** — esa regla manda sobre cualquier foco.

## Paso 0 — Auditoría de estado (siempre)

Antes de elegir foco, ejecuta y resume al usuario en 5 líneas:

```bash
# Versión actual (mayor.menor.patch del último tag)
git describe --tags --abbrev=0

# Última fase cerrada / fase abierta — del header de TASKS.md
sed -n '1,80p' TASKS.md

# Focos admitidos hoy (los obsoletos están listados como "ya no aplican")
grep -A2 'Foco admitido' TASKS.md | head -5

# Lista F-N abiertos de la fase actual (no tachados con ~~)
grep -E '^### F[0-9]+-[0-9a-z]+ ·' TASKS.md | grep -v '~~' | head -20

# Bugs P0 vivos
sed -n '/## Bugs P0/,/^## /p' TASKS.md | head -40

# Última PR mergeada en main
git log --oneline -5
```

El resultado de este paso reemplaza cualquier asunción que traigas. Lo que diga
el header de `TASKS.md` (fase abierta o cerrada, hallazgos de bug-bash activos,
deferrals a v1.2+) es el menú — no lo que recuerdes de la última sesión.

## Paso 1 — Elegir foco

`$ARGUMENTS` admite los focos que **TASKS.md declara vivos hoy**. A fecha
de este comando los habituales son:

| Foco       | Trabaja en                                                                    | Cuándo elegirlo                                                       |
| ---------- | ----------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| `v1.2`     | Deferrals del próximo minor (sharding scatter-gather, cache-stampede cross-instancia, F6-3b binder) + hallazgos de bug-bash activos | Cuando arrancas feature work post-v1.1 |
| `doc-sync` | Pasada de saneamiento documental: invoca `/doc-sync` (consume `docs-auditor`) | Cuando se hayan acumulado cambios sin reflejar en `website/docs/`     |
| `auto`     | Audita TASKS.md, propone foco, espera confirmación                            | Default cuando no se pasa argumento                                   |

Si `TASKS.md` declara focos que no aparecen aquí (por ejemplo apertura de una
fase nueva post-v1.0), **respeta lo que dice TASKS.md** — este comando es un
índice, no la autoridad.

## Paso 2 — Reglas que aplican en cualquier foco

1. **Lee el playbook del módulo donde vayas a tocar** antes de cualquier
   edit (`docs/playbooks/{query-builder,dialects,migrations,tenant,cache,security}.md`).
   Cita línea concreta antes de proponer el cambio.
2. **Subagente `code-reviewer` obligatorio antes de cerrar PR**
   (`.claude/agents/code-reviewer.md`). Aprobación bloqueante. Si el PR
   toca API pública y no toca `website/docs/`, el reviewer **bloquea** vía
   `docs-auditor`.
3. **Conventional Commits + docs sincronizadas en el mismo PR** (ADR-0008).
4. **Cero lenguaje de marketing.** Antes de PR: `grep -rn "production-ready\|enterprise-grade\|battle-tested" .` debe estar vacío.
5. **Si el cambio toca SQL: los 6 motores verdes** (PG/MySQL/MariaDB/MSSQL/Oracle + SQLite). Oracle corre en la matriz `integration` bloqueante (vía `docker run gvenzl/oracle-free`, no testcontainers).
6. **Si la sesión cierra una Fase o entrega un F-N**, actualiza `TASKS.md` (tachar item cerrado con `~~F-N · ...~~` + bloque "**Cerrado** — descripción + PR/commit + doc"), y si toca taggear usa `/release vX.Y.Z`.

## Paso 3 — Foco específico: `v1.2`

Fase 6 (codegen + HA + sharding + benchmarks) está **cerrada** (entregada en
v1.0.0); v1.1.0 fue el release de hardening (bug-bash F0-F14 completo). El
trabajo del próximo minor son los **deferrals** documentados en
`docs/ROADMAP.md` (§ v1.2+) y `TASKS.md`. No los dupliques aquí; el menú vive
ahí.

Deferrals vivos:
- **Sharding**: scatter-gather + shard-key-from-entity (follow-up de F6-7).
- **Caché**: coordinación de stampede cross-instancia (hoy in-process, ADR-0011).
- **F6-3b** (UPDATE/partial/batch binder codegen) — **diferido** por payoff ~1%
  (ADR-0017); abrir sólo si type-safety lo motiva, **no por velocidad**: el gate
  ADR-0002 ≥3× p99 está **retirado** (el cuello no es reflect sino allocs
  arquitectónicos + round-trip driver; ver `benchmarks/PROFILING.md`).

Antes de feature nueva: si `TASKS.md` § "Bug-bash hallazgos" tiene un hallazgo
**activo** (p.ej. BB-14), tiene prioridad — ciérralo o avánzalo primero.

Cada item es 1 PR con `code-reviewer` + docs + CHANGELOG; los que abren ADR
escriben el ADR en el mismo PR.

## Paso 4 — Foco específico: `doc-sync`

Invoca `/doc-sync` (definido en `.claude/commands/doc-sync.md`). El comando
ejecuta `docs-auditor` en modo `--report` primero (para que veas los gaps),
luego aplica `--fix` sólo a los arreglos triviales (versión actual, lista de
capabilities entregadas, snapshot de release-notes). Los gaps que requieren
decisión humana se listan en chat para que tú decidas (ej. desdoblar fila
multi-tenant del comparison).

Una sesión `doc-sync` no entrega feature — entrega coherencia. Si la sesión
está a punto de marcar un item como cerrado, primero invoca `/doc-sync` para no
dejar el item entregado con docs desalineadas.

## Plantilla de cierre de sesión

Al final de la sesión, deja un comentario al usuario con esta forma:

```
Sesión cerrada — foco: <v1.2|doc-sync|auto>

Items cerrados:
- <ID> · <título> — PR #<n>, commit <hash>, doc <ruta>

Items abiertos heredados a próxima sesión:
- <ID> · <razón por la que no cerró>

Gaps de doc detectados por docs-auditor (si los hay):
- <gap> · <acción sugerida>

Próximo /next-session sugerido: <auto|v1.2|doc-sync> (motivo en una línea)
```

Esto deja al siguiente Claude (o al humano del lunes) un puntero limpio sin
tener que reconstruir contexto.

---

**Razón de existir de este comando:** los archivos `release.md`,
`code-reviewer.md` y `docs-auditor.md` cubren el cierre de un PR, el cierre
de una release y la auditoría de coherencia. Faltaba el otro extremo: el
arranque de sesión. Sin él, cada nueva sesión empieza con "déjame mirar el
estado", lee TASKS, lee análisis, propone tres cosas distintas y termina
haciendo media. Este comando ancla el arranque a una decisión binaria
(`v1.2|doc-sync`) y al estado vivo del repo, no al recuerdo del último
Claude ni a un roadmap hardcoded en el frontmatter.

**Nota histórica:** versiones anteriores de este comando llevaban los focos
`f0`, `tipos`, `fase3`, `fase4`, `fase5` hardcoded en frontmatter. Cuando
esas fases cerraron, el comando se volvió desinformación. La regla nueva:
**el comando lee TASKS.md y deriva el menú**; no lo lleva escrito. Si
cambia el roadmap, basta editar el `### Foco específico` correspondiente y
mover la entrada de la tabla del Paso 1.
