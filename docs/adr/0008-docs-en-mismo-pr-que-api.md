---
id: 0008
title: Documentación se modifica en el mismo PR que la API
status: accepted
date: 2026-05-10
deciders: jcsvwinston
related: []
supersedes: null
tags: [process, governance, docs]
---

# 0008 — Documentación se modifica en el mismo PR que la API

## Contexto

La auditoría inicial de Quark (`docs/ANALISIS_MADUREZ.md`) detectó incoherencia profunda entre código y docs:

- `RELEASE_NOTES_V1.md` anuncia v1.0.0 production-ready.
- `CHANGELOG.md` está en 0.1.1.
- `SECURITY.md` dice "pre-1.0".
- `README` enlaza `examples/blog-api/` que no existe.
- `examples/README.md` referencia paths del monorepo previo (`pkg/quark/...`).

La causa raíz no es individual — es estructural: durante meses los docs vivieron en un repo separado (`quark-docs`) y la disciplina de mantenerlos sincronizados con la API dependía de la memoria humana del autor. Falló previsiblemente.

Tras unificar docs y código en un solo repo (`quark/website/`), persiste el riesgo si no se establece la regla.

## Decisión

**Cualquier PR que modifica API pública DEBE incluir, en el mismo PR:**

1. La actualización correspondiente en `website/docs/`.
2. La entrada en `CHANGELOG.md` bajo `Unreleased`.
3. Si es BREAKING, una nota provisional para el futuro `docs/MIGRATION_vX.Y.Z.md`.

PRs que tocan API sin incluir estos cambios **se rechazan automáticamente por el subagente `code-reviewer`** (`.claude/agents/code-reviewer.md`). No hay excepción "lo arreglo en otro PR" — esa lógica fue precisamente la que llevó al estado actual.

**API pública** = cualquier identificador exportado en el paquete raíz, en `cache/`, en `migrate/`, en `otel/`, en `cmd/quark/`, y los tags interpretables (`db:`, `pk:`, `rel:`, `m2m:`, `polymorphic:`, `quark:`, `nullable:`, `default:`, `join:`).

Lo que NO requiere actualizar docs:
- Refactoring interno (`internal/*`) sin cambio de API.
- Bugfixes que no cambian comportamiento documentado (anota en CHANGELOG bajo `### Fixed` pero no en website).
- Tests nuevos.
- Cambios de tooling de CI/build.
- Tipos.

## Consecuencias

**Positivas:**
- La doc no se desincroniza por inacción.
- Code review unificado: el reviewer ve API y doc en el mismo diff.
- Releases triviales de cerrar — la doc ya está al día por construcción.
- Disciplina culturalmente reforzada: "docs are not a side concern".

**Negativas:**
- PRs ligeramente más grandes y más lentos.
- Fricción al iterar cuando se está experimentando con una API que aún no se quiere documentar (mitigable: usa branch + abre PR cuando estés listo, no antes).
- Requiere que `website/docs/` esté maduro estructuralmente (sidebars existen para los nuevos contenidos). Si vas a abrir un área nueva, abre antes el sidebar y las páginas placeholder.

## Cómo se enforza

1. **Manual / cultural**: este ADR. Mencionado en `CONTRIBUTING.md` y `CLAUDE.md`.
2. **Automático parcial**: el subagente `code-reviewer` (`.claude/agents/code-reviewer.md`) lo verifica antes de aprobar PR.
3. **Automático CI**: el linter de docs (TASKS F0-10) puede comparar `git diff` de API vs `git diff` de `website/docs/` y fallar si hay desbalance significativo.
4. **Verificación en `/release`**: el slash command `.claude/commands/release.md` verifica antes de taggear que ningún archivo público está sin doc.

## Excepciones

- **Pre-release breaking changes en branch experimental**: aceptable abrir PRs de feature sin doc al branch `experimental/*`. La regla aplica a `main` y a branches de release.
- **Documentación tipográficamente trivial** (typo, formato): obviamente no requiere bump de API.

## Cuándo reabrir

Si el coste del PR-doble se vuelve insostenible (proyecto crece a varios committers que se pisan), considerar pipelines más sofisticados (auto-generación de doc desde `go doc`, linters que sugieren stub de doc al detectar API nueva). Por ahora la fricción es manejable y deseable.
