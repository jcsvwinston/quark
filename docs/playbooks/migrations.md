---
type: playbook
module: migrations
files:
  - migrator.go
  - sync.go
  - migrate/migrate.go
  - internal/migrate/migrate.go
  - internal/db/introspection.go
last_review: 2026-05-10
related_adrs: [0001]
related_p0: []
phase: 0
---

# Playbook: Migraciones

## Qué cubrimos

Quark tiene **dos sistemas paralelos** de migración:

1. **Auto-migración** (estilo GORM `AutoMigrate`): `migrator.go` + `sync.go`. Genera `CREATE TABLE IF NOT EXISTS` desde reflect, soporta composite PK, índices, FKs, join tables M2M. `Sync` hace introspección (`internal/db/introspection.go`) y compara columnas: añade nuevas, renombra (vía tag `quark:"rename:old"`), dropa si `SafeMigrations=false`.

2. **Migraciones versionadas** (estilo Flyway/Alembic): `migrate/migrate.go`. Registry global `map[string]*Migration` (`migrate/migrate.go:19`), tabla `quark_migrations`, `Up`/`Down`/`UpDryRun`.

Los dos coexisten. Auto-migración es el "modo desarrollo / pequeño proyecto"; migraciones versionadas son para equipos con disciplina de release.

## Bugs P0 vivos

Ninguno crítico hoy en este módulo. Pero hay **deuda estructural** que debe ser conocida — ver §Anti-patterns y §Limitaciones.

## Limitaciones críticas (NO publicitar más allá de lo que son)

### `Sync` no detecta drift real

**Comportamiento actual** (`sync.go:84`): sólo compara **nombres de columnas**. Si cambias en el modelo:

- `VARCHAR(255)` → `TEXT`
- `nullable` → `NOT NULL`
- añades `default`
- añades/cambias índices
- añades/cambias FKs
- añades CHECK constraints

**`Sync` no nota nada** y la base de datos queda silenciosamente desincronizada del modelo Go. **Documenta esto en `website/docs/migrations/auto.md` con un warning visible.**

El plan: introspección completa + diff estructural en Fase 3 (`docs/ANALISIS_MADUREZ.md` §4 Fase 3).

### Sin lock distribuido

Dos pods aplicando `Up` en paralelo es **race condition garantizada**. Los locks que existen en cada motor (PG `pg_advisory_xact_lock`, MySQL `GET_LOCK`, MSSQL `sp_getapplock`, Oracle `DBMS_LOCK`) **no se usan**.

Hoy: documenta en producción que las migraciones deben ejecutarse desde un único pod (job de migración separado, no desde el pod de la app). Fase 3 lo arregla.

### Down se escribe a mano siempre

No hay diff reversible automático. `Migration.Down` lo escribe el desarrollador. Si se equivoca, el rollback puede romper la base. Atlas/Alembic generan el down a partir del diff de schema; Quark hoy no.

### Registry global mutable

`migrate/migrate.go:19` — `var registry = map[string]*Migration{}` con `Reset()` para tests. Anti-patrón: dos clientes en el mismo proceso comparten registry. Si Nucleus instancia un cliente por tenant + un cliente para datos de admin compartidos en el mismo proceso, ambos ven las mismas migraciones.

Plan Fase 3: registry por `*Client`.

### Migrator versionado NO envuelve `Up` en transacción (en MySQL)

En motores con DDL transaccional (PG, MSSQL), `Up` queda en transacción si el migrator la abre. **Pero MySQL no soporta DDL transaccional**, así que un `Up` que falla a media migración deja el schema inconsistente sin marca de versión. La fila en `quark_migrations` no se inserta, pero el ALTER TABLE parcial queda aplicado.

`SupportsTransactionalDDL` se respeta para auto-migration (`sync.go:26`). El versionado debe seguir el mismo patrón.

### `internal/migrate.SQLType` muy pobre

Mapeo Go → SQL en `internal/migrate/migrate.go:25-34`:
- `string` → `VARCHAR(255)` siempre, sin opción de longitud por tag.
- No mapea `decimal.Decimal` (a pesar de estar como indirect dependency).
- No mapea `uuid.UUID`.
- No mapea `time.Duration`.
- No mapea arrays.
- No JSON tipado.

Plan Fase 1: extender con `RegisterTypeMapper(reflect.Type, dialect, fn)` y permitir longitud por tag (`db:"name,size=512"`).

## Anti-patterns a vigilar

### `fmt.Sprintf` con nombre de tabla en SQL crudo

`migrate/migrate.go:43,55,102,166,197` usa `Sprintf` con `m.tableName`. Hoy `tableName` está hardcoded a `quark_migrations` en el constructor, así que no es inyectable, pero el patrón es feo. Si refactorizas para soportar nombre custom de tabla de migrations, debe pasar por validación de identifier.

### Asumir DDL transaccional

```go
// MAL — falla en MySQL
tx.Exec("ALTER TABLE x ADD COLUMN y INT")
tx.Exec("ALTER TABLE x ADD COLUMN z INT")
tx.Commit()

// BIEN — verifica capability
if dialect.SupportsTransactionalDDL() {
    // tx-style
} else {
    // sin tx; cada DDL es un commit; documentar al usuario
}
```

### `t.Skip` para gatear tests por motor

Anti-pattern explícitamente prohibido por `CLAUDE.md` regla #7. Si tu test sólo aplica a Postgres, usa testcontainers (cuando esté setup F0-8) o build tag `//go:build integration_postgres`.

## Decisiones que afectan al módulo

- **ADR 0001 (Active Record)**: las migraciones operan sobre structs reflect-readable; el `migrator` no asume Unit of Work.

## Roadmap de mejora

- **Fase 1**: `SQLType` extensible, longitud por tag, mapeo de decimal/UUID/Duration.
- **Fase 3**: 
  - Schema diff real (tipos, NOT NULL, defaults, índices, FKs, checks).
  - `quark schema diff` que emite migración up+down candidata.
  - Lock distribuido (PG `pg_advisory_xact_lock`, MySQL `GET_LOCK`, MSSQL `sp_getapplock`, Oracle `DBMS_LOCK.REQUEST`).
  - Migración transaccional con resume en MySQL via state checkpointing.
  - Dry-run con plan de cambios estilo `terraform plan`.
  - Backfill orquestado: `Migration.Backfill(fn func(*Tx) error, batchSize int)` con resume token.
  - Registry por `*Client` (deja de ser global).

## Tests críticos a no romper

- `internal/migrate/migrate_test.go` — tipos PK por motor.
- `migrate/migrate_test.go` — orden de migraciones, idempotencia.
- `sync_test.go` — auto-migration por dialecto.

## Cuándo invocar al `code-reviewer`

Antes de cualquier PR que toque `migrator.go`, `sync.go`, `migrate/`, o `internal/db/introspection.go`. El reviewer vigila especialmente: que `Sync` no afirma capacidades que no tiene, que el registry se queda local al cliente cuando se introduzca, que SQL crudo pasa por validación, y que cambios en `SQLType` cubren los 6 motores.
