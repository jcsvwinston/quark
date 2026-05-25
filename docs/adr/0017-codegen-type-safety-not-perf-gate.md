---
id: 0017
title: Codegen se justifica por type-safety; se retira el gate de performance ≥3× p99 de ADR-0002
status: accepted
date: 2026-05-25
deciders: jcsvwinston
related: [0002, 0014]
supersedes: "0002 (partial — only the ≥3× p99 gate clause + the codegen-as-performance framing)"
tags: [architecture, performance, codegen, phase-6, v1.0]
---

# 0017 — Codegen es type-safety, no velocidad: se retira el gate ≥3× p99

> **Alcance de la supersesión.** Este ADR **deja sin efecto** dos cosas de
> [ADR-0002](0002-reflect-default-codegen-fase-6.md): (1) la cláusula de
> §Restricciones *"los benchmarks deben demostrar mejora de ≥3× en latencia
> p99 para justificar el paso"* a codegen, y (2) el encuadre de codegen como
> una mejora de **performance**. **El resto de ADR-0002 sigue vigente**:
> reflect por defecto y permanente, codegen opt-in, misma API pública. El
> mecanismo de coexistencia ([ADR-0014](0014-codegen-coexistence-typed-registry.md))
> tampoco se toca.

## Contexto

ADR-0002 fijó codegen como opt-in en Fase 6 y, en §Restricciones, le impuso un
**gate**: los benchmarks debían demostrar **≥3× en latencia p99** para
justificar el esfuerzo. En §Cuándo reabrir, ese mismo ADR previó:

> *"Si tras Fase 4 los benchmarks muestran que reflect ya no es el cuello de
> botella (porque el cuello es de I/O o cache), reevaluar prioridad de Fase 6."*

Fase 6 entregó tanto el codegen (F6-1 skeleton, F6-2 typed scanners, F6-3a
INSERT binder, F6-4 accesores tipados) como los benchmarks honestos (F6-8a
baseline + el profiling de seguimiento). **Tres data points medidos** responden
directamente la pregunta de §Cuándo reabrir:

- **F6-8a (baseline):** el path reflect de Quark corre **~1.5–2.1×** sobre el
  suelo de `database/sql` puro. Ese margen es el techo total que cualquier
  optimización puede recuperar — y ya es < 3×.
- **F6-2 (scan codegen):** Find ~2 %, List(200) ~4–5 %, mismos allocs.
- **F6-3a (insert binder codegen):** Create ~1 % (−6 allocs/op: 89 vs 95).

El profiling (`benchmarks/PROFILING.md`) explica **por qué** el gate no se
alcanza por codegen de scan/bind:

1. **La CPU no vive en reflect.** En `ListWhere`, ~67 % es el motor SQLite +
   syscalls (mmap) y ~52 % cum es `database/sql.(*Rows).Next/.Close`. El
   `scanRow` reflectivo de Quark **no aparece en el top-25 de nodos de CPU**.
   Quitar reflect no mueve CPU que reflect no consume.
2. **El sobrecoste vs raw es de allocations, y son arquitectónicas, no
   reflectivas.** Read: result collection (`List.func1`) ~36 %, `scanRow`
   `[]any`/boxing ~14 %, `clone` del builder inmutable ~7 %, construcción de
   query ~10 %. Write: `For[T]` ~19 %, `saveAny` ~19 %, `buildInsert` ~12 %,
   `rowToMap` ~9 %. El codegen toca una fracción menor de esto y **ni siquiera
   elimina esos allocs** (el scanner generado sigue alocando el `[]any` y
   boxeando cada campo).

Esto cumple **exactamente** la condición de reapertura de ADR-0002. La decisión
quedó pendiente del mantenedor (registrada así en `TASKS.md` y en
`PROFILING.md` §Recommendation) y **bloquea el tag v1.0**: `docs/ROADMAP.md`
afirmaba *"v1.0 ships only when F6-8 proves the ≥3× p99 codegen gate from
ADR-0002"*. Con los cuatro pilares de Fase 6 entregados (codegen, HA, sharding,
benchmarks), el único obstáculo a v1.0 es esta decisión.

## Decisión

1. **Se retira el gate ≥3× p99** como condición de v1.0 y como justificación de
   codegen. No es alcanzable con el diseño actual por codegen de scan/bind, y el
   profiling demuestra que el coste no vive en reflect. Mantenerlo bloquearía
   v1.0 indefinidamente contra una métrica que mide lo que no es el cuello.

2. **El valor de codegen se reencuadra como type-safety en compile-time +
   forward-compat, NO velocidad.** F6-4 (accesores de columna tipados, ya
   entregado) materializa ese valor y es independiente de cualquier gate de
   perf. El mecanismo de F6-1/F6-2/F6-3a queda como foundation correcta y opt-in
   de **coste cero** para quien no lo usa (un map-lookup por op, confirmado
   despreciable).

3. **El gate de v1.0 pasa a ser el checklist honesto** de
   `docs/ANALISIS_MADUREZ.md` §3 (cobertura cross-engine, gaps estructurales
   cerrados), no una cifra de speedup.

4. **Dispositions de los items diferidos:**
   - **F6-3b** (UPDATE/partial/batch binder): permanece **diferido**. Se reabre
     sólo si type-safety o corrección lo motivan — **nunca por velocidad**
     (payoff ~1 % medido en F6-3a, riesgo de corrupción de escritura mayor).
   - **F6-8b** (ent + sqlc, comparación codegen-tier): se reencuadra como
     **informativo/opcional**, no gate. Su propósito original era alimentar la
     comparación del ≥3×; sin ese gate aporta señal comparativa pero **no
     bloquea v1.0**.

5. **Si en el futuro la performance por-operación importa, las palancas son
   reducción de allocs (independientes del codegen)**, no codegen: `rowToMap`
   lazy (ya hecho, commit `02ec8543`), clone copy-on-write (ya hecho, PR #107),
   buffers reusados en scan/bind. Aun así acotadas: contra una DB en red el
   round-trip del driver domina todo lo demás.

## Consecuencias

**Positivas:**

- v1.0 deja de estar bloqueado por una métrica inalcanzable. El camino a un v1.0
  honesto queda definido por gaps reales (ANALISIS_MADUREZ §3), no por un
  speedup que el diseño no puede entregar.
- Codegen gana una justificación honesta y verificable (type-safety), ya
  alineada con la doc pública existente: `website/docs/guides/codegen.mdx`
  describe el codegen como *"for type-safety and forward-compatibility, not for
  a dramatic speedup"* y reconoce que *"the speedup is small at this stage"*.
- El esfuerzo futuro se dirige a las palancas reales (allocs) si la perf
  importa, no a codegen-por-velocidad de payoff ~1 %.

**Negativas:**

- Quark **no promete** paridad de performance con sqlc/ent. Su codegen-tier
  sigue siendo más rápido en hot paths CPU-bound in-memory; Quark acepta ese
  margen a cambio de DX y API estable. Se documenta, no se esconde.
- F6-8b sin entregar deja la comparación codegen-tier sin números publicados;
  se asume como gap informativo documentado, no como deuda bloqueante.
- Revisar un gate aceptado puede leerse como "mover la portería". Mitigación: la
  decisión se apoya en datos medidos (3 data points + profiling) que son
  **exactamente** la condición de reapertura que ADR-0002 §Cuándo reabrir
  anticipó — no es arbitraria ni retroactiva.

## Alternativas consideradas

1. **Mantener el gate ≥3× y seguir invirtiendo en codegen-por-velocidad
   (F6-3b + optimizaciones de scan/bind).** Rechazado: el profiling muestra que
   el coste no vive en reflect; el techo de mejora por scan/bind codegen es
   ~1–5 %. Perseguir 3× por esa vía es imposible con el diseño actual y bloquea
   v1.0 *sine die*.
2. **Recalibrar el gate a un número alcanzable (p.ej. ≥1.2×).** Rechazado: un
   gate de speedup, sea cual sea el número, sigue encuadrando codegen como una
   feature de velocidad, que no es su valor. Mejor retirar el encuadre que
   recalibrarlo.
3. **Eliminar codegen de v1.0.** Rechazado: ya está entregado (F6-1/2/3a/4), es
   opt-in de coste cero, y su valor de type-safety (F6-4) es real e
   independiente. Quitarlo perdería ese valor por una razón (perf) que nunca fue
   su justificación honesta.
4. **Perseguir las palancas de allocs antes de taggear v1.0.** Rechazado como
   bloqueante: son independientes del codegen y del gate y pueden ir post-v1.0
   como mejora incremental. No deben retrasar el tag.

## Restricciones que esta decisión impone

- La doc pública **no** debe presentar codegen como una feature de performance
  ni publicar números de speedup como argumento de venta. `codegen.mdx` ya lo
  hace bien; mantenerlo así.
- Cualquier número de benchmark publicado (F6-8a, futura F6-8b) debe ser honesto
  y apples-to-apples; nada de cherry-picking para defender el gate retirado.
- El mecanismo codegen (registry + fallback, ADR-0014) sigue vigente como opt-in
  de coste cero. Esta decisión **no** lo toca.
- ADR-0002 sigue vigente en todo lo demás (reflect default permanente, codegen
  opt-in, misma API pública). Sólo quedan sin efecto su cláusula de gate ≥3× y
  su encuadre de codegen como mejora de performance.

## Cuándo reabrir

- Si surge demanda real de paridad de performance con ORMs codegen-tier **y** un
  rediseño (no codegen de scan/bind, sino reducción de allocs arquitectónicas o
  un fast-path que evite `database/sql`) muestra un techo de mejora sustancial
  → reabrir con un ADR sucesor que fije un objetivo de perf realista y medible.
- Si F6-8b se entrega y los números codegen-tier cambian la lectura (improbable
  según el profiling) → revisar.
- Si Go añade acceso estático a campos de struct sin reflect que cambie el
  cálculo coste/beneficio del codegen (cf. ADR-0014 §Cuándo reabrir).
