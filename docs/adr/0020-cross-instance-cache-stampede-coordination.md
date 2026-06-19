---
id: 0020
title: Coordinación de cache-stampede cross-instancia vía capacidad opcional CacheLocker
status: accepted
date: 2026-06-19
implemented: stampedeStore.computeWithLock + memory/redis CacheLocker; opt-in WithCacheCrossInstance; cierra el deferral v1.2 "cache stampede cross-instancia"
deciders: jcsvwinston
related: [0011, 0004]
supersedes: null
tags: [cache, stampede, cross-instance, redis, v1.2]
---

# 0020 — Coordinación de cache-stampede cross-instancia

## Contexto

ADR-0011 entregó protección contra cache stampede vía un wrapper (`stampedeStore`)
sobre cualquier `CacheStore`: **singleflight** (in-process), **TTL jitter** y
**XFetch** (refresh probabilístico anticipado). Su §Consecuencias dejó un gap
explícito y diferido: el singleflight es **in-process** — N procesos que pierden
la misma clave caliente a la vez disparan **N recomputes** contra la base de
datos, uno por proceso. XFetch mitiga ese caso *probabilísticamente* (cada
proceso decide refrescar antes de la expiración con probabilidad creciente, lo
que reparte los recomputes en el tiempo), pero no hay coordinación
**determinista** entre procesos: bajo un miss simultáneo real, varios siguen
computando.

El deferral v1.2 ("coordinación de stampede cross-instancia") es cerrar ese gap.

Dos hechos restringen la elección:

1. **`CacheStore` no tiene primitiva de lock** (`Get`/`Set`/`Delete`/`InvalidateTags`),
   y `Set` no expone semántica `NX` (sobreescribe). El wrapper no puede construir
   un lock atómico cross-proceso sólo con la interfaz actual.
2. **`v1.x` mantiene compatibilidad de API.** No se puede añadir un método
   obligatorio a `CacheStore` (rompería todos los stores de terceros).

## Decisión

**Coordinación cross-instancia vía una capacidad OPCIONAL `CacheLocker`,
detectada por type-assertion, activada con un opt-in explícito
`WithCacheCrossInstance()`, con política wait-and-reread.**

- **`CacheLocker`** (nueva interfaz, aditiva): `AcquireLock(ctx, key, ttl)
  (acquired bool, release func() error, err error)` — un try-lock **no
  bloqueante** por clave con TTL. El wrapper hace `inner.(CacheLocker)`: si el
  store la implementa **y** el opt-in está activo, coordina; si no, cae al
  comportamiento de ADR-0011 (singleflight + XFetch) sin cambios. Cero breaking
  change.
- **Flujo en `getOrCompute`** (dentro del singleflight, que ya colapsó este
  proceso a un caller): el ganador del lock recomputa, escribe la caché y
  libera; los perdedores hacen **wait-and-reread** (sondean `Get` hasta que
  aparece el valor del ganador, con presupuesto = TTL del lock), y sólo computan
  si ese wait expira (ganador lento/caído — el lock auto-expira).
- **Implementaciones**: `redis.Store` vía `SET key token NX PX ttl` + release Lua
  token-checked (sólo borra si sigue siendo su token → un release tardío tras la
  expiración no pisa a un nuevo holder); `memory.Store` vía try-lock in-process
  por clave (caso degenerado de un solo proceso, pero contrato correcto).
- **Opt-in**: `WithCacheCrossInstance()`, **off por defecto**. No cambia el
  comportamiento de los usuarios actuales ni paga el round-trip del lock salvo
  que se pida explícitamente.
- **Best-effort**: un error del backend de lock degrada a compute sin coordinar
  (no falla ni bloquea la request); el lock auto-expira (holder caído → otro
  proceso toma el relevo, degradando a unos pocos computes, nunca la manada
  completa de N).

## Alternativas consideradas

- **Extender `CacheStore` con el lock** (método obligatorio): rechazado —
  breaking change en `v1.x`, rompe stores de terceros. La capacidad opcional
  type-asserted es el patrón ya usado por ADR-0011.
- **Sólo XFetch (no hacer nada más)**: insuficiente — es probabilístico, no
  determinista; bajo un miss simultáneo real varios procesos computan.
- **Lock vía `Set` sin `NX`**: racy — sin atomicidad NX hay carrera entre
  "compruebo marcador" y "escribo marcador"; no es un lock real.
- **Auto-on cuando el store implementa `CacheLocker`** (sin opt-in): rechazado —
  cambiaría el comportamiento (y añadiría el round-trip del lock) a los usuarios
  de Redis existentes sin que lo pidan. El opt-in es conservador y `v1.x`-safe.
- **Serve-stale para los perdedores** (servir el valor viejo mientras el ganador
  recomputa): mejor latencia, pero exige cambiar el modelo de expiración del
  wrapper para retener el valor stale. Diferido — wait-and-reread es más simple y
  correcto; reabrir si hay demanda.

## Consecuencias

- **Positivas**: bajo carga multi-instancia con clave caliente, un miss colapsa a
  **un recompute** cross-proceso (antes N). Aditivo y opt-in: cero impacto para
  quien no lo activa o usa un store sin `CacheLocker`. El lock auto-expira →
  ningún proceso caído puede atascar la clave.
- **Coste**: un round-trip de lock a Redis por miss de clave caliente (sólo con
  el opt-in). Un holder patológicamente lento (> TTL del lock) degrada a unos
  pocos computes, nunca la manada completa.
- **Límites**: la política de perdedor es wait-and-reread (no serve-stale); el TTL
  del lock y el intervalo de sondeo son constantes internas (no configurables aún).

## Cuándo reabrir

Si se quiere servir stale a los perdedores, o hacer configurable el TTL del lock /
el wait, o activar la coordinación automáticamente — cada uno es un ADR sucesor
sobre este.
