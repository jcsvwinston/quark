# F14 — Soak / long-run

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F14.
> Relacionado: harness de stress en `benchmarks/stress/` (F6-9).

## Qué prueba

Que un workload mixto sostenido no degrada con el tiempo: latencia estable,
memoria estable, cero panics, cero errores inesperados. Por motor.

## Time-box (lee esto)

El objetivo del spec es **12h × 6 motores (72 engine-hours)** con snapshots de
métricas cada 5 min — eso es una **pasada de ventana release-candidate**, no de
CI. Esta fase está **parametrizada y acotada en tiempo**: corre un workload corto
por defecto (`-soak-seconds`, default 5s) y verifica los invariantes del soak
sobre esa ventana. La pasada RC real:

```bash
go test -tags=bugbash -run TestSoak ./phases/f14_soak/ \
    -engines=all -soak-seconds=43200 -soak-workers=8 -timeout 13h
```

## Workload

60% reads (point lookup cacheado), 30% writes (insert de `soak_txn`, invalida el
tag de caché de la tabla), 10% complex (JOIN de dos tablas `soak_txns ⋈
soak_accounts`). Varios workers (`-soak-workers`, default 4) con la **caché L2
configurada** (`WithCacheStore(memory.New())`; los reads usan `Cache()`).

## Invariantes verificados (sobre la ventana)

- **Latencia no degrada**: la mediana de la segunda mitad de la ventana está
  dentro de un factor generoso (4×) de la primera mitad — captura crecimiento
  desbocado, no jitter. (Necesita ≥50 muestras por mitad; si no, se loguea y se
  salta el chequeo de tendencia.)
- **Memoria estable**: el heap post-run (tras GC) está dentro de un factor
  generoso (5×) del baseline post-seed **y** por encima de un piso absoluto de
  64 MiB para flagear — así el churn de warmup sobre un baseline pequeño no da
  falso positivo. Best-effort (`CategoryGap`/P2): el heap es ruidoso en ventanas
  cortas.
- **Cero panics** (los workers hacen `recover` y cuentan) y **cero errores** de
  operación.

Las muestras de latencia por mitad están **acotadas** (5000) para que el
bookkeeping del propio test no domine la medición de memoria.

## Notas

- **SQLite** es single-writer: la DSN añade `busy_timeout(5000)` para que la
  contención se manifieste como **latencia**, no como `SQLITE_BUSY` (mismo
  enfoque que `benchmarks/stress`). Motores servidor: concurrencia real, DSN sin
  cambios.
- **Snapshots OTel cada 5 min**: parte de la pasada RC completa, fuera de scope
  de la versión acotada.
- La columna del `WHERE` en el op complejo va **sin cualificar** (`acct_id`): el
  guard de identifiers rechaza nombres con punto; el `ON` sí va cualificado (su
  validador lo permite).

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestSoak -v ./phases/f14_soak/                       # SQLite, 5s
go test -tags=bugbash -run TestSoak -v ./phases/f14_soak/ -engines=all -soak-seconds=43200 -timeout 13h
```

## Hallazgos (en `TASKS.md` § "Bug-bash hallazgos")

**Sin hallazgos.** Pasada acotada 2026-06-03 (Docker, 6s/motor): verde en los 5
motores de CI (SQLite + PG + MySQL + MariaDB + MSSQL), 0 errores, 0 panics,
latencia plana o decreciente entre mitades, memoria estable. Fase test-only (sin
cambio de código). La pasada RC de 12h × 6 motores queda como paso de
release-candidate (no CI).

## Criterio done

- [x] Latencia estable (no creciente) sobre la ventana acotada.
- [x] Memoria estable (sin leak).
- [x] Cero panics no esperados; cero errores de operación.
- [ ] (RC) 12h × 6 motores sin incidencias — pendiente de la ventana RC.
