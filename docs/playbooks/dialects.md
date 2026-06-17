---
type: playbook
module: dialects
files:
  - dialect.go
last_review: 2026-05-10
related_adrs: [0005]
related_p0: []
closed_p0: [P0-2]
phase: 0
---

# Playbook: Dialectos SQL

## Qué cubrimos

Seis dialectos: **SQLite** (con dos drivers, `mattn/go-sqlite3` y `modernc.org/sqlite`), **PostgreSQL** (`pgx/v5`), **MySQL** y **MariaDB** (`go-sql-driver/mysql`; MariaDB embebe MySQLDialect + extras), **MSSQL** (`microsoft/go-mssqldb`), **Oracle** (`sijms/go-ora/v2`).

Cada `Dialect` provee:

- `Placeholder(n int) string` — `?` (MySQL/SQLite), `$n` (Postgres), `@p{n}` (MSSQL), `:{n}` (Oracle).
- `Quote(identifier) string` — `"x"` / `` `x` `` / `[x]`.
- `LimitOffset(limit, offset) string` — sintaxis específica (Postgres `LIMIT/OFFSET`, MSSQL/Oracle `OFFSET ... FETCH NEXT ... ROWS ONLY`).
- `RETURNING` — disponible en Postgres y SQLite (3.35+), simulado con `OUTPUT INSERTED` en MSSQL, no soportado en MySQL/MariaDB/Oracle.
- `LastInsertIDQuery` — `last_insert_rowid()` (SQLite), `LASTVAL()` (PG con secuencia), `SCOPE_IDENTITY()` (MSSQL), driver-level (MySQL).
- `JSONExtract`, `UpsertSQL`, `BuildRoutineQuery`, DDL básico (`AlterTable*`, `RenameColumn`, `RenameTable`).

Registro de dialectos custom: `RegisterDialect("vertica", verticaDialect)` (`dialect.go:859`).

## Bugs P0 vivos

(ninguno en este módulo; ver § Historial.)

## Historial — bugs cerrados

### P0-2 · `JSONExtract` concatenaba el path (cerrado)

`Dialect.JSONExtract` cambió de `(column, path string) string` a `(column, path string) (sql string, args []any, err error)`. SQL fragment usa `?` como marker neutral; `query_exec.go:substitutePathMarkers` los traduce al placeholder de cada motor en build time. Cada dialecto llama `guard.ValidateJSONPath` antes del bind. Detalles del fix y decisiones (rechazo de leading `$`, max 256 chars) en `docs/playbooks/security.md` § Historial.

**Impacto en custom dialects**: cualquier dialecto registrado vía `RegisterDialect("vertica", …)` debe actualizar la firma. Pre-1.0; sin uso conocido externo.

## Lo que está bien hecho (no romper)

### Upsert por dialecto correcto

- **PG/SQLite**: `INSERT ... ON CONFLICT (cols) DO UPDATE SET ...`.
- **MySQL/MariaDB**: `INSERT ... ON DUPLICATE KEY UPDATE ...`.
- **MSSQL/Oracle**: `MERGE ... USING (VALUES ...) ...` construido a mano (`query_crud.go:1074-1183`, `query_crud.go:1507-1606`).

Esto está por encima de bun y al nivel de ent. **No simplifiques esto** sin verificar que mantienes el comportamiento de los 6 motores.

### `OFFSET/FETCH` con `ORDER BY` automático en MSSQL/Oracle

`buildSelect` (`query_exec.go`, rama justo tras la cláusula ORDER BY explícita) inyecta un ORDER BY cuando hay LIMIT/OFFSET sin ORDER BY explícito y el dialecto es MSSQL/Oracle (lo exigen para `OFFSET/FETCH`). Usa el **PK** por defecto, pero cae a la posición ordinal **`ORDER BY 1`** cuando hay `DISTINCT`, `GROUP BY` o un **set-op** (UNION/INTERSECT/EXCEPT): esos restringen el ORDER BY a columnas del select-list o a un ordinal, y el PK no proyectado los rompe (ORA-01791/ORA-00979 en Oracle; "ORDER BY items must appear in the select list" bajo compound-select — Finding J). Si introduces nuevo path de paginación, sigue este patrón — un OFFSET sin ORDER BY en estos motores es un error sintáctico.

### MariaDB se autodetecta por versión de servidor (BB-3)

MariaDB no tiene driver `database/sql` propio (usa `go-sql-driver/mysql`, nombre "mysql"), así que `DetectDialect` no puede distinguirlo por nombre. `client.New` hace `SELECT VERSION()` una vez en conexiones "mysql" (`isMariaDBServer`) y cambia a `MariaDBDialect` si el server es MariaDB; un `WithDialect` explícito gana y salta el probe. Consecuencia: `MariaDBDialect.LockSuffix` emite `LOCK IN SHARE MODE` para `ForShare` (MariaDB no tiene `FOR SHARE` — `Error 1064`), y rechaza `ForShare`+`SkipLocked`/`NoWait` con `ErrUnsupportedFeature` (esa forma no admite modificadores). Si añades comportamiento dialect-divergente MariaDB↔MySQL nuevo, ponlo en `MariaDBDialect` (override), no en `MySQLDialect`.

**Oracle + lock pesimista (BB-4):** Oracle prohíbe combinar el row-limiting clause (`OFFSET/FETCH`) con `FOR UPDATE`/`SKIP LOCKED`/`NOWAIT` — **ORA-02014**. `buildSelect` detecta `!q.lock.IsZero() && dialect=="oracle"` y activa `suppressRowLimit`, que inhibe **tanto el OFFSET/FETCH como el ORDER BY implícito** (sin row-limiting, Oracle no exige ORDER BY). El cap implícito de `List()` se descarta (lock sobre todas las filas, con WARN); un `Limit`/`Offset` explícito —o `First()`, que aplica `Limit(1)`— junto al lock devuelve `ErrUnsupportedFeature`. Sólo Oracle; MSSQL usa table hints y sí convive con OFFSET/FETCH.

### Wrapper `timeScanner` para MySQL

`query_exec.go:27-71`. MySQL en algunos drivers/configs devuelve `[]byte` para columnas `DATETIME` en lugar de `time.Time`. El wrapper parsea cuatro formatos. **No quites este código sin verificar primero qué devuelve cada driver para columnas de tiempo en su matriz de configuración.**

## Anti-patterns a vigilar

### Asumir un placeholder

```go
// MAL
sql := fmt.Sprintf("SELECT * FROM users WHERE id = ?")

// BIEN
sql := fmt.Sprintf("SELECT * FROM users WHERE id = %s", dialect.Placeholder(1))
```

Cualquier SQL nuevo construido en el código debe usar `dialect.Placeholder(n)`. Buscar `?` hardcoded en el código fuera de tests es un anti-pattern detectable.

**Excepción documentada**: `Dialect.JSONExtract` devuelve un fragmento con `?` como **marker neutral** que `query_exec.go:substitutePathMarkers` traduce a placeholders dialect-specific en render time. Es deliberado y centralizado — no lo extiendas a otros sitios sin discutirlo, mantén la regla general "usar `dialect.Placeholder(n)` directamente".

### Asumir un quoting

```go
// MAL
sql := "SELECT * FROM \"users\""

// BIEN
sql := "SELECT * FROM " + dialect.Quote("users")
```

### Oracle uppercasea identifiers automáticamente

`dialect.go:622` (Oracle dialect). Esto rompe esquemas con identifiers entre comillas case-sensitive. Es deuda conocida — no hay opción para desactivarlo. Si emerges con un caso de uso que exija lower-case Oracle, abre issue: la solución requiere un flag por dialecto.

### `maxIdentifierLen=64` rompe Postgres (63 max)

Hoy `internal/guard/guard.go` tiene 64. Postgres rechaza identifiers de 64+ caracteres (truncará silenciosamente o errará según versión). Oracle ≤ 30 (legacy) o 128 (12c+). MSSQL 128. **No es configurable por dialecto** hoy. Es deuda — cuando lo arregles, hazlo por dialecto, no global.

### Sin tipos nativos Postgres

Hoy:
- **Sin arrays nativos** (`int[]`, `text[]`). Los slices Go no se mapean.
- **Sin UUID nativo** — se cuela como `VARCHAR(36)` en `internal/migrate/migrate.go:25-34`.
- **Sin `tstzrange`, `daterange`, `inet`, `hstore`, `bytea` tipado.**

ADR 0002 (reflect → codegen Fase 6) lo tendrá más fácil de resolver con codegen. Hasta entonces, requieren `pgtype.Array`/`pgtype.UUID` envueltos por el usuario.

### Interfaz `Dialect` no es ortogonal

Mezcla SQL builder + DDL + procedures + JSON. Cuando MariaDB añade `CreateSequence`/`HistoryQuery` (`dialect.go:768-806`), sólo accesibles vía type-assert. **No añadas más métodos a `Dialect` sin considerar si pertenecen a una interfaz secundaria** (`SequenceSupport`, `TemporalTablesSupport`, etc.) que el usuario obtiene con type-assert opcional.

## Decisiones que afectan al módulo

- **ADR 0005 (Solo relacional)**: no hay backends NoSQL. TimescaleDB/CockroachDB se aceptan vía dialecto Postgres si emergen.

## Roadmap de mejora

- **Fase 0**: ~~cerrar P0-2 (JSON path) — cerrado, ver § Historial.~~
- **Fase 1**: tipos ricos — `decimal.Decimal`, `uuid.UUID`, `time.Duration`, `[]byte`/`bytea`, `JSON[T]` genérico, arrays Postgres.
- **Fase 2**: AST permite expresar window functions, locking, CTEs por dialecto.
- **Fase 3**: introspección completa (tipos, NOT NULL, defaults, índices, FKs, checks) por dialecto.

## Tests críticos a no romper

- `dialect_test.go` y `dialect_unit_test.go` — pruebas unitarias por dialecto (placeholder, quote, limit/offset, returning).
- `n_fixes_test.go` — bugs Oracle/MSSQL retroalimentados por auditoría.
- Suites por motor (`postgres_suite_test.go`, etc.).

## Cuándo invocar al `code-reviewer`

Antes de cualquier PR que añada un dialecto, modifique los upserts, toque la interfaz `Dialect` o introduzca tipos nuevos. El reviewer verifica que el cambio aplica en los 6 motores (o justifica por qué algunos no), que no asume placeholder/quoting, y que los tests cubren los 6.
