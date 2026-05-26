---
id: 0018
title: Oracle distributed migration lock via DBMS_LOCK (session-scoped), not a lock-table SELECT FOR UPDATE
status: accepted
date: 2026-05-26
implemented: F3-1 (Oracle); closes v1.0-gate §A Item 1 cluster MigrationLock
deciders: jcsvwinston
related: [0009]
supersedes: null
tags: [migrations, locking, oracle, phase-3, v1-gate]
---

# 0018 — Oracle migration lock: `DBMS_LOCK`

## Contexto

`Client.AcquireMigrationLock(ctx, name, timeout)` da un lock advisory
cluster-wide para serializar migraciones entre procesos. Los cinco dialectos
ya soportados lo implementan con un primitivo **session-scoped** y encajan en
una interfaz deliberadamente estrecha — `DBConn` expone sólo `ExecContext`,
`QueryRowContext` y `Close`, **sin control de transacción**:

- PostgreSQL: `pg_advisory_lock(hashtext(name))` (session-level).
- MySQL / MariaDB: `GET_LOCK(name, timeout)` (session-bound).
- MSSQL: `sp_getapplock @LockOwner='Session'`.

Oracle devolvía `ErrUnsupportedFeature`. Para v1.0 (gate §A Item 1) el
SharedSuite exige el contrato en Oracle: adquirir, liberar (idempotente),
exclusión mutua entre conexiones concurrentes, y `ErrLockTimeout` al expirar.

Dos hechos restringen la elección:

1. **Oracle hace auto-commit del DDL.** Una migración corre `CREATE TABLE` /
   `ALTER` que commitean implícitamente. El lock debe **sobrevivir a esos
   commits**.
2. **La interfaz del lock no expone transacciones** (`DBConn` no tiene
   `Begin`). Los cinco lockers existentes son session-scoped precisamente
   por eso.

## Decisión

**Implementar el lock Oracle con `DBMS_LOCK`** — el primitivo advisory
session-scoped de Oracle, análogo directo de `pg_advisory_lock`/`GET_LOCK`:

- `DBMS_LOCK.ALLOCATE_UNIQUE(name, handle)` mapea el nombre del lock a un
  handle (determinista por nombre, así que `Release` re-deriva el handle en la
  misma sesión sin necesidad de transportarlo).
- `DBMS_LOCK.REQUEST(handle, lockmode => X_MODE, timeout => secs,
  release_on_commit => FALSE)` toma el lock exclusivo. **`release_on_commit =>
  FALSE`** es la clave: el lock es de sesión y NO se suelta en commit, así que
  sobrevive a los commits implícitos del DDL.
- Conexión dedicada por lock (igual patrón que los otros): se sostiene durante
  la vida del lock; `Release` corre `DBMS_LOCK.RELEASE` y devuelve la conexión
  al pool. Idempotente vía flag `released`.
- Códigos de `REQUEST`: `0` adquirido / `4` ya lo posee esta sesión → éxito;
  `1` → `ErrLockTimeout`; `2/3/5` → error. Timeout en segundos enteros
  (granularidad de `DBMS_LOCK`); sub-segundo redondea a 1s; timeout ≤ 0 usa
  `MAXWAIT` (32767s).

**Prerrequisito de privilegio:** `DBMS_LOCK` no está concedido a los usuarios
de esquema por defecto. El operador debe ejecutar, una vez, como `SYSDBA`:

```sql
GRANT EXECUTE ON SYS.DBMS_LOCK TO <quark_user>;
```

El contenedor de test aplica este grant en el arranque; queda documentado en
`website/docs/guides/migrations.mdx` como requisito de Oracle.

## Consecuencias

**Positivas:**
- Encaja la interfaz session-scoped existente sin ensancharla — paridad real
  con los otros cinco dialectos, mismo contrato del SharedSuite.
- Sobrevive a los commits implícitos del DDL de Oracle (`release_on_commit =>
  FALSE`), que es exactamente lo que una migración necesita.
- `DBMS_LOCK` es el primitivo idiomático; semántica de timeout/wait nativa.

**Negativas:**
- **Requiere `GRANT EXECUTE ON DBMS_LOCK`** — fricción de adopción: el DBA
  debe concederlo una vez. Muchos entornos Oracle restringen `DBMS_LOCK`.
  Mitigado documentándolo como prerrequisito explícito.
- Timeout con granularidad de segundos (no sub-segundo).

## Alternativas consideradas

1. **Lock-table con `SELECT … FOR UPDATE WAIT n`.** Privilege-free (no necesita
   grant), patrón usado por algunas herramientas en Oracle. **Rechazado** por
   un choque estructural: `FOR UPDATE` es **transaction-scoped** — el lock se
   suelta al commit. Sostenerlo requiere una transacción abierta, y la interfaz
   `DBConn` del locker **no expone control de transacción** (es session-scoped
   por diseño, como los otros cinco). Además, en modo autocommit el `FOR
   UPDATE` se soltaría tras la propia sentencia. Encajarlo obligaría a
   ensanchar la interfaz de lock con `Begin`/`Commit`/`Rollback` sólo para
   Oracle — peor que pedir un grant. (El auto-commit del DDL no afectaría al
   lock porque corre en otra conexión, pero el problema del autocommit de la
   propia conexión del lock y la falta de `Begin` en la interfaz lo descarta.)
2. **Tabla de lock con test-and-set (`INSERT` PK + commit).** Da exclusión
   pero **falla rápido** (unique violation) en vez de bloquear-con-timeout;
   replicar la espera exigiría polling. Rechazado: semántica peor que el
   contrato (`block up to timeout`).
3. **Seguir devolviendo `ErrUnsupportedFeature`.** Rechazado: Quark se posiciona
   como "el ORM con Oracle real" (gate §A Item 1); un lock de migración es
   parte de esa promesa.

## Restricciones que esta decisión impone

- El usuario de migraciones de Oracle **debe** tener `EXECUTE ON DBMS_LOCK`.
- El lock es session-scoped y se libera si la sesión muere (crash del proceso
  → la conexión dedicada cae → Oracle suelta el lock), cumpliendo el contrato
  de `MigrationLock` de release-on-teardown.
- El timeout se redondea a segundos enteros.

## Cuándo reabrir

- Si se ensancha la interfaz de lock para soportar transacciones (entonces el
  lock-table `FOR UPDATE` privilege-free sería viable y evitaría el grant).
- Si emerge demanda de timeout sub-segundo en Oracle.
