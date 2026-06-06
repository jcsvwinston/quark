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

- **Pool acotado y reutilizado** (`WithMaxOpenConns`/`WithMaxIdleConns` = nº
  workers; SQLite a 1): mantiene un pool estable que **reutiliza** conexiones en
  vez de abrirlas/cerrarlas por op. Con el default `MaxIdleConns(2)` y N>2
  workers, la mayoría de conexiones se churneaban cada op — y ese churn **saturó
  el listener de Oracle con `ORA-12516`** en la pasada RC de 12h (ver Hallazgos).
  Un app real usa pool acotado; el soak también.
- **SQLite** es single-writer: pool a 1 conexión + la DSN añade
  `busy_timeout(5000)` para que la contención se manifieste como **latencia**,
  no como `SQLITE_BUSY` (mismo enfoque que `benchmarks/stress`). Motores
  servidor: concurrencia real con el pool acotado a nº workers.
- **Snapshots OTel cada 5 min**: parte de la pasada RC completa, fuera de scope
  de la versión acotada.
- La columna del `WHERE` en el op complejo va **sin cualificar** (`acct_id`): el
  guard de identifiers rechaza nombres con punto; el `ON` sí va cualificado (su
  validador lo permite).

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestSoak -v ./phases/f14_soak/                       # SQLite, 5s (smoke)
```

### Pasada RC (12h × 6 motores) — usa el script, no a mano

El soak RC debe cubrir **los 6 motores** (SQLite, PostgreSQL, MySQL, MariaDB, SQL
Server, **Oracle**) y **sobrevivir al cierre de la terminal/sesión** (un
`go test` en background muere con la sesión — eso abortó el primer intento). El
script [`run-rc-soak.sh`](run-rc-soak.sh) cablea los 6 (imposible saltarse uno)
y lanza cada job desacoplado con `nohup`:

```bash
./phases/f14_soak/run-rc-soak.sh           # lanza 12h × 6 motores, detached
./phases/f14_soak/run-rc-soak.sh watch     # check de progreso
./phases/f14_soak/run-rc-soak.sh collect   # resultado + nº de hallazgos por motor
./phases/f14_soak/run-rc-soak.sh stop      # mata jobs + borra contenedores

# overrides: SOAK_SECONDS (default 43200=12h), SOAK_WORKERS (default 8),
# BUGBASH_DSN_ORACLE (reusar un Oracle local en vez de bootear bugbash-oracle).
```

Oracle: el harness lo arranca solo (gvenzl, ~5 min) y le hace el `GRANT
DBMS_LOCK`; si tu Oracle local ya ocupa el 1521, exporta `BUGBASH_DSN_ORACLE`
para reusarlo (evita la colisión de puerto). Aunque Oracle está excluido del
**CI** (image issue), el soak RC es manual/local y **sí lo incluye**.

## Hallazgos (en `TASKS.md` § "Bug-bash hallazgos")

**Sin bugs de Quark.** Pasada acotada 2026-06-03 verde en los 5 de CI. La pasada
**RC de 12h × 6 motores** (2026-06-05) salió **limpia en los 4 motores de
producción** (PG/MySQL/MariaDB/MSSQL: 12h, 0 errores, latencia/memoria planas).
Dos motores fallaron **por configuración del harness, no por Quark**:

- **Oracle** — 269k errores, **todos `ORA-12516`** (listener sin handlers libres)
  + degradación de latencia. Causa: el soak **no acotaba el pool** → con
  `MaxIdleConns(2)` y 8 workers las conexiones se churneaban y saturaban el
  listener del Oracle free-tier. Re-validado con el pool acotado: **0 errores,
  latencia plana**. Era config/entorno, no un defecto del ORM. Arreglado en el
  harness (pool acotado+reutilizado).
- **SQLite** — 2.896 errores de 101.7M ops (0,003%): goteo de `SQLITE_BUSY` con
  8 escritores sobre un motor single-writer. Rigidez del test; el pool a 1
  conexión lo elimina.

## Criterio done

- [x] Latencia estable (no creciente).
- [x] Memoria estable (sin leak).
- [x] Cero panics; cero errores de op (tras acotar el pool — el churn de
      conexiones era el único origen de errores).
- [x] (RC) 12h × 6 motores: 4 de producción limpios; SQLite/Oracle eran config
      del harness, no Quark (validado). Sin hallazgos de producto.
