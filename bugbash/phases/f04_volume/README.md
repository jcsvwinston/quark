# F4 — Volumen

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F4.
> Playbook: [`docs/playbooks/query-builder.md`](../../../docs/playbooks/query-builder.md)
> (List cap implícito, streaming, chunking de batch).

## Qué prueba

Que el query builder se comporta bajo un dataset de volumen por motor: el cap
implícito de `List()`, la paginación profunda, el streaming server-side, y —
sobre todo— que `CreateBatch` chunkea para no reventar el techo de
bind-parameters de cada motor.

## Grupos cubiertos (los 6 motores)

- **ListImplicitCap** — `List()` sin `Limit()` trunca al cap de seguridad
  documentado (100 filas), **no** devuelve las 5000 ni hace OOM; un `Limit()`
  explícito anula el cap. (Cita `query_builder.go` / `query_exec.go` — el cap
  vive en `List()`; ver playbook query-builder § "List() con resultado truncado
  silenciosamente".)
- **DeepOffsetPagination** — recorre el namespace completo con
  `Limit(500)+Offset` y verifica que la unión de páginas es exactamente el set
  completo: sin huecos, sin duplicados, ids estrictamente crecientes.
- **CursorFullScan** — `Cursor()` streamea el set completo server-side y cierra
  limpio (`Err()` y `Close()` sin error).
- **IterEarlyStop** — `Iter()` propaga verbatim el error del callback y se
  detiene en esa fila; un `Iter()` sin error visita todo el set.
- **IterContextCancel** — cancelar el `context` a mitad de stream detiene
  `Iter()` antes de drenar el set y aflora un error. Es **driver-dependiente**
  (las filas pueden venir prefetcheadas), así que un fallo aquí es `gap`, no
  `regression`. Verde en los 5 motores de CI.
- **PaginateExactCount** — `Paginate()` reporta `Total` **exacto** sobre el
  namespace y una última página parcial correcta (`TotalPages`, tamaño del
  último page).
- **CreateBatchChunking** — un único `CreateBatch(10000)` debe tener éxito en
  todos los motores. Con 7 columnas insertables son 70 000 placeholders, por
  encima del techo de MSSQL (~2100), SQLite (32766) y PG/MySQL (65535). Es el
  **finder** del bug de chunking ausente (BB-10). Tras el fix, las 10 000 filas
  aterrizan en todos los motores.

Los sub-tests de un motor servidor **comparten la tabla**, así que cada uno
acota sus aserciones a su propio namespace `org_id` (read=1, batch=2), nunca a
counts absolutos.

## Fuera de scope (escalado, logueado)

- **1M orders / 5M order_lines** del spec, el presupuesto de **memoria** (2×
  peak con `runtime.MemStats`) y el de **latencia** (p50<50ms / p99<500ms): son
  tier **F14 soak**. F4 siembra unos miles de filas — suficiente para ejercitar
  el cap, los paths de streaming y el techo de parámetros, no la envolvente de
  latencia.
- **Offset N=100k** del spec: el offset profundo se ejercita dentro del set
  sembrado (no a 100k), por la misma razón de escala.

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestVolume -v ./phases/f04_volume/                          # SQLite
go test -tags=bugbash -run TestVolume -v ./phases/f04_volume/ -engines=all -timeout 40m
```

## Hallazgos (en `TASKS.md` § "Bug-bash hallazgos")

- **BB-10** (cerrado) — `CreateBatch` no chunkeaba: emitía un único
  `INSERT … VALUES` con `filas × columnas` placeholders, reventando el techo de
  bind-parameters (MSSQL ~2100 a unos cientos de filas anchas; SQLite/PG/MySQL a
  unos miles). `DeleteBatch` ya chunkeaba; `CreateBatch` no. Arreglado en el
  mismo PR (chunking a `maxBatchBindParams=2000` por statement, loop sobre el
  executor ligado); verificado por este grupo en SQLite + PG + MySQL + MariaDB +
  MSSQL. Oracle no se ve afectado (usa loop de INSERTs single-row).

## Criterio done

- [x] `List()` cap implícito enforced; `Limit()` explícito lo anula.
- [x] Paginación profunda sin huecos/duplicados; orden estable entre páginas.
- [x] `Cursor()`/`Iter()` streamean el set completo y cierran limpio.
- [x] `Iter()` corta con error de callback; cancelación de `context` observada.
- [x] `Paginate()` con `Total` exacto y última página parcial correcta.
- [x] `CreateBatch(10000)` éxito en los 5 motores de CI (chunking, BB-10).
