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

### Lock distribuido (F3-1 — cerrado)

`Client.AcquireMigrationLock(ctx, name, timeout)` da exclusión cross-proceso, opt-in (NO se llama solo desde `Migrate`). Implementado en 5 motores con primitivos session-scoped: PG `pg_advisory_lock`, MySQL/MariaDB `GET_LOCK`, MSSQL `sp_getapplock`, **Oracle `DBMS_LOCK` (ADR-0018; requiere `GRANT EXECUTE ON DBMS_LOCK`)**. SQLite devuelve `ErrUnsupportedFeature` (single-writer; usa `BEGIN IMMEDIATE` en proceso). Detalle por motor en `dialect_migration_lock.go`; contrato en `migration_lock_integration_test.go` (SharedSuite). Timeout → `ErrLockTimeout`.

**Anti-pattern**: cualquier nuevo locker debe ser session-scoped (la interfaz `DBConn` no expone transacciones — por eso el `FOR UPDATE` transaction-scoped no encaja; ver ADR-0018 §Alternativas).

### Down se escribe a mano siempre

No hay diff reversible automático. `Migration.Down` lo escribe el desarrollador. Si se equivoca, el rollback puede romper la base. Atlas/Alembic generan el down a partir del diff de schema; Quark hoy no.

### Dos registries — un cerrado (F3-7), uno aún pendiente

Quark tiene HOY dos conceptos de "registry":

1. **Model registry per-Client** (`client_registry.go`, cerrado en F3-7).
   `Client.RegisterModel(...)` + `Client.RegisteredModels()` per-instance,
   mutex-protegido. Multi-tenant safe — cada Client gestiona su propio
   model set sin cross-contamination. Esto NO toca el global type-meta
   cache de `internal/schema` (`modelRegistry sync.Map`), que es correcto
   como global state porque la meta es determinista per `reflect.Type`.

2. **Versioned migration registry global y mutable** (`migrate/migrate.go:19` —
   `var registry = map[string]*Migration{}` con `Reset()` para tests).
   Anti-patrón sigue vivo: dos clientes en el mismo proceso comparten
   este registry. Si Nucleus instancia un cliente por tenant + un cliente
   para datos de admin compartidos en el mismo proceso, ambos ven las
   mismas migraciones versionadas. F3-7 NO cierra esto — el scope de F3-7
   fue intencionalmente aditivo (el model registry per-Client) y no tocó
   el global de migraciones versionadas.

Pendiente (Fase 4+): mover el global de `migrate/migrate.go` a per-Client
también. Patrón a seguir: el mismo de F3-7 (`Client.RegisterMigration(...)`
+ `Client.RegisteredMigrations()`).

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

### PK en el pipeline de planes: el flag viaja aparte del tipo (F3-2-pk)

`Column.PrimaryKey` transporta la pertenencia al PK (true en cada miembro de
una clave compuesta); `Column.Type` es SIEMPRE el tipo desnudo, sin el
fragmento `PRIMARY KEY`/identity. Reglas al tocar este pipeline:

1. **El render del constraint tiene UNA fuente de verdad**:
   `internal/migrate.PKColumnSQL` (+ `ClassifyPKType` para clasificar desde el
   string de catálogo). El migrator (`SQLType(isPK:true)`) y
   `applyCreateTable` (executor de `OpCreateTable`) delegan ahí. No dupliques
   los fragmentos por dialecto.
2. **Los 6 introspectores deben poblar `PrimaryKey`** (SQLite `PRAGMA pk>0`,
   MySQL/MariaDB `COLUMN_KEY='PRI'`, PG `table_constraints`+`key_column_usage`,
   MSSQL `sys.indexes is_primary_key=1`, Oracle `USER_CONSTRAINTS 'P'`). Si
   añades un introspector y no lo pueblas, TODO plan propondrá un
   `OpAlterColumn` de PK falso en cada tabla.
3. **`Diff` compara el flag; `ApplyPlan` rechaza cambios de PK** con
   `ErrUnsupportedFeature` (table rebuild) — el guard va ANTES del check de
   tipo para que un delta tipo+PK no emita un ALTER TYPE que ignore el PK.
4. **Oracle**: el catálogo reporta los identity PK como `NUMBER` pelado;
   `ClassifyPKType` lo trata como entero→identity a propósito (espejo del
   `oracleBareNumberMatch` del diff). No "arregles" eso sin leer el godoc.

### El diff compara módulo la decoración de cada catálogo (task_b03f2155)

`normalizeType` y `canonicalDefault` (`migrate_diff.go`) absorben la forma
propia con que cada catálogo devuelve tipos y defaults. Si añades un tipo o
tocas un introspector, el invariante a preservar es: **`Migrate(models)` →
`PlanMigration(models)` devuelve plan VACÍO en los 6 motores** (lo pinnea
`RoundTrip_RichFixture` en la SharedSuite con m2m + defaults bool/string +
`time.Time`). Equivalencias vivas:

- Tipos: PG `timestamp without time zone`≡`TIMESTAMP`; MySQL/MariaDB
  `tinyint(1)`≡`BOOLEAN` (sólo el marcador bool; `tinyint(4)` es int real);
  Oracle `TIMESTAMP(6)`≡`TIMESTAMP` (sólo la precisión POR DEFECTO).
- Defaults: parens de MSSQL (`((1))`≡`1`), cast de PG (`'x'::text`≡`'x'`),
  unquote de MySQL (`member`≡`'member'`), case de bools (`true`≡`TRUE`).
  El CONTENIDO string sigue case-sensitive (`'Active'`≠`'active'`).
- Las join tables m2m van EN el desired (`modelsToSchema` las sintetiza con
  la forma exacta de `createJoinTables`); un modelo explícito de la join
  table gana sobre la síntesis. Sin esto el diff proponía DROPearlas
  (pérdida de datos vía el flujo documentado de drift-check).

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

### Introspección: MariaDB reporta `COLUMN_DEFAULT = "NULL"` (string literal)

(BB-11, cerrado.) En MariaDB, `INFORMATION_SCHEMA.COLUMN_DEFAULT` devuelve el
**string literal `"NULL"`** para una columna nullable sin default (MySQL devuelve
NULL real → `sql.NullString{Valid:false}`). Sin normalizar, `PlanMigration`
emite un `OpAlterColumn` falso-positivo (`default "NULL"→<nil>`) en cada columna
así, rompiendo el invariante "plan vacío sin cambios". `mysqlLikeIntrospect`
(`dialect_introspection.go`) normaliza el literal **acotado a `dialectName ==
"mariadb"`** (MySQL debe conservar un `DEFAULT 'NULL'` real). Si tocas el
introspector MySQL/MariaDB, no rompas esta normalización.

### Migrator versionado: DDL del bookkeeping table debe ser per-dialecto

(BB-12, cerrado.) `Migrator.Init` (`migrate/migrate.go`) creaba
`quark_migrations` con `CREATE TABLE IF NOT EXISTS … TIMESTAMP …` — roto en
MSSQL (no tiene `IF NOT EXISTS`; su `TIMESTAMP` es rowversion) y Oracle. Ahora es
per-dialecto (MSSQL `IF NOT EXISTS (SELECT … sys.tables)` + `DATETIME`; Oracle
`VARCHAR2` + swallow ORA-00955), vía `Raw` como `GetApplied`. Mismo patrón que
`ensureMigrationStateTable`/`ensureBackfillStateTable`. Cualquier tabla interna
nueva debe seguir este patrón, no asumir DDL portable.

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
