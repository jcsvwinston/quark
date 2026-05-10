---
type: playbook
module: cache
files:
  - cache.go
  - cache/memory/memory.go
  - cache/redis/redis.go
last_review: 2026-05-10
related_adrs: [0004]
related_p0: []
phase: 0
---

# Playbook: Caché L2

## Qué cubrimos

Caché L2 integrada con interfaz pluggable (ADR 0004). Tres componentes:

- **`CacheStore`** (interfaz): `Get/Set/Delete/InvalidateTags`.
- **`memory.Store`** (`cache/memory/memory.go`): TTL + reverse-index de tags + goroutine de cleanup.
- **`redis.Store`** (`cache/redis/redis.go`): claves `quark:cache:*` y SETs `quark:tag:*` para invalidación.

Activación: `quark.WithCache(memory.New(...))` o `quark.WithCache(redis.New(client))`. La caché se integra al middleware pipeline; cada query SELECT pasa por ella si la activas.

Cache key incluye: `dialect.Name() + tenantID + schema + sqlStr + args` (`cache.go:35-45`).

## Bugs P0 vivos

Ninguno crítico hoy. Hay **deuda estructural** que limita uso en producción.

## Limitaciones críticas (deuda Fase 4)

### Sin protección contra cache stampede

Si una clave caliente expira y N requests llegan simultáneamente, **todas miss y emiten la query**. Resultado: tormenta de queries idénticas a la DB.

Hoy el cache no usa:
- **Singleflight**: que la primera request en miss compute la respuesta y las demás esperen.
- **Probabilistic early expiration** (XFetch algorithm): refresh la clave antes de expirar con probabilidad creciente, evitando convergencia en el momento de expirar.
- **Jitter en TTL**: ±10% para que claves cacheadas en lote no expiren simultáneamente.

Plan Fase 4: introducir las tres protecciones por defecto.

### Invalidación grosera por tabla

`executeExec` (`query_crud.go:43`) invalida por `q.table`. Si haces `UPDATE users SET name = 'x' WHERE id = 1`, **todas las queries cacheadas que tocan `users` se borran**. Aunque sean para otros usuarios, otros tenants, o queries que no leen `name`.

Esto es seguro (nunca devuelves stale data) pero ineficiente (alta tasa de invalidación = alta tasa de miss).

Plan Fase 4: invalidación granular por PK afectada. Mutaciones registran las PKs cambiadas y se emiten invalidaciones precisas. Tag por tabla queda como fallback para casos donde no se conocen las PKs (DELETE WHERE complex).

### Cache key serializa args con `%v`

`cache.go:35-45` construye el key con `fmt.Sprintf("%v", arg)`. Esto colisiona:

- `int64(1)` y `string("1")` ambos producen `"1"` → misma key, riesgo de devolver resultado equivocado si el SELECT estaba parametrizado.
- `time.Time` con timezones distintas pero mismo wall-clock pueden colisionar.
- `nil` y string vacío.

Plan Fase 4: serialización determinista con `gob` o length-prefixed con `binary.Write`.

### TTL del tag-key Redis = `ttl + 24h`

`cache/redis/redis.go:58` configura el SET de tag con TTL `cacheTTL + 24h`. Si el mismo tag se reescribe con TTL distinto, no se actualiza al máximo, **se sobreescribe** — el TTL del SET puede quedar más corto que las claves que apunta, leak de keys huérfanas en el SET.

Plan Fase 4: estrategia `EXPIREAT` con `MAX(current, new)` o cleanup periódico.

### Sólo cache-aside, no read-through ni write-through

Hoy el patrón es: la query lee la cache, si miss ejecuta la DB, si hit la usa. No hay read-through (la cache misma calcule el SELECT) ni write-through (UPDATE actualiza la cache directamente sin invalidar).

No es necesariamente deuda — cache-aside es lo correcto para la mayoría de los casos. Pero documenta la decisión.

## Anti-patterns a vigilar

### Cachear queries con `args` que incluyen valores de usuario sin sanitizar

El cache key se calcula sobre `args`. Si un arg es de tipo arbitrario (`any`) y la app permite valores de usuario en queries cacheadas, alguien puede:

- Construir argumentos con `fmt.Stringer` que devuelva strings predecibles, colisionando con keys de otros usuarios.
- Construir args muy largos para inflar la cache.

Mitigación: validar tipos de args antes de cachear; no cachear queries con args que no sean primitivos serializables.

### Cachear queries bajo tenant sin incluir `tenantID`

Hoy el key incluye `tenantID`, pero si introduces un nuevo path de cache (ej. cache de related entities en eager loading) **debe** incluir el tenant ID. Si no, el tenant A puede leer datos cacheados del tenant B.

### Singleflight ad-hoc en sitios distintos

Cuando llegue Fase 4, no implementes singleflight en cada call site. Hazlo a nivel de la capa de cache (interfaz `CacheStore` o un wrapper sobre ella). Si algún caller lo hace por su cuenta, romperá la coherencia con el resto.

## Decisiones que afectan al módulo

- **ADR 0004**: caché integrada (no plugin externo). Mantenida en el repo principal con interfaz pública para terceros.

## Roadmap de mejora

- **Fase 4**:
  - Singleflight por cache key.
  - TTL con jitter (±10% configurable, `jitterPct` opción).
  - Probabilistic early expiration opcional.
  - Invalidación granular por PK además de tabla.
  - Cache key con serialización determinista.
  - Negative caching opcional (cachear `ErrNoRows`).
  - Compresión opcional (gzip) para values grandes.

## Tests críticos a no romper

- `cache_all_engines_test.go` — cache opera con los 6 motores.
- Tests de invalidación por tag.

Cualquier cambio en `cache.go` o stores debe pasar la suite multi-engine; las claves no son universales (`dialect.Name()` es parte del key) y eso debe respetarse.

## Cuándo invocar al `code-reviewer`

Antes de cualquier PR que toque `cache.go`, `cache/memory/`, `cache/redis/`, o que cambie cómo `executeQuery`/`executeExec` integran caché. El reviewer vigila especialmente:

- No se introduce singleflight/jitter/PEX a medias (todo o nada en Fase 4).
- Cache key incluye todos los discriminantes (dialect, tenant, schema, args serializados deterministically).
- Invalidación cubre las mutaciones del PR.
- Documentación de la cache en `website/docs/caching/` está al día.
