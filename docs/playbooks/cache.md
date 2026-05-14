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

### ~~Cache key serializa args con `%v`~~ — cerrado (F4-4)

**Cerrado** — `generateCacheKey` (`cache.go`) ya no usa
`fmt.Sprintf("%v", arg)`. El encoding es ahora **type-tagged y
length-prefixed**: cada componente fijo (`dialect`, `tenantID`,
`schema`, `sqlStr`) va length-prefixed, y cada bind arg lleva un byte
de tipo (`cacheArg*`) más su valor en big-endian. Cierra las tres
clases de colisión:

- **tipo**: `int64(1)` y `string("1")` tenían tags distintos → keys
  distintos. Idem `uint64` / `float64` / `bool` / `nil`.
- **boundary**: sin separadores, `"my"+"sql"` hasheaba el mismo stream
  que `"mysql"+""`, y args `"ab"+""` igual que `"a"+"b"`. El
  length-prefix lo elimina.
- **nil**: `nil` tiene su propio tag, no colisiona con `""`.

`time.Time` se keyea por `UnixNano()`: el mismo instante en zonas
distintas da el mismo key (cache hit legítimo), instantes distintos
nunca colisionan. Tipos no primitivos caen a `fmt.Sprintf("%#v", v)`
(incluye el tipo Go, no invoca `Stringer` — cierra el vector de
colisión por `Stringer` predecible documentado más abajo).
Reflection-free (ADR-0002). Cobertura: `cache_test.go` (determinismo,
type/boundary/nil collision, time, discriminantes de query).

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
  - ~~Cache key con serialización determinista~~ — cerrado (F4-4).
  - Singleflight por cache key — F4-5 (ADR-0011, wrapper común).
  - TTL con jitter (±10% configurable, `jitterPct` opción) — F4-5.
  - Probabilistic early expiration (XFetch) — F4-5 (in scope).
  - Invalidación granular por PK además de tabla — F4-6.
  - Negative caching — diferido a future work (fuera de Fase 4).
  - Compresión opcional (gzip) para values grandes — diferido a
    future work (fuera de Fase 4).

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
