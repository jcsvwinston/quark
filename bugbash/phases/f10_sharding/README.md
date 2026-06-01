# F10 — Sharding

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F10.
> Superficie: `shard_router.go` (`ShardRouter`, `NewShardRouter`, `HashShardFunc`,
> `WithShardKey`, `DefaultShardResolver`, `GetClient`/`ShardNames`). ADR-0016.

## Qué prueba

Routing por shard key vía `ShardRouter`: distribución, ausencia de fan-out
implícito, cero leaks cross-shard, tx ligada a un shard, y estabilidad de la API
al resharding.

## Topología

4 (luego 5) **shards = ficheros SQLite independientes** — el spec admite "4
SQLite files o 4 PG schemas". El routing es **engine-agnostic** (cada shard es un
`*Client`), así que SQLite basta y no necesita contenedor; el motor de cada shard
es irrelevante para la lógica de routing bajo prueba. **No requiere Docker.**

## Grupos cubiertos

- **HashDistribution** — enruta 4000 keys (`cust-N`) por `WithShardKey` sobre 4
  shards con `HashShardFunc` (FNV-1a mod N); cuenta filas por shard y aplica
  **chi-square** (df=3, uniforme si < 7.815). Determinista (keys fijas) → no
  flaky. Escalado desde las 100k del spec (logueado).
- **NoShardKeyErrors** — una operación **sin** shard key en el `context` da error
  (`ErrInvalidQuery`): no hay fan-out cross-shard implícito. Cubre write, read y
  `GetClient` directo.
- **CrossShardNoLeak** — una fila escrita bajo la key kA vive **sólo** en
  shard(kA); una lectura bajo una key que mapea a otro shard nunca la ve.
- **TxBoundToShard** — un `Tx` obtenido para un shard escribe **sólo** en ese
  shard; el router resuelve shards distintos a `*Client` distintos → un `Tx`
  jamás puede cruzar shards (no hay transacción cross-shard, ADR-0016).
- **ReshardingAPIStable** — añadir un 5º shard y reconstruir el router con un
  `HashShardFunc` nuevo mantiene la API funcionando (el dato **no** se migra —
  eso es trabajo del operador; se verifica que el routing/onboarding no rompe).

## Fuera de scope (logueado)

- **100k ops del spec** → 4000 (suficiente para una distribución significativa en
  SQLite sin volumen soak-tier).
- **PG schemas como shards**: equivalente al modelo de ficheros SQLite (routing
  engine-agnostic); no se duplica la cobertura.
- **Migración de datos en resharding** (scatter-gather, rebalanceo): follow-ups
  abiertos de F6-7; la fase verifica la **estabilidad de la API**, no el
  rebalanceo de datos.

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestSharding -v ./phases/f10_sharding/
```

## Hallazgos (en `TASKS.md` § "Bug-bash hallazgos")

**Sin hallazgos.** Pasada 2026-06-01 (SQLite, 4→5 shards): 5/5 grupos verdes,
distribución casi perfecta (1001/1001/999/999 → chi-square 0.004), 2× limpio.
Routing por key, error sin key, cero leaks cross-shard, tx por-shard y API
estable al resharding, todos sólidos. Fase test-only (sin cambio de código).

## Criterio done

- [x] Distribución uniforme (chi-square < 7.815).
- [x] Cero leaks cross-shard.
- [x] Error claro sin shard key (`ErrInvalidQuery`); sin fan-out implícito.
- [x] `Tx` ligada a un único shard (sin transacción cross-shard).
- [x] API estable al añadir un shard (resharding).
