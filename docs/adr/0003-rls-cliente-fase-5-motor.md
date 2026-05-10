---
id: 0003
title: RLS hoy es WHERE-injection cliente; motor real en Fase 5
status: accepted
date: 2026-05-10
deciders: jcsvwinston
related: [0007]
supersedes: null
tags: [security, multi-tenancy, postgres]
---

# 0003 — RLS hoy es WHERE-injection cliente; motor real en Fase 5

## Contexto

"Row-Level Security" (RLS) tiene dos significados que se confunden a propósito en marketing de ORMs:

1. **RLS de motor**: el SGBD (típicamente Postgres con `CREATE POLICY` y `SET LOCAL app.tenant_id = ...`) aplica el filtro a NIVEL DEL EJECUTOR DE QUERY. Cualquier query, incluida `client.Raw()` o `Exec`, queda filtrada. Es defensa real.
2. **RLS de cliente**: el ORM inyecta `WHERE tenant_id = ?` en cada query que construye. Si el usuario evita el ORM (raw SQL, otra herramienta, otro pod), el filtro no existe. Es disciplina, no aislamiento.

`tenant_router.go` hoy implementa la opción (2): la estrategia `RowLevelSecurity` añade una condición a `q.where` en `client.go:170-181`. El comentario admite que es "client-side WHERE injection".

Adicionalmente se ha detectado un bug (P0-1 en `TASKS.md`): `Or()` no propaga `tenantID/tenantCol`, lo que rompe la inyección incluso cuando se usa el ORM correctamente.

## Decisión

**Reconocer públicamente que la estrategia `RowLevelSecurity` actual es WHERE-injection cliente, no RLS de motor.**

El soporte de RLS de motor real (Postgres `CREATE POLICY` + `SET LOCAL app.tenant_id`) llega en **Fase 5** del plan estratégico (`docs/ANALISIS_MADUREZ.md` §4). Ese motor real será **complementario**, no sustituto: los usuarios podrán seguir usando WHERE-injection donde RLS-de-motor no aplique (SQLite, MySQL sin policies).

Hasta Fase 5:

- `tenant_router.go` mantiene el WHERE-injection.
- La documentación pública (`website/docs/multi-tenancy/`) explica claramente la limitación.
- El bug P0-1 (`Or()`) se arregla en Fase 0 — no es aceptable que la disciplina de cliente tenga huecos.
- En `client.Raw()` y `Exec`, **se loguea WARN** cuando el contexto contiene tenantID, recordando al desarrollador que esa ruta no inyecta filtro.

En Fase 5 se añade:
- Nueva estrategia `RowLevelSecurityNative` que usa `SET LOCAL app.tenant_id = $1` por transacción y delega en políticas SQL del motor.
- Generador de policies: `quark tenant install-rls-policies` que emite el SQL templates por modelo.
- La estrategia `RowLevelSecurity` actual queda renombrada a `RowLevelSecurityClient` con deprecation warning para la siguiente versión major.

## Consecuencias

**Positivas (decisión actual):**
- Cero dependencia de capacidades del motor para multi-tenancy básica.
- Funciona en SQLite y MySQL (donde policies no son nativas).
- Implementación simple, fácil de debuggear.

**Negativas (decisión actual):**
- No es defensa real: `client.Raw()` se salta el filtro.
- Bugs en la propagación (Or, subqueries, tx) son de seguridad.
- "Multi-tenancy" en marketing público debe ir cualificado.

**Coste futuro:**
- Fase 5 introduce dependencia de Postgres-específicos para la modalidad nativa.
- Migración de usuarios existentes de `RowLevelSecurity` → `RowLevelSecurityClient` requiere paso explícito (no auto-rename).

## Alternativas consideradas

1. **Quitar la estrategia RLS hasta tener la nativa.** Rechazado: los usuarios la necesitan ya, aunque limitada; eliminarla rompe casos de uso reales.
2. **Hacer RLS nativa desde v0.x.** Rechazado: alcance demasiado grande para Fase 0–1; arrastra dependencia de Postgres en motores donde no aplica.
3. **Implementar RLS-cliente en una capa middleware en lugar de en el query builder.** Rechazado: sigue siendo disciplina; no resuelve el problema de seguridad.

## Lo que esta decisión NO permite

- **NO** publicitar `RowLevelSecurity` como "Row-Level Security" sin cualificar. Use "tenant scoping" o "client-side row filtering".
- **NO** prometer aislamiento contra desarrolladores que escriben `client.Raw()`.
- **NO** asumir que la estrategia previene fugas en queries con `Or()`, subqueries, joins o RawQuery hasta que el bug P0-1 esté cerrado y haya cobertura de tests.

## Cuándo reabrir

Cuando Fase 5 se entregue, este ADR se actualiza a `superseded` y un ADR sucesor describe la nueva arquitectura con dos modalidades (`Client` + `Native`).
