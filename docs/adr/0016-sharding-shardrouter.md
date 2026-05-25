---
id: 0016
title: Sharding — ShardRouter routes per-query to a shard Client by shard key; no implicit cross-shard
status: accepted
date: 2026-05-25
implemented: F6-7 (skeleton)
deciders: jcsvwinston
related: [0007, 0015]
supersedes: null
tags: [architecture, ha, scaling, sharding, phase-6]
---

# 0016 — Sharding: `ShardRouter`

## Contexto

`For[T](ctx, provider)` resuelve el `*Client` de una query vía
`ClientProvider.GetClient(ctx)` (precedente: `TenantRouter`, ADR-0007). El
pillar de sharding de Fase 6 (F6-7) quiere **particionar los datos entre N
bases de datos shard** por una *shard key* (p.ej. `user_id`, `region`): cada
fila vive en exactamente un shard, y una query debe ir al shard que la posee.

Esto es distinto de las réplicas (ADR-0015), donde todos los nodos tienen los
mismos datos. Aquí los datos están **disjuntos** por shard. Antes de escribir
código hay que fijar: **(1)** cómo llega la shard key al router, **(2)** cómo se
mapea una key a un shard, **(3)** qué pasa con queries que no tienen una sola
shard key (cross-shard).

## Decisión

- **`ShardRouter` implementa `ClientProvider`.** Dado el `ctx`, resuelve la
  shard key, la mapea a un nombre de shard y devuelve el `*Client` de ese
  shard. `For[T](ctx, shardRouter)` corre la query sobre ese Client **sin
  cambios** — el resto del ORM no sabe que hay sharding.

- **La shard key se pasa por contexto, por operación.** `quark.WithShardKey(ctx,
  key)` la inyecta; un `ShardResolver` (con `DefaultShardResolver` leyendo ese
  valor, o uno custom) la extrae. **Uniforme para lecturas y escrituras** — el
  caller dice a qué partición pertenece la operación. Extraer la key
  automáticamente de la entidad en `Create` es trabajo futuro (necesita un hook
  de modelo y no aplica a lecturas; el ctx es el mecanismo uniforme).

- **Mapeo key → shard pluggable (`ShardFunc`).** El skeleton entrega
  `HashShardFunc(shardNames)` (FNV-1a mod N, asignación estable y uniforme); un
  caller puede dar su propio `ShardFunc` (rango, tabla de lookup, geo, …). El
  `ShardFunc` es la costura de resharding.

- **Sin cross-shard implícito.** Una query sin shard key en contexto **falla**
  (no hay fan-out silencioso). El scatter-gather (consultar todos los shards y
  fusionar) es una capacidad **deliberada y separada** → follow-up, no un
  comportamiento por defecto: su semántica (orden, merge, `LIMIT` global,
  agregados) es no trivial y no debe colarse por accidente al olvidar la key.

- **Límites duros (documentados):**
  - **No cross-shard joins** — un `JOIN` sólo ve el shard de la query.
  - **No cross-shard transactions** — una `Tx` está ligada al `*Client` de un
    shard; no hay 2PC. Una transacción que necesite tocar dos shards es un
    error de diseño de la app.
  - Los shards son un `map[nombre]*Client` fijo dado en construcción;
    **resharding / rebalanceo en caliente es responsabilidad del operador**,
    fuera de alcance (cambiar el `ShardFunc` + migrar datos).

- **Composición con multi-tenancy (ADR-0007):** ortogonal. El `*Client` de un
  shard puede a su vez estar detrás de un `TenantRouter`; cada shard gestiona su
  propia config de tenant. No se mezclan en `ShardRouter`.

## Consecuencias

**Positivas:**
- Escalado horizontal de **escrituras y lecturas** (a diferencia de las
  réplicas, que sólo escalan lecturas). API estable: `quark.For[T]` igual.
- El `ShardFunc` aísla la política de partición en un punto.
- Routing explícito por construcción (`GetClient`), testeable, sin estado.

**Negativas:**
- El caller debe suministrar la shard key; olvidarla **falla** (no es una fuga
  silenciosa — preferible a un fan-out accidental). Disciplina, no magia.
- Sin cross-shard joins/tx: la app debe diseñar el modelo para que las
  operaciones queden dentro de un shard (denormalizar, elegir bien la key).
- Scatter-gather diferido: hoy no hay lectura cross-shard.

## Alternativas consideradas

1. **Auto-extraer la shard key de la entidad** (en `Create`/`Update`).
   Rechazado para el skeleton: necesita un hook de modelo y **no aplica a
   lecturas** (`List`/`Find` no llevan entidad). El ctx es el mecanismo uniforme
   para read y write; la extracción por entidad puede añadirse encima.
2. **Scatter-gather implícito al faltar la key.** Rechazado: sorpresivo y caro
   (toca todos los shards), con semántica de merge/orden/límite no trivial.
   Debe ser opt-in explícito, no el fallback de un olvido.
3. **Un solo `*Client` con particionado de tabla del motor** (PG declarative
   partitioning, etc.). Rechazado como solución de F6-7: eso es particionado a
   nivel de motor (una DB), ortogonal al sharding a nivel de aplicación (N DBs).
   Un usuario puede usar ambos.

## Restricciones que esta decisión impone

- `ShardRouter` es un `ClientProvider`; el routing ocurre **en la llamada a
  `For[T]`** — `GetClient(ctx)` se invoca en cada `For[T]`, igual que el routing
  en-ejecución de ADR-0015 — no en una fase de construcción del router.
- Una `Tx` nunca cruza shards (está ligada al Client del shard resuelto).
- Cualquier capacidad cross-shard futura (scatter-gather) debe ser API
  explícita y separada, nunca el comportamiento por defecto de una query normal.

## Cuándo reabrir

- Demanda de scatter-gather / lecturas cross-shard (diseñar merge + límites).
- Extracción de shard key desde la entidad para escrituras.
- Resharding dinámico / consistent hashing con migración asistida.
