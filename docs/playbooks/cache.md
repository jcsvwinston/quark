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

### ~~Sin protección contra cache stampede~~ — cerrado (F4-5, ADR-0011)

**Cerrado** — `stampedeStore` (`cache_stampede.go`) envuelve cualquier
`CacheStore` con las tres protecciones, instalado automáticamente por
`WithCacheStore` (no opt-in). Los stores concretos (`memory.Store`,
`redis.Store`, o de terceros) no cambian — la interfaz pública
`CacheStore` no rompe.

- **Singleflight** in-process (`golang.org/x/sync/singleflight`):
  N callers concurrentes para la misma key colapsan a 1 compute. El
  query path (`query_exec.go:List`) usa el método `getOrCompute` del
  wrapper cuando hace type-assert con `*stampedeStore` (siempre que
  hay caché).
- **TTL jitter**: cada `Set` multiplica el TTL por un factor uniforme
  en `[1-jitterPct, 1+jitterPct]`. Default `±10%`, configurable con
  `WithCacheJitter(pct)`. `0` desactiva jitter.
- **XFetch / probabilistic early refresh**: cada entrada lleva
  metadata (`deltaNs`/`computedAt`/`expiresAt`) embebida como
  `xfetchEntry` length-prefixed. `Get` evalúa
  `timeLeft ≤ delta * β * (-ln(rand()))` (Vattani et al.). Default
  `β = 1.0`, ajustable con `WithCacheXFetchBeta(β)`. `β = 0` desactiva
  XFetch.

Gap documentado (ADR-0011 §Consecuencias): el singleflight **no cubre
cross-instancia** (N procesos → N computes). El stampede in-process es
el caso severo y común y queda resuelto al 100%; cross-instancia se
aborda en ADR sucesor si surge demanda real.

Cobertura: `cache_stampede_test.go` (10 tests: encoding/decoding,
detección de entradas legacy, jitter en rango, XFetch boundary cases,
singleflight bajo carga concurrente con 50 goroutines, hit-after-first,
clamping de configuración).

### ~~Invalidación grosera por tabla~~ — cerrado (F4-6)

**Cerrado** — `executeExec` (`query_crud.go`) ahora acepta `extraTags
...string` variadic. Cuando una mutación conoce la PK afectada
(`Update`, `UpdateFields`, `softDelete`, `hardDeleteByPK`, `Tracked.Save`,
Insert post-PK-populate), pasa `<table>:<pk>` como tag adicional. La
misma `InvalidateTags` call carga AMBOS tags — un solo round-trip al
backing store por mutación.

Los callers pueden cachear queries by-PK con ese tag:
`Find(1).Cache(ttl, "users:1")` invalida sólo cuando esa fila cambia,
no cuando cualquier `users` se toca. Los listings siguen cacheados con
el tag de tabla (`users`), invalidados siempre — la coherencia para
listings se preserva.

Mutaciones que no conocen la PK (`DeleteBatch` con WHERE complejo,
`UpdateBatch`, raw Exec, upserts no-PK-única) **sólo invalidan el tag
de tabla** — el comportamiento histórico, seguro como fallback.
**Composite PKs** caen al tag de tabla también (`rowTag()` retorna
`""` para `HasCompositePK == true`); follow-up posible si surge
demanda usar un encoding estable de PK compuesta para tag granular.

Cobertura: `cache_invalidation_test.go` (3 grupos:
`TestRowTag_Format` con 5 cases, `TestInvalidateInsert_*` con 5
cases, `TestExecuteExec_PassesRowTagAlongTable` con 3 cases verifica
que la wire-up `executeExec(..., rowTag)` → `InvalidateTags(table,
row)` funciona, sin tag → fallback al tag de tabla, tag vacío
filtrado). **BB-15 (PR #175)**: el helper post-insert `invalidateInsert`
(antes `invalidateRowTag`) invalida el table tag **además** del row tag —
los paths INSERT…RETURNING/OUTPUT (PG/SQLite/MariaDB/MSSQL) pasan por
`executeQueryRow`, que no invalida nada, así que sin esto una lectura
cacheada a nivel de tabla quedaba stale tras un `Create`. Regresión
cross-engine en el SharedSuite: `testCacheInsertInvalidation`.

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

### ~~TTL del tag-key Redis = `ttl + 24h`~~ — cerrado (F4-6)

**Cerrado** — `cache/redis/redis.go:Set` reemplaza el `pipe.Expire(...)`
único por la combinación `ExpireNX` + `ExpireGT` en el pipe: el primero
inicializa el TTL cuando el SET acaba de crearse (no tenía TTL), el
segundo extiende cuando el nuevo > actual. **Nunca se acorta**, así
que un key con TTL pequeño tageado tarde no deja keys huérfanas en el
SET.

Requiere **Redis 7.0+** (los flags `NX`/`GT` se introdujeron ahí). En
Redis &lt; 7 los comandos son no-ops y el comportamiento histórico
(broken) vuelve — gap documentado en el comentario inline y aceptado
como requirement de Redis 7+ por defecto.

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
  - ~~Singleflight por cache key~~ — cerrado (F4-5, ADR-0011).
  - ~~TTL con jitter (±10% configurable, `jitterPct` opción)~~ —
    cerrado (F4-5).
  - ~~Probabilistic early expiration (XFetch)~~ — cerrado (F4-5).
  - ~~Invalidación granular por PK además de tabla~~ — cerrado (F4-6).
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
