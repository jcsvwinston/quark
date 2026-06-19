---
id: 0021
title: Shard key desde la entidad vía la interfaz ShardKeyer (helper caller-side, no un hook del router)
status: accepted
date: 2026-06-19
implemented: v1.2 (ShardKeyer + WithShardKeyOf)
deciders: jcsvwinston
related: [0016, 0001]
supersedes: null
tags: [architecture, sharding, scaling, phase-6-followup]
---

# 0021 — Shard key desde la entidad (`ShardKeyer`)

## Contexto

[ADR-0016](0016-sharding-shardrouter.md) fijó que la shard key llega **por
contexto** (`WithShardKey`), uniforme para lecturas y escrituras, y dejó como
follow-up explícito (§"Cuándo reabrir": *"Extracción de shard key desde la
entidad para escrituras"*; §Alternativas #1) extraerla de la propia entidad.

La tensión es arquitectónica: el routing ocurre en `ShardRouter.GetClient(ctx)`,
que `For[T]` invoca **antes** de `.Create(entity)` — el `ClientProvider` nunca
ve la entidad. Y repetir `WithShardKey(ctx, user.TenantID)` en cada call site
duplica el conocimiento de *cuál* es el campo de partición y deja que un call
site enrute por el campo equivocado.

## Decisión

- **Interfaz `ShardKeyer { ShardKey() string }`.** El modelo declara su propia
  shard key. Es el "hook de modelo" que 0016 anticipó, expresado como interfaz
  idiomática (estilo `fmt.Stringer`), **sin reflect** (regla 5) y compile-time
  safe.
- **Helper `WithShardKeyOf(ctx, entity ShardKeyer)`** = `WithShardKey(ctx,
  entity.ShardKey())`. Es **caller-side**: popula el contexto desde la entidad
  antes de `For[T]`. El contexto sigue siendo el mecanismo uniforme de routing
  (0016); esto sólo lo rellena desde la entidad.
- **El router NO cambia y sigue sin saber de sharding** (0016: *"el resto del
  ORM no sabe que hay sharding"*). La extracción es del caller, no un hook
  interno del provider ni de `Create`.
- `ShardKey() == ""` → el routing **falla** como una key ausente (no fan-out
  silencioso), coherente con 0016.

## Alternativas consideradas

1. **Struct tag `shardkey:"…"` + reflect.** Rechazado: introduce parsing de tag
   nuevo + reflect en el path de escritura cuando una interfaz lo resuelve en
   tiempo de compilación. Sería más "declarativo" a costa de más reflect (deuda
   conocida, §1.1 de ANALISIS_MADUREZ) y una gramática de tag adicional.
2. **Auto-extraer en `Create`** (el ORM lee la entidad y re-enruta). Rechazado:
   viola 0016. El `*Client` se resuelve en `GetClient(ctx)` antes de ver la
   entidad; acoplar `Create` al `ShardRouter` para re-enrutar rompe el modelo
   `ClientProvider` y no aplica a lecturas (`List`/`Find` no llevan entidad).
3. **Validar en `Create` que `entity.ShardKey()` casa con el shard resuelto.**
   Rechazado por el mismo acoplamiento (el ORM tendría que conocer el router).
   La disciplina queda del caller: usar `WithShardKeyOf` con la misma entidad
   que se crea.

## Consecuencias

**Positivas:**
- El campo de partición vive en un único sitio (el método del modelo): menos
  repetición y menos riesgo de enrutar por el campo equivocado.
- Cero reflect; superficie de API mínima (una interfaz + un helper).
- El router permanece agnóstico de sharding; `For[T]` no cambia.

**Negativas:**
- Sigue siendo del lado del caller: olvidar `WithShardKeyOf` (o usar
  `WithShardKey` con un valor distinto al de la entidad) no se detecta — 0016
  prefiere *fallo-al-faltar* sobre magia.
- Sólo aplica a escrituras con entidad a mano; las lecturas siguen con
  `WithShardKey` (no hay entidad de la que extraer).

## Cuándo reabrir

- Demanda de validación en `Create` de key↔shard (requeriría que el ORM conozca
  el router → reabrir 0016, no este ADR).
- Scatter-gather / lecturas cross-shard es un follow-up **separado** con su
  propio ADR (semántica de merge/orden/límite no trivial; ver 0016 §"Cuándo
  reabrir").
