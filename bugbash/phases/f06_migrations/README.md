# F6 — Migraciones (schema-as-code)

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F6.
> Playbook: [`docs/playbooks/migrations.md`](../../../docs/playbooks/migrations.md).
> Superficie: `migrate_plan.go` (`PlanMigration`/`Plan`/`Diff`), `migrate_execute.go`
> (`ApplyPlan`, checkpoint resumable), `migrate_backfill.go` (`BackfillSpec`/
> `Backfill`), `migrate/migrate.go` (`Migrator.Up`/`Down`), `migration_lock.go`
> (`AcquireMigrationLock`). ADR-0001 / ADR-0018.

## Qué prueba

El ciclo de schema-as-code: plan/diff, apply, backfill con resume, migración
versionada Up/Down, y lock de migración distribuido. Por motor.

## Grupos cubiertos

- **PlanAndApply** — `PlanMigration` da plan **vacío** cuando el modelo coincide
  con el schema (cero falsos positivos); añadir una columna produce el diff
  exacto (`OpAddColumn` que menciona la columna); `ApplyPlan` lo aplica y el
  siguiente `PlanMigration` vuelve a estar vacío; la columna nueva round-trip.
- **BackfillResumes** — `Backfill` rellena `legacy_id` por PK en batches; un
  `Process` que falla a mitad deja un **resume token** (filas parciales) y una
  re-ejecución con el mismo `Name` completa desde el último PK exitoso (la
  garantía F3-4-resumable; sustituye al `kill -9` del spec).
- **VersionedUpDown** — una migración registrada en el registry global aplica
  vía `Migrator.Up(ctx,1)` y revierte vía `Migrator.Down(ctx,1)`.
- **LockSerializes** — dos goroutines compiten por `AcquireMigrationLock`; la
  segunda obtiene `ErrLockTimeout` mientras la primera lo retiene, y lo consigue
  tras el release. SQLite no tiene lock cross-proceso (single-writer) →
  `ErrUnsupportedFeature`, logueado y omitido.

Cada sub-test posee su(s) propia(s) tabla(s) y las dropea primero, para que las
mutaciones de schema no colisionen en una BD de motor servidor compartida.

## Nota sobre TASKS

`TASKS.md` aún lista F3-3/4/5/6 (schema diff core, migración transaccional+
resumable, CLI plan/apply, backfill) como **abiertos**, pero `PlanMigration` /
`ApplyPlan` / `Backfill` están implementados y con tests de integración en el
módulo raíz (`migrate_plan.go`, `migrate_execute.go`, `migrate_backfill.go`,
`plan_integration_test.go`, `backfill_integration_test.go`). Los marcadores
están **stale**, no es feature ausente. F6 lo verifica empíricamente. (Marcado
para higiene de TASKS en las notas del PR.)

## Fuera de scope (logueado)

- **CLI `quark migrate plan/verify/apply` (F3-5)**: la fase ejercita la API
  (`PlanMigration`/`ApplyPlan`); la envoltura CLI (`quarkmigrate.Run`) la cubren
  los tests del root.
- **Backfill `kill -9` real**: sustituido por un `Process` que falla a mitad +
  re-run (mismo mecanismo de resume token, sin matar el proceso).
- **Down auto-reversible**: `Migration.Down` se escribe a mano (playbook); F6
  registra un Down explícito.

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestMigrations -v ./phases/f06_migrations/                          # SQLite
go test -tags=bugbash -run TestMigrations -v ./phases/f06_migrations/ -engines=all -timeout 40m
```

## Hallazgos (en `TASKS.md` § "Bug-bash hallazgos")

Pasada cross-engine 2026-06-03 (Docker). Destapó y **cerró 2 bugs reales** en
el mismo PR; verde 4/4 en los 5 motores de CI tras los fixes.

- **BB-11** (cerrado) — `PlanMigration` daba un diff falso-positivo en **MariaDB**:
  `INFORMATION_SCHEMA.COLUMN_DEFAULT` reporta el default de una columna nullable
  sin default como el string literal `"NULL"` (MySQL reporta NULL real), así que
  el differ emitía un `OpAlterColumn` espurio (`default "NULL"→<nil>`) en cada
  columna así, rompiendo "plan vacío sin cambios". Fix en
  `dialect_introspection.go` (`mysqlLikeIntrospect`): normaliza el literal
  `"NULL"` a "sin default".
- **BB-12** (cerrado) — migraciones versionadas rotas en **SQL Server**:
  `Migrator.Init` emitía `CREATE TABLE IF NOT EXISTS quark_migrations
  (… TIMESTAMP …)` (MSSQL no tiene `IF NOT EXISTS` y su `TIMESTAMP` es
  rowversion). Fix en `migrate/migrate.go`: DDL de la tabla de bookkeeping
  per-dialecto (MSSQL `sys.tables` guard + `DATETIME`; Oracle `VARCHAR2` +
  swallow ORA-00955), vía `Raw` como `GetApplied`.

Nota de harness: la pasada inicial mostró ruido de tablas-de-sistema en MySQL
(la DSN del bug-bash conecta a la BD `mysql`) y un token de backfill stale (se
dropeaba `quark_migration_state` en vez de `quark_backfill_state`); ambos eran
defectos del test, corregidos (las aserciones se acotan a la tabla propia; se
dropea la tabla de estado correcta).

## Criterio done

- [x] Plan vacío sin cambios; diff exacto al añadir columna; ApplyPlan resuelve.
- [x] Backfill rellena por PK y **resume** tras fallo a mitad.
- [x] Migración versionada Up/Down (BB-12 arreglado para MSSQL).
- [x] Lock de migración serializa (ErrLockTimeout); SQLite → ErrUnsupportedFeature.
