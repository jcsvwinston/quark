---
type: playbook
module: security
files:
  - security.go
  - internal/guard/guard.go
last_review: 2026-05-10
related_adrs: []
related_p0: []
closed_p0: [P0-2, P0-5]
phase: 0
---

# Playbook: Seguridad / SQL Guard

## Qué cubrimos

`internal/guard.SQLGuard` es la primera línea de defensa contra inyección SQL en Quark. Provee:

- **`ValidateIdentifier(name)`**: regex `^[a-zA-Z_][a-zA-Z0-9_]*$`, blacklist de SQL keywords, max len 64.
- **`ValidateOperator(op)`**: whitelist (`=`, `!=`, `<>`, `<`, `<=`, `>`, `>=`, `LIKE`, `ILIKE`, `IN`, `NOT IN`, `IS NULL`, `IS NOT NULL`, `BETWEEN`).
- **`ValidateRawQuery(sql, isSelect)`**: regex anti-`UNION SELECT`, `OR 1=1`, `; DROP `, `; DELETE`, `; UPDATE … SET`, y comentario de línea `--` (para `RawQuery` exige además placeholders). Sólo se ejecuta sobre queries que el usuario pasa a `client.RawQuery`/`Exec` con `AllowRawQueries=true`. **Backstop heurístico, no filtro completo**: los comentarios de bloque `/* */` se permiten a propósito (son *optimizer hints* `/*+ … */` legítimos), así que la evasión `UNION/**/SELECT` no se atrapa aquí — la frontera real es `AllowRawQueries` (off por defecto) + placeholders para valores. Cubierto por la fase F13 del bug-bash (`bugbash/phases/f13_security`).

`security.go` extiende con `Limits` (max where conditions, max joins, max query length, etc.) y políticas de raw query.

## Filosofía

El guard **NO es defensa completa contra inyección SQL** — eso requeriría un parser SQL real, no regex. **El guard es una capa adicional** sobre el patrón principal: usar bind params (placeholders) para todos los valores. El builder de queries lo hace por construcción; el usuario que use `client.Raw()` o `Exec` debe seguir la disciplina.

**El guard SÍ previene una clase concreta de errores**: identifiers (nombres de columna/tabla) que se concatenan al SQL. Ahí sí no se puede usar bind params (los identifiers no se parametrizan), y sin validación cualquier input no controlado sería inyectable.

## Bugs P0 vivos

(ninguno en este módulo; ver § Historial.)

## Historial — bugs cerrados

### P0-2 · `JSONExtract` concatenaba el path con `fmt.Sprintf` (cerrado)

**Era**: `dialect.go` interpolaba el path JSON con `Sprintf("'%s'", path)` (PG) o `'$.%s'` (resto). Una comilla simple rompía el SQL; con path desde input no controlado, vector de inyección.

**Fix aplicado** (defense-in-depth, 2 capas):

1. **Bind del path** en cada dialecto. Nueva firma `Dialect.JSONExtract(column, path string) (sql string, args []any, err error)`. SQL fragment usa `?` como marker neutral; `query_exec.go:substitutePathMarkers` los sustituye por `dialect.Placeholder(N)` al render. PG usa `jsonb_extract_path_text(col, ?, ?, …)` con un bind por segmento; MySQL/MariaDB/SQLite/MSSQL usan `JSON_EXTRACT`/`JSON_VALUE(col, ?)` con `$.path`. **Excepción Oracle (2026-05-26, #28):** `JSON_VALUE` de Oracle rechaza un path bound (`ORA-40454: path expression not a literal`), así que el path validado se inlinea como literal (`JSON_VALUE("COL", '$.path')`, `args=nil`). Sigue siendo injection-safe por la misma lógica del § Filosofía: un token que el motor NO deja parametrizar se valida antes de concatenar (aquí vía `ValidateJSONPath`). `OracleDialect.JSONExtract` es el patrón de referencia para ese caso.
2. **`ValidateJSONPath`** (función libre en `internal/guard/guard.go`) — regex `^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$`, max 256 chars. Cada dialecto la llama antes de construir el bind. Path inválido → error envuelto con `ErrInvalidJSONPath` (sentinel nuevo en `errors.go`).

**Decisión sobre `$.user.name`**: rechazado. La API expone `user.name`; el dialecto añade el prefijo `$.` o construye el variadic. Razón: API uniforme, sin obligar al usuario a conocer la sintaxis de cada motor.

**Cobertura de regresión**: `testJSONPathSecurity` en `json_path_security_test.go` wired a `SharedSuite` (los 6 motores). Aserciones: (a) path en bind args, no en SQL; (b) 8 vectores de inyección rechazados (comillas, `;`, `--`, `/*`, leading `$`, dash, espacios, vacío).

**Anti-pattern a evitar al construir nuevas funciones JSON**: cualquier path o expresión proveniente de input del usuario que se interpole con `Sprintf %s` en lugar de pasarse como bind — **salvo cuando el motor rechaza el bind y el valor ya pasó por un validator del guard** (ver `OracleDialect.JSONExtract` como patrón de referencia: literal inline sólo tras `ValidateJSONPath`). Si añades soporte JSON nuevo (containment ops, JSONB queries, arrays), sigue el patrón `Dialect.JSONExtract`: validar + (bind por defecto / literal validado donde el motor lo exija).

### P0-5 · `JOIN ... ON` se concatenaba raw (cerrado, fase deprecation)

`query_builder.go:Join`/`LeftJoin`/`RightJoin` aceptaban `on` como string opaco
y `query_exec.go:buildSelect`/`Count` lo emitían verbatim al SQL final. `WHERE
col` se validaba via `ValidateIdentifier`; `JOIN ON` no. Si el `on` venía de
input dinámico, vector de inyección — el caso más simple es
`"users.id = orders.user_id; DROP TABLE orders"`.

Fix aplicado:

1. **`guard.ValidateJoinOn(expr)`** — regex `^\s*<token>\s*<op>\s*<token>(\s+(?i:AND|OR)\s+<token>\s*<op>\s*<token>)*\s*$` con `<token> = ident(.ident)?` y `<op> ∈ {=, !=, <>, <, <=, >, >=}` (max 512 chars). Sólo comparaciones identifier-to-identifier; literales, function calls, paréntesis, subqueries y comentarios SQL quedan rechazados.
2. **Wiring**: ambos call sites (`query_exec.go:buildSelect` y `Count`) llaman al validator antes de concatenar `j.onClause`. Path inválido → `ErrInvalidJoin` (sentinel nuevo en `errors.go`) sin ejecutar SQL.
3. **Deprecation**: `Join`, `LeftJoin`, `RightJoin` en su forma string-raw están marcados `// Deprecated:` en godoc; reemplazo en v0.4 con builder estructurado `Join(table).On(col, op, otherCol)` (Fase 2 AST).

Cobertura: `testJoinOnSecurity` en `join_on_security_test.go` wired a
`SharedSuite` — 4 subtests (valid identifier join, valid AND-joined, 8
vectores de inyección rechazados, mismo check para Count). Unit tests
adicionales en `internal/guard/guard_test.go` (`TestValidateJoinOn_Valid`
con 12 casos, `TestValidateJoinOn_Invalid` con 18 casos, BoundMethod).

**Anti-pattern a evitar al añadir más builders SQL**: cualquier string que
provenga de input del usuario y termine en SQL final debe pasar por un
validator del guard antes de concatenarse. La regla cubre nombres de columna,
nombres de tabla, nombres de schema, expresiones JSON path, expresiones
JOIN ON, y cualquier identificador o predicado nuevo que añadas. Si la
expresión es lo bastante rica como para que un validator de regex no le
quepa, reescribe esa parte del builder con AST (Fase 2) en lugar de añadir
otra string-raw API.

## Limitaciones reconocidas (NO publicitar como completas)

### `ValidateRawQuery` con regex es heurística débil

`UNION/**/SELECT` (con comentarios) la pasa. Comentarios `--` no se filtran consistentemente. Para defense-in-depth real haría falta un parser SQL.

**No anuncies "anti-injection completo"** — anúncialo como "primera línea de defensa, no sustituye placeholders".

### Blacklist de keywords incompleta

Hoy bloquea `DROP`, `TRUNCATE`, `ALTER`, etc. **NO bloquea**: `MERGE`, `WITH`, `WINDOW`, `MATERIALIZED`. Son DDL/DML válidos en algunos contextos pero pueden ser parte de un payload inyectado.

Si extiendes la blacklist, mantén la lista por dialecto (no todas las keywords son SQL keywords en todos los motores).

### `maxIdentifierLen=64`

PG max es 63; Oracle legacy 30, 12c+ 128; MSSQL 128. **No es configurable hoy.** Plan: por dialecto en Fase 1 o cuando emerge dolor.

## Anti-patterns a vigilar

### Concatenar identifiers de usuario sin validar

```go
// MAL
sql := "SELECT * FROM " + userInput

// BIEN
if err := guard.ValidateIdentifier(userInput); err != nil {
    return err
}
sql := "SELECT * FROM " + dialect.Quote(userInput)
```

Cualquier identificador (tabla, columna, schema, alias, secuencia, índice) que provenga de input del usuario **debe** pasar por `ValidateIdentifier` antes de concatenarse.

### Concatenar valores de usuario al SQL

```go
// MAL
sql := fmt.Sprintf("WHERE id = %d", userID)

// BIEN
sql := "WHERE id = " + dialect.Placeholder(1)
args := []any{userID}
```

Esto debería ser obvio, pero el `code-reviewer` lo busca explícitamente porque cuando aparece, suele aparecer en cantidad.

### Bypass del guard "porque es para tests"

`AllowRawQueries=true` está pensado para tests y migraciones, no para código de producción. Si lo activas en `production` config, lo loguea WARN. Si introduces un test que activa raw queries para verificar comportamiento, **el test debe estar en archivo `*_test.go` con build tag explícito**, no en código de producción.

### Asumir que el guard cubre lo que toca el caller

El guard valida lo que se le pasa explícitamente. **Si tu nuevo código construye SQL crudo y nunca llama al guard, el guard no te protege.** Cuando introduzcas una nueva ruta de generación de SQL, integra el guard explícitamente.

## Roadmap de mejora

- **Fase 0**:
  - ~~`ValidateJSONPath` (cerró P0-2 — ver Historial).~~
  - ~~`ValidateJoinOn` (cerró P0-5 hasta Fase 2 — ver Historial).~~
- **Fase 1**:
  - `maxIdentifierLen` por dialecto.
- **Fase 4** (paralelo a observabilidad):
  - Auditar emisión de errores con `fmt.Errorf` que pueden filtrar PII en logs/spans (parte del esfuerzo de redacción de PII).
- **Fase 6** (consideración):
  - Evaluar parser SQL real (sqlparser o similar) en lugar de regex para `ValidateRawQuery`. Coste-beneficio incierto; el patrón principal sigue siendo "usa el builder, no raw".

## Tests críticos a no romper

- `internal/guard/guard_test.go` — tests unitarios de cada validación.
- Tests de los suite por motor que ejercitan `AllowRawQueries`.

## Cuándo invocar al `code-reviewer`

Antes de cualquier PR que:

- Toque `internal/guard/`.
- Introduzca nueva ruta de generación de SQL en cualquier módulo (especialmente `dialect.go`, `query_*.go`, `migrate/`).
- Active `AllowRawQueries` en algún path.
- Cambie `Limits`.

El reviewer vigila especialmente: cualquier `fmt.Sprintf` con `%s` en SQL final que no venga de un identifier ya validado o un placeholder; cualquier operador o keyword introducido sin actualizar la whitelist; tests que cubran edge cases de inyección clásicos.
