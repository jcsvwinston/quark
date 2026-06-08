---
description: Pasada de saneamiento documental sobre toda la docs pública. Invoca al subagente `docs-auditor` en modo `--scope=full`, primero `--report` (para que veas los gaps) y, si confirmas, después `--fix` para los arreglos triviales. Las decisiones humanas las deja listadas en chat. Es el contrapeso al "entregamos más rápido de lo que actualizamos el índice".
argument-hint: [--report-only | --fix]
---

# /doc-sync $ARGUMENTS

Ejecutas una pasada de coherencia entre el código real de Quark y la documentación pública. **No es trabajo de feature**: es trabajo de mantenimiento que, si no se hace periódicamente, mete a la siguiente sesión en una auditoría grande estilo la que motivó este flujo.

> **Antes de empezar:** confirma que estás en `main` limpio (`git status` + `git log --oneline -1`). Si hay cambios sin commitear que tocan `.md`/`.mdx`, párate y aclara qué pretendes — `/doc-sync` no debe pisar trabajo en vuelo.

## Modo de uso

`$ARGUMENTS` admite:

- vacío (default) — pase en dos fases: report → confirma humano → fix.
- `--report-only` — sólo informa, no toca archivos. Útil para auditorías periódicas.
- `--fix` — salta el confirm y aplica auto-fixes triviales directamente. Sólo cuando ya conoces los gaps (segunda invocación tras un `--report-only`).

## Paso 1 — Invocar `docs-auditor` en modo report

Lanza al subagente `docs-auditor` con `--scope=full --report`. El agente recorre los 11 checks (A-K) definidos en `.claude/agents/docs-auditor.md` y devuelve un informe estructurado con tres secciones:

1. **DRIFT** (bloqueante).
2. **WARN** (recomendado).
3. **Decisiones humanas pendientes** (no auto-fixables).

Resume al usuario el informe **literal** del agente, sin interpretarlo. No filtres severidad — el humano decide qué priorizar.

## Paso 2 — Confirmar con el usuario qué se aplica

Pregunta explícitamente:

1. ¿Aplico auto-fixes triviales (versión actual, snapshot de release-notes, tabla SECURITY)? (sí/no)
2. ¿Procedo con las decisiones humanas que requieren reescritura? (lista por separado; cada una es un sí/no individual o un "hazlo tú con esta indicación").
3. ¿Algún gap reportado que prefieras dejar abierto como issue en GitHub en lugar de cerrar aquí?

**No asumas autorización implícita por el invocador.** Si `$ARGUMENTS == --fix` se salta esta pregunta para los auto-fixes triviales, pero las decisiones humanas siempre necesitan confirmación individual.

## Paso 3 — Aplicar auto-fixes confirmados

Lanza `docs-auditor` en modo `--fix --scope=full`. El agente aplica:

- Versión declarada coherente entre `README.md`, `SECURITY.md`, `intro.mdx`, `release-notes.mdx`, `ROADMAP.md` (todas a la versión del último tag).
- Secciones nuevas en `release-notes.mdx` para tags que no aparecen (sacando el resumen de `docs/RELEASE_NOTES_v*.md`).
- Tabla "Supported Versions" en `SECURITY.md` bumpeada.
- "Known Current Boundaries" de `roadmap.mdx` cuando una boundary ya no aplica (ej. `cmd/quark` ahora existe).

Cualquier auto-fix que el agente quiera aplicar y no caiga en estos buckets, lo eleva a "decisión humana" — no lo hace por su cuenta.

## Paso 4 — Atajar decisiones humanas confirmadas

Para cada decisión humana confirmada por el usuario:

1. Lee la página tocada entera (no por offset — necesitas contexto).
2. Edita con `Edit` (no `Write`) cuando posible, para no perder formato/comentarios.
3. Si la decisión añade contenido nuevo (ej. añadir sección "Stampede protection" a `caching-observability.mdx`), incluye:
   - Ejemplo de uso real (no pseudocódigo).
   - Link al ADR correspondiente si existe (ej. ADR-0011 para stampede).
   - Mención de la versión donde llegó la feature.
4. Tras cada edit, verifica que la página sigue compilando (vista rápida del mdx; sintaxis Docusaurus admonitions / sidebar_position correcto).

## Paso 5 — Verificación final

Lanza otro `docs-auditor --report --scope=full` para confirmar que los DRIFT bloqueantes desaparecieron. Si quedan, repite Paso 4 o eleva como issue.

```bash
# Si hay decisiones humanas que se quedan abiertas:
gh issue create \
  --title "docs: <descripción del gap>" \
  --label "docs,maintenance" \
  --body "Detectado por /doc-sync el $(date +%Y-%m-%d). <Descripción detallada del gap y la decisión pendiente>."
```

## Paso 6 — Commit y PR

```bash
git checkout -b chore/doc-sync-$(date +%Y%m%d)
git add -A
git status   # revisa antes
git commit -m "docs: pasada de saneamiento (docs-auditor)

<resumen de checks A-K y auto-fixes aplicados>
<lista de decisiones humanas atendidas>
<issues abiertos para decisiones pendientes>

Closes: <issues si aplica>"
git push -u origin HEAD
gh pr create --title "docs: pasada de saneamiento" --label docs
```

PR pasa por:

- CI verde (incluyendo build del sitio).
- Aprobación del subagente `code-reviewer` (que delegará a `docs-auditor` y debería aprobar — éste es justo el caso en que docs y código se alinean).

## Cuándo ejecutar `/doc-sync` proactivamente

- **Tras cerrar una Fase** (ej. Fase 6 → v1.0): obligatorio.
- **Tras una release minor** (`v0.X.0`): recomendado dentro de la misma semana.
- **Antes de abrir un PR que cierra un F-N grande**: para no entregar el F-N con docs desalineadas.
- **Cuando el lector externo (issue, discusión) detecta una contradicción**: el incidente justifica una pasada completa, no sólo el fix puntual.

## Anti-patterns

- **No pasar por `/doc-sync` directo sin `--report-only` previo** la primera vez en una sesión. Querrás ver qué propone antes de autorizar.
- **No agrupar saneamiento documental con cambio de feature en el mismo PR.** Confunde la review. Saneamiento es PR propio, con tipo `docs:` en el commit.
- **No reescribir `versioned_docs/version-X.Y.Z/`** salvo error explícito — son snapshots inmutables.
- **No retirar caveats honestos** (fire-and-forget, in-process, deferrals a v1.2+/v2.0) aunque sean repetitivos. La regla anti-marketing del CLAUDE.md es **incondicional** — no se levanta porque v1.0/v1.1 estén liberadas.

---

**Razón de existir de este comando:** el flujo de `/release` cubre el deploy de la versión, pero asume que la doc del sitio ya está alineada en el momento del tag. La realidad es que el alineamiento se descalibra durante el desarrollo entre release y release. `/doc-sync` es la pasada periódica que recalibra. Ejecutado cada 2-3 semanas (o tras cada Fase), evita que las auditorías documentales se conviertan en sesiones de un día entero como las que motivaron este flujo.

**Apoyado por:** el subagente `docs-auditor` (lógica de detección) y el subagente `code-reviewer` (que delega a `docs-auditor` por PR — el otro vector de prevención).
