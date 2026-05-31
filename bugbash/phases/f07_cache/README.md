# F7 — Caché

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F7.
> Playbook: [`docs/playbooks/cache.md`](../../../docs/playbooks/cache.md).

## Qué prueba

La caché L2 integrada (ADR-0004 / ADR-0011) sobre el **camino real de query**,
por motor. Comprueba los comportamientos observables black-box, contando los
SELECT que llegan de verdad a la DB con un `QueryObserver` (el query path
emite exactamente un evento de observer por viaje real a la DB, incluso cuando
el singleflight colapsa N llamadas; los cache hits no lo disparan).

## Grupos cubiertos (memory backend, los 6 motores)

- **Singleflight** — 1000 goroutines lanzan la misma query cacheada a la vez;
  el `stampedeStore` (auto-instalado por `WithCacheStore`) colapsa los misses
  de caché fría a **≈1** query a la DB. El ideal es 1; `getOrCompute` consulta
  la caché antes de entrar al singleflight (Get-then-Do), así que un caller que
  falló el Get pero llega al `Do` justo tras terminar el primer compute puede
  arrancar un 2º — ventana inherente y documentada, no un stampede. La garantía
  es **colapso efectivo** (N reads fríos → constante diminuta, nunca ~N); el
  test asierta `≤ 5` y loguea el conteo real.
- **CacheAside** — read frío = 1 hit a DB; read idéntico = servido de caché;
  read con args distintos = key distinto = nuevo hit (discriminación de key).
- **PerRowInvalidation** (F4-6) — una entrada cacheada bajo un tag de fila
  (`<tabla>:<pk>`) **sobrevive** la mutación de *otra* fila y se invalida sólo
  con la mutación de la suya. Demuestra la invalidación granular por PK.
- **EmptyResultCaching** — una query cacheada que no matchea filas cachea el
  resultado vacío; el segundo read idéntico se sirve de caché.
- **JitterDoesNotBreakHits** — con jitter de TTL activado, un read caliente
  sigue sirviéndose de caché (la distribución ±10% se mide en
  `cache_stampede_test.go`, no aquí).

## Grupo Redis (gated)

- **RedisBackend** — singleflight + invalidación por tag contra el
  `redis.Store`, condicionado a un Redis alcanzable (`QUARK_TEST_REDIS_ADDR`,
  default `localhost:6379`). Redis es un backend de caché opcional, **no un
  motor SQL** — si no hay Redis se loguea scope-out (la matriz de motores SQL
  la cubren los otros grupos), no es un skip de motor.

  Para ejercitarlo: `docker run -d --name bugbash-redis -p 6379:6379 redis:7-alpine`.

## Fuera de scope black-box (citado, no re-medido)

- **TTL jitter (±10%)** y **XFetch** (refresh probabilístico): internos y
  verificados en `cache_stampede_test.go`. F7 sólo confirma que activar jitter
  no rompe los hits.
- **Gap cross-instancia** (N procesos → N computes): es un **non-bug
  documentado** (ADR-0011 §Consecuencias); el singleflight es in-process.
  Reproducirlo necesita procesos separados — fuera del alcance de un test.
- **Negative caching de fila** (`First` que devuelve no-rows): **diferido**
  (playbook §Roadmap, "Negative caching — diferido a future work"). Ojo a la
  distinción: el caching de un **resultado de lista vacío** sí funciona
  (grupo EmptyResultCaching); lo diferido es el caso `First`/`ErrNoRows`.

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestCache -v ./phases/f07_cache/                       # SQLite (+ Redis si está arriba)
go test -tags=bugbash -run TestCache -v ./phases/f07_cache/ -engines=all -timeout 40m
```

## Criterio done

- [x] Singleflight efectivo (1000 → ≈1, colapso a constante diminuta) en los 6 motores.
- [x] Invalidación granular por PK (F4-6) verificada.
- [x] Caching de resultado vacío.
- [x] Redis backend (singleflight + invalidación) cuando hay Redis.
- [x] Jitter no rompe hits; distribución medida en unit tests (citado).
