# F8 — Hooks / Eventos / Audit

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F8.
> (No hay playbook dedicado; la superficie vive en `tx.go`, `events.go`,
> `audit.go`, `hooks.go` — ADR-0013.)

## Qué prueba

La semántica transaccional de hooks, EventBus y audit log, por motor.

## Grupos cubiertos (los 6 motores)

- **SavepointTruncation** — `tx.Tx` anidado de 5 niveles. El nivel 4 devuelve
  error (su savepoint hace rollback, arrastrando el 5); el nivel 3 traga el
  error y commitea. Se verifica que **tanto las filas como los hooks
  `OnCommit`** de los niveles 4-5 se descartan, y que los niveles 1-3
  sobreviven con `OnCommit` en orden FIFO. (Truncación de la cola de hooks por
  savepoint, ADR-0013.)
- **OnCommitOnRollback** — en commit dispara `OnCommit` (no `OnRollback`) y
  viceversa; un callback `OnCommit` que devuelve error se **loguea, no es
  fatal** (el dato queda committeado).
- **EventBus** — un `Create` publica un evento `created` post-commit. Un error
  del bus **no hace rollback** del write: se persiste el dato y el fallo
  aflora como **`ErrEventEmitFailed`** (at-least-once, sin outbox — ADR-0013;
  el bus error no se traga en silencio, se devuelve para que el caller sepa
  que el evento se perdió).
- **AuditLog** — `EnableAuditLog` + N `Create` individuales → N filas en
  `quark_audit` (delta), cada una con un `diff` no vacío. Escalado desde los
  100k del spec (logueado).
- **AuditAtomicity** — el audit se escribe **inline en la misma tx**, así que
  una tx revertida no deja ni la fila de datos ni la de audit. (Sustituye al
  kill-9 del spec con un rollback.)
- **FindHooks** — `BeforeFind` aborta una lectura devolviendo error;
  `AfterFind` error propaga al caller.
- **TxFromContext** — un hook de lifecycle despachado vía `ForTx` alcanza la
  tx activa con `quark.TxFromContext(ctx)`.

## Fuera de scope (gaps documentados)

- **`BeforeFind` que muta el query** (`q.Where(...)`): **no está en la API**.
  El hook sólo recibe `ctx` y sólo puede **abortar** devolviendo error
  (`hooks.go`). F8 testea el abort; la mutación del query no existe.
- **100k writes / kill-9 real**: tier de F14 soak. F8 escala (logueado) y
  simula el crash con un rollback (la garantía de atomicidad es la misma:
  audit inline-in-tx).
- **Paths bulk** (`CreateBatch`/`UpdateBatch`/`DeleteBatch`/`UpdateMap`/
  `DeleteBy`) **no se auditan ni emiten eventos** — mismo límite de scope que
  los hooks (audit.go). F8 usa `Create` individual.

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestHooks -v ./phases/f08_hooks/                       # SQLite
go test -tags=bugbash -run TestHooks -v ./phases/f08_hooks/ -engines=all -timeout 40m
```

## Hallazgos (en `TASKS.md` § "Bug-bash hallazgos")

- **BB-9** (cerrado) — savepoints no dialect-aware: `Tx.Savepoint`/`RollbackTo`/
  `ReleaseSavepoint` emitían SQL ANSI sin condicionar al motor, así que las tx
  anidadas (`tx.Tx`) fallaban en MSSQL (`SAVE TRANSACTION`) y Oracle (sin
  `RELEASE SAVEPOINT`). Arreglado en el mismo PR con la interfaz opcional
  `SavepointDialect`; verificado por el grupo SavepointTruncation en los 6
  motores.

## Criterio done

- [x] Cero side-effects de tx revertidas (savepoint + audit atomicity).
- [x] Audit log coherente con los writes (delta exacto + diff válido).
- [x] OnCommit/OnRollback fire-or-discard + callback error tolerado.
- [x] EventBus post-commit + error no hace rollback (`ErrEventEmitFailed`).
- [x] FIFO de hooks post-commit; `TxFromContext` accesible desde hooks.
