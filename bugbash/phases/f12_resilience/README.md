# F12 — Resiliencia & concurrencia

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F12.
> Superficie: `tx.go` (retry loop + `runTxOnce` rollback-on-panic +
> `waitDeadlockBackoff`), `db_errors.go` (`isDeadlock`), `option.go`
> (`WithDeadlockRetry`, `WithMaxOpenConns`), `client.go` (`Raw()` → pool).

## Qué prueba

El comportamiento bajo carga adversa: agotamiento del pool, cancelación de
`context`, pánico en hooks dentro de tx, concurrencia masiva de tx, y deadlocks
reales con retry. Por motor.

## Grupos cubiertos

> Cobertura de CI: SQLite + PG + MySQL + MariaDB + MSSQL. Oracle corre a mano
> (`-engines=oracle`) pero está excluido del run de CI por el image issue —
> mismo caveat que el resto del bug-bash.

- **PoolExhaustionWaits** — `WithMaxOpenConns(5)` + 50 goroutines
  lectoras: `database/sql` **encola** esperando conexión, ninguna crashea, el
  cap se respeta (`Stats().MaxOpenConnections==5`) y el pool drena a
  `InUse==0` al final.
- **ContextCancelReleasesConn** — un `context` cancelado aflora como
  error y **devuelve la conexión al pool** (`InUse==0`); el cliente sigue usable.
- **PanicInHookRollsBack** — dentro de `client.Tx`: primero un write
  con éxito (dato + fila de audit inline), luego `BeforeUpdate` **pánica**.
  `runTxOnce` hace rollback (ambas filas desaparecen) y libera la conexión; el
  pánico **se re-propaga** al caller. Verifica rollback + audit-no-escrito +
  `InUse==0`.
- **ConcurrentTxNoLeak** (6 motores) — 200 goroutines abren `client.Tx` con un
  savepoint anidado (`tx.Tx`) y escriben una fila; 1 de cada 10 pánica
  (recuperado por goroutine). Sin leak de conexiones (`InUse==0`), sin leak de
  goroutines (best-effort, `CategoryGap`), y commitean **exactamente** las no-
  pánicas. SQLite es single-writer → su pool se capa a 1 conexión (serializa;
  `SQLITE_BUSY` bajo escritura concurrente es límite del motor, no de Quark).
- **DeadlockRetryRecovers** (5 motores servidor; SQLite skip logueado) — dos tx
  toman locks de filas A/B en orden inverso y deadlockean; `WithDeadlockRetry(6)`
  reintenta a la víctima y **ambas** commitean sin error. Una barrera de canales
  (sólo en el primer intento; los retries la saltan) fuerza el deadlock de forma
  determinista.

## Notas de implementación

- El spec dice "`Client.Stats()`"; la API real expone el pool vía
  **`Client.Raw().Stats()`** (`*sql.DB`). Las aserciones de pool/leak usan eso.
- `isDeadlock` (`db_errors.go`) reconoce PG `40P01`, MySQL/MariaDB `1213`,
  MSSQL `1205`, Oracle `60`. **No** reconoce `SQLITE_BUSY` — por diseño: SQLite
  serializa escrituras, no produce deadlocks de lock-ordering.

## Fuera de scope (logueado)

- **Reconexión tras drop de red** (`docker network disconnect`) y el **soak de
  30 min** del spec: tier **F14**. F12 fuerza los mismos modos de fallo de forma
  determinista y a pequeña escala.
- **Escala:** el spec pide 1000 goroutines concurrentes; F12 usa 200 (satura un
  pool de 8 y produce pánicos aleatorios dentro del timeout de CI). 1000 es
  tier F14.
- Detección de leak de goroutines: best-effort (`runtime.NumGoroutine` con
  tolerancia), `CategoryGap` — los drivers pueden mantener workers vivos.

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestResilience -v ./phases/f12_resilience/                          # SQLite
go test -tags=bugbash -run TestResilience -v ./phases/f12_resilience/ -engines=all -timeout 40m
```

## Hallazgos (en `TASKS.md` § "Bug-bash hallazgos")

**Sin hallazgos.** Pasada cross-engine 2026-06-01 (Docker): verde en los 5
motores de CI (SQLite + PG + MySQL + MariaDB + MSSQL). El retry de deadlock,
el rollback-on-pánico (con audit inline), la liberación de conexión tras
cancelación, y la ausencia de leaks bajo 200 tx concurrentes son sólidos. La
fase es test-only (no destapó bug → sin cambio de código).

## Criterio done

- [x] Pool exhaustion: los callers esperan, no crashean; `InUse==0` al final.
- [x] Cancelación de `context`: error + conexión liberada; cliente usable.
- [x] Pánico en hook dentro de tx: rollback (dato + audit) + conexión liberada.
- [x] Concurrencia de tx con pánicos aleatorios: sin leaks; commit selectivo.
- [x] Deadlock real recuperado por `WithDeadlockRetry` (5 motores servidor).
