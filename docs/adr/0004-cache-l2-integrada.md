---
id: 0004
title: Caché L2 integrada (memory/redis), no plugin externo
status: accepted
date: 2026-05-10
deciders: jcsvwinston
related: []
supersedes: null
tags: [architecture, caching, performance]
---

# 0004 — Caché L2 integrada (memory/redis), no plugin externo

## Contexto

Los ORMs gestionan caché de tres formas distintas:

- **Sin caché propia.** GORM, sqlc, ent. El usuario pone Redis encima si lo necesita, gestiona su propia invalidación. Cero acoplamiento, máximo trabajo del usuario.
- **Caché vía plugin oficial.** Hibernate (Ehcache/Hazelcast), GORM (`gorm.io/plugin/cache`). El plugin es opcional pero "bendecido". El usuario activa caché con un import.
- **Caché integrada con interfaz pluggable.** Doctrine, EF Core (en parte), Quark hoy. La capa de caché vive en el ORM con interfaz `CacheStore`; implementaciones (memory/redis) son intercambiables. El usuario obtiene caché con `WithCache(...)`.

Quark hoy ya implementa la opción tres. Existe `CacheStore` con `Get/Set/Delete/InvalidateTags`, una memory-store con TTL + tags + cleanup goroutine, y una redis-store con `quark:cache:*` y `quark:tag:*`.

## Decisión

**Mantener la caché L2 integrada con interfaz pluggable. No moverla a plugin externo.**

La caché es parte del producto Quark, no un add-on. El usuario obtiene `WithCache(memory.New(...))` o `WithCache(redis.New(...))` directamente desde el módulo principal.

La interfaz `CacheStore` queda **estable**: terceros pueden implementar stores propios (Memcached, Hazelcast, in-process LRU custom) sin tocar el core.

## Consecuencias

**Positivas:**
- Diferenciador real frente a GORM/ent que no traen caché out-of-the-box.
- Coherencia de invalidación: el ORM sabe cuándo emite mutaciones, así que invalida tags automáticamente sin que el usuario lo configure.
- API uniforme: cambiar de memory a redis es una línea, no una migración.
- Consumible por Nucleus directamente sin imports adicionales.

**Negativas:**
- Más superficie de mantenimiento en el repo principal (memory + redis stores).
- Los usuarios que NO quieran caché tienen las dependencias en `go.sum` indirectas (mitigable con build tags si el peso resulta crítico — no lo es hoy).
- Limita ritmo de innovación de cache stores: un store Memcached experimental tiene que pasar por el repo principal (mitigable: aceptamos stores externos vía la interfaz pública).

**Limitaciones reconocidas (deuda de Fase 4):**
- Sin protección contra cache stampede (singleflight, jitter, probabilistic early expiration).
- Invalidación grosera por tabla, no por PK.
- Cache key serializa args con `%v` — colisiones posibles entre tipos.
- TTL de tag-key en redis = `ttl + 24h` puede dejar leaks.

Estas se cierran en Fase 4 (`docs/ANALISIS_MADUREZ.md` §4 Fase 4). **No se anuncia caché como "production-grade" hasta entonces.**

## Alternativas consideradas

1. **Sin caché, dejar que el usuario monte la suya.** Rechazado: el caso de uso de Nucleus la demanda; sin caché propia, todos los usuarios reimplementan la misma capa.
2. **Plugin externo (`quark-cache-redis`, `quark-cache-memory`).** Rechazado: la coherencia de invalidación require que el ORM conozca el cache; un plugin externo siempre estará un commit detrás de los hooks reales.
3. **Caché de query por defecto sin invalidación (read-through con TTL).** Rechazado: sin invalidación = stale data garantizada; mejor cero caché que caché peligrosa.

## Cuándo reabrir

Si tras Fase 4 emerge una clase de cache store no soportable con la interfaz actual (write-through con coherencia distribuida tipo CRDT, por ejemplo), reabrir para evolucionar la interfaz.
