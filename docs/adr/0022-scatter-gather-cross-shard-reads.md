---
id: 0022
title: Scatter-gather cross-shard reads vía funcs explícitas (merge caller-side, agregados no-COUNT diferidos)
status: accepted
date: 2026-06-19
implemented: v1.2 (ScatterGather + ScatterCount + ScatterMerge)
deciders: jcsvwinston
related: [0016, 0021]
supersedes: null
tags: [architecture, sharding, scaling, phase-6-followup]
---

# 0022 — Scatter-gather cross-shard reads

## Contexto

[ADR-0016](0016-sharding-shardrouter.md) fijó sharding por shard key explícita y
dejó scatter-gather (leer **todos** los shards y fusionar) como follow-up
explícito: *"una capacidad deliberada y separada → follow-up… su semántica
(orden, merge, `LIMIT` global, agregados) es no trivial y no debe colarse por
accidente al olvidar la key"*. Su §Alternativas #2 rechazó el fan-out implícito
al faltar la key, y §"Cuándo reabrir" lista *"Demanda de scatter-gather /
lecturas cross-shard (diseñar merge + límites)"*.

## Decisión

- **Funcs genéricas explícitas, separadas de `For[T]`:** `ScatterGather[T]`
  (filas) y `ScatterCount[T]` (suma). NO se activan por una query normal — un
  `For[T]` sin shard key sigue fallando (0016). Son funcs libres (Go no permite
  type params en métodos), como `For[T]`.
- **Fan-out concurrente, read-only.** Una goroutine por shard; cada una corre la
  MISMA query (closure `build`) sobre el `*Client` de su shard. Sin escrituras
  ni transacciones cross-shard (0016) — una `Tx` sigue ligada a un solo shard.
- **Merge caller-side vía `ScatterMerge[T]{Less, Limit}`.** El comparador lo
  suministra el caller; **no** se introspecciona el `OrderBy` del `Query` (sería
  frágil ante cambios del AST). Sin `Less` → concatena en orden de nombre de
  shard. `Limit > 0` → top-N global tras ordenar. Para un top-N correcto el
  caller pone `Limit(N)` por-shard en `build` **y** `Limit: N` global.
  Implementación: concat + `sort.SliceStable` + truncado (materializa en
  memoria).
- **Cualquier error de shard → error.** Un resultado parcial sobre shards
  disjuntos es incompleto; se reporta en vez de truncar en silencio.
- **Agregados acotados:** `COUNT` se suma (shards disjuntos → suma exacta).
  `AVG/MIN/MAX` y `GROUP BY` cross-shard **no** se fusionan (semántica no
  trivial: `AVG` necesita SUM/COUNT, `GROUP BY` necesita merge de grupos) →
  diferidos; el caller los corre por shard.

## Alternativas consideradas

1. **Fan-out implícito al faltar la shard key.** Rechazado (0016 §Alt #2):
   sorpresivo y caro; debe ser opt-in explícito.
2. **Introspeccionar el `OrderBy`/`Limit` del `Query` para auto-fusionar.**
   Rechazado: acopla scatter al interno del builder (frágil); el comparador
   explícito es robusto y type-safe.
3. **Functional options (`WithScatterOrder`/`WithScatterLimit`).** Rechazado:
   `WithScatterLimit(n)` no puede inferir `T` (no lo lleva en ningún argumento) →
   obligaría a `WithScatterLimit[T](n)` en cada call site. El struct
   `ScatterMerge[T]` con campos nombrados es más legible y `T` se infiere de
   `build`.
4. **Streaming k-way merge** (heap sobre iteradores por shard). Diferido: el MVP
   materializa; optimización de memoria para más adelante si un workload la pide.
5. **`errgroup` con cancelación al primer error.** Diferido: evita añadir
   dependencia; el `WaitGroup` manual corre todo y reporta el primer error en
   orden de nombre de shard.

## Consecuencias

**Positivas:**
- Lectura cross-shard explícita y type-safe; top-N global correcto con el
  comparador del caller; `COUNT` exacto sobre shards disjuntos.
- Cero acoplamiento al builder. El default sigue siendo "sin cross-shard"
  (0016 intacto): el fan-out nunca ocurre por accidente.

**Negativas:**
- El caller suministra el comparador y los límites (por-shard + global):
  disciplina, no magia.
- Materializa los resultados en memoria (O(shards × per-shard-limit)).
- Agregados no-`COUNT` y `GROUP BY` no soportados cross-shard (correrlos por
  shard). En error se desperdicia el trabajo de los demás shards.

## Cuándo reabrir

- Demanda de agregados cross-shard (`AVG`/`MIN`/`MAX`) o `GROUP BY` → diseñar el
  merge de agregados.
- Memoria: si un workload materializa demasiado → streaming k-way merge.
- Cancelación temprana en error (`errgroup`) si el coste del trabajo
  desperdiciado importa.
