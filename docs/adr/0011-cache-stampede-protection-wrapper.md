---
id: 0011
title: Cache stampede protection vía wrapper común sobre CacheStore
status: accepted
date: 2026-05-14
deciders: jcsvwinston
related: [0004]
supersedes: null
tags: [cache, performance, phase-4]
---

# 0011 — Cache stampede protection vía wrapper común sobre CacheStore

## Contexto

`docs/playbooks/cache.md` documenta la deuda estructural de la caché L2:
sin protección contra *cache stampede*, una clave caliente que expira
con N requests concurrentes produce N misses simultáneos y N queries
idénticas a la base de datos. Es el problema que más limita el uso de
la caché en producción.

La Fase 4 (`docs/ANALISIS_MADUREZ.md` §4) lo aborda con tres mecanismos
que el playbook exige introducir **juntos** ("todo o nada"):

- **Singleflight** — la primera request en miss computa la respuesta;
  las demás esperan ese resultado en vez de lanzar su propia query.
- **TTL con jitter** (±10% configurable) — claves cacheadas en lote no
  expiran todas en el mismo instante.
- **Probabilistic early expiration (XFetch)** — refresca la clave antes
  de expirar con probabilidad creciente, evitando la convergencia de
  requests en el momento exacto de expiración.

El playbook es tajante sobre **dónde** vive esta lógica:

> Cuando llegue Fase 4, no implementes singleflight en cada call site.
> Hazlo a nivel de la capa de cache (interfaz `CacheStore` o un wrapper
> sobre ella). Si algún caller lo hace por su cuenta, romperá la
> coherencia con el resto.

Quedan dos formas de "a nivel de la capa de cache": extender la
interfaz `CacheStore` o envolverla. Este ADR fija cuál.

## Decisión

**Un wrapper común sobre `CacheStore`.** Fase 4 introduce un tipo
interno (p.ej. `stampedeStore`) que implementa `CacheStore`, envuelve
otro `CacheStore` cualquiera (`memory.Store`, `redis.Store`, o uno de
terceros) y le añade singleflight + jitter + XFetch. Los stores
concretos **no cambian** y la interfaz pública `CacheStore` **no
cambia**.

- **Singleflight** es **in-process**: N requests concurrentes dentro de
  un mismo proceso colapsan a 1 query. Implementado con
  `golang.org/x/sync/singleflight` o equivalente propio, indexado por
  cache key.
- **Jitter** se aplica al TTL en cada `Set`; `jitterPct` es
  configurable (default 10%).
- **XFetch** guarda, junto al value, el coste de cómputo de la última
  query (delta) y un timestamp; en `Get` decide probabilísticamente si
  devolver el value cacheado o señalar un refresh temprano.

Activación: **por defecto cuando hay caché configurada**
(`quark.WithCache(...)` envuelve automáticamente con `stampedeStore`).
El playbook pide "introducir las tres protecciones por defecto" — no es
opt-in. Una opción permitirá ajustar `jitterPct` y desactivar XFetch si
hace falta, pero el singleflight no se desactiva.

## Consecuencias

**Positivas:**

- Una sola implementación de la lógica de stampede — cero divergencia
  entre `memory` y `redis`.
- `memory.Store` y `redis.Store` no se tocan: menos superficie de bug,
  sus tests multi-motor existentes siguen válidos.
- Los `CacheStore` de terceros siguen funcionando sin cambios — no hay
  ruptura de la interfaz pública (respeta ADR-0004: caché con interfaz
  estable para terceros).
- El stampede in-process —el caso severo y común— queda resuelto al
  100%.

**Negativas:**

- El singleflight **no cubre el caso cross-instancia**: N procesos en
  un despliegue multi-réplica siguen pudiendo lanzar N queries (una por
  proceso) cuando la clave expira. Es un orden de magnitud menos severo
  que el stampede in-process (N requests → N queries por proceso) y se
  documenta como gap explícito en `cache.md` y en la doc pública.
- XFetch necesita persistir metadata extra por value (delta + timestamp).
  En `redis.Store` esto implica un encoding del value que el wrapper
  controla; el playbook ya prevé serialización determinista (F4-4), así
  que el coste se absorbe ahí.

## Alternativas descartadas

- **Métodos nuevos en la interfaz `CacheStore`** (cada Store implementa
  su singleflight; redis usaría `SET NX` como lock distribuido).
  Cubriría el caso cross-instancia, pero: (a) dos implementaciones
  divergentes que mantener, (b) más superficie de bug, (c) **rompe a
  los implementadores de `CacheStore` de terceros**, que tendrían que
  implementar la nueva superficie. El coste de mantenimiento y la
  ruptura no compensan resolver hoy un caso de severidad menor.
- **Híbrido: wrapper común + hook `DistributedLock` opcional** que
  redis implementaría para el caso cross-instancia. Es la opción más
  completa pero introduce tres caminos de código y obliga al ADR a
  especificar el fallback cuando el lock distribuido no está
  disponible. Complejidad especulativa: se adopta sólo si aparece
  demanda real de stampede protection cross-instancia.

## Cuándo reabrir

Si un despliegue multi-réplica reporta stampede cross-instancia real
(no teórico), abrir un ADR sucesor que adopte la opción híbrida: el
wrapper de este ADR se mantiene como la capa in-process y se le añade
el hook `DistributedLock` opcional. La interfaz `CacheStore` pública no
debería romperse ni siquiera entonces — el hook va en una interfaz
opcional aparte, como `MigrationLocker` en F3-1.
