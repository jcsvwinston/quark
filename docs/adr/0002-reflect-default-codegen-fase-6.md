---
id: 0002
title: Reflect por defecto, codegen opt-in en Fase 6
status: accepted
date: 2026-05-10
deciders: jcsvwinston
related: [0001, 0014, 0017]
supersedes: null
tags: [architecture, performance, codegen, dx]
---

# 0002 — Reflect por defecto, codegen opt-in en Fase 6

## Contexto

Los ORMs Go se dividen en dos campos por su estrategia de mapeo struct ↔ SQL:

- **Reflect-based** (GORM, bun, Quark hoy): metadatos del modelo se calculan en runtime con `reflect`, se cachean por tipo. Pros: API sencilla, cero pasos de build, cualquier struct funciona. Contras: coste por fila/columna en hot paths, no hay seguridad de tipos en compile-time sobre las columnas.
- **Codegen-based** (sqlc, ent, sqlboiler, jet, Diesel en Rust): un comando `gen` produce código Go tipado a partir del esquema o de queries SQL escritas a mano. Pros: cero reflect en runtime, type-safe en compile-time (un typo en columna no compila), performance ≈ raw `database/sql`. Contras: paso extra de build, friction al iterar, structs generados son menos flexibles.

El núcleo actual de Quark (`scanRow`, `loadRelations`, `buildInsert`, `buildUpdate`, `saveAny`) es 100% reflect. Los generics en la firma (`Query[T]`, `Page[T]`) son superficiales; el T se erosiona vía `reflect.TypeOf(zero)` y se trabaja con `reflect.Value`.

Bifurcar Quark en dos APIs (reflect-mode y codegen-mode) sería un error: dobla la superficie a mantener y confunde al usuario sobre cuál usar.

## Decisión

**Reflect es el camino por defecto** y se mantiene como tal indefinidamente. Es el on-ramp idiomático.

**Codegen llegará en Fase 6** como camino **opt-in**, no como reemplazo:

- `quark gen models` emitirá, por cada modelo:
  - Scanner tipado sin reflect.
  - Insert/Update batch con bind manual.
  - Constructor de `Query[T]` con accesores tipados (`Where().Name().Eq("x")`).
- El usuario que use codegen sigue usando la **misma API pública** (`quark.For[User]`); el codegen instala implementaciones tipadas que reemplazan las reflect-paths internamente.
- El usuario que no use codegen sigue funcionando con reflect, sin penalización en DX.

Hasta Fase 6, **no se introduce reflect adicional en hot paths sin discusión previa** (issue + ADR sucesor si la decisión cambia).

## Consecuencias

**Positivas:**
- API estable: el usuario no tiene que elegir entre dos sabores hasta que necesite el upgrade.
- On-ramp suave: cualquier struct compila y funciona desde el primer día.
- Codegen llega cuando hay tracción y benchmarks que lo justifiquen.

**Negativas:**
- Performance hoy es 2–5× peor que sqlc/ent en hot paths (medido en cargas sintéticas; hace falta benchmark proper en Fase 6).
- No hay type-safety de columnas en compile-time hasta Fase 6.
- Reflect cache (`sync.Map` por tipo) consume memoria proporcional a número de modelos, aunque modesta.

## Alternativas consideradas

1. **Codegen como única estrategia desde v0.x.** Rechazado: añade fricción de build a un ORM en alpha que necesita adopción rápida.
2. **Híbrido API distinto en codegen (`quarkgen.For[User]`).** Rechazado: bifurca API. Los usuarios no deberían reescribir su código al activar codegen.
3. **Generics de Go full-blown sin reflect (estilo Diesel).** Rechazado: Go generics no soportan trait/method-level constraints suficientes para codificar el ORM de forma puramente estática; siempre se cae a reflect en algún punto.

## Restricciones que esta decisión impone

- Toda capa nueva debe ser **reflect-friendly** o tener un hook obvio para que el codegen la reemplace.
- Las firmas públicas no pueden depender de tipos generados por codegen (el usuario sin codegen debe poder importar todo).
- ~~Cuando llegue Fase 6, los benchmarks deben demostrar mejora de ≥3× en latencia p99 para justificar el paso.~~ **Superseded por [ADR-0017](0017-codegen-type-safety-not-perf-gate.md) (2026-05-25):** el gate ≥3× p99 se **retira**. Tres data points de Fase 6 (F6-8a baseline ~1.5–2.1×, scan codegen ~2–5 %, insert binder ~1 %) + profiling (`benchmarks/PROFILING.md`) demuestran que reflect no es el cuello de botella, así que el gate no es alcanzable por codegen de scan/bind. Codegen se justifica ahora por **type-safety** (F6-4), no por velocidad.

## Cuándo reabrir

Si tras Fase 4 los benchmarks muestran que reflect ya no es el cuello de botella (porque el cuello es de I/O o cache), reevaluar prioridad de Fase 6.

> **Resuelto (2026-05-25, [ADR-0017](0017-codegen-type-safety-not-perf-gate.md)):** esta condición de reapertura se cumplió. Los benchmarks de Fase 6 (F6-8a + profiling) mostraron que el cuello es el motor + `database/sql` y allocs arquitectónicas, no reflect. ADR-0017 actúa sobre esa evidencia: retira el gate ≥3× y reencuadra codegen como type-safety. El resto de ADR-0002 (reflect default, codegen opt-in, misma API) sigue vigente.
