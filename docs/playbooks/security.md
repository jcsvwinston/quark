---
type: playbook
module: security
files:
  - security.go
  - internal/guard/guard.go
last_review: 2026-05-10
related_adrs: []
related_p0: [P0-5]
closed_p0: [P0-2]
phase: 0
---

# Playbook: Seguridad / SQL Guard

## Qué cubrimos

`internal/guard.SQLGuard` es la primera línea de defensa contra inyección SQL en Quark. Provee:

- **`ValidateIdentifier(name)`**: regex `^[a-zA-Z_][a-zA-Z0-9_]*$`, blacklist de SQL keywords, max len 64.
- **`ValidateOperator(op)`**: whitelist (`=`, `!=`, `<>`, `<`, `<=`, `>`, `>=`, `LIKE`, `ILIKE`, `IN`, `NOT IN`, `IS NULL`, `IS NOT NULL`, `BETWEEN`).
- **`ValidateRawQuery(sql, isSelect)`**: regex anti-`UNION SELECT`, `OR 1=1`, `; DROP `, `--`. Sólo se ejecuta sobre queries que el usuario pasa a `client.RawQuery`/`Exec` y `AllowRawQueries=true`.

`security.go` extiende con `Limits` (max where conditions, max joins, max query length, etc.) y políticas de raw query.

## Filosofía

El guard **NO es defensa completa contra inyección SQL** — eso requeriría un parser SQL real, no regex. **El guard es una capa adicional** sobre el patrón principal: usar bind params (placeholders) para todos los valores. El builder de queries lo hace por construcción; el usuario que use `client.Raw()` o `Exec` debe seguir la disciplina.

**El guard SÍ previene una clase concreta de errores**: identifiers (nombres de columna/tabla) que se concatenan al SQL. Ahí sí no se puede usar bind params (los identifiers no se parametrizan), y sin validación cualquier input no controlado sería inyectable.

## Bugs P0 vivos

### P0-5 · `JOIN ... ON` se concatena raw

**Localización**: `query_builder.go:229` y `query_exec.go:467`. La string `onClause` no pasa por el guard.

**Impacto**: Inconsistencia. `WHERE col` valida; `JOIN ON expr` no.

**Fix esperado en este módulo**:

1. Añadir `ValidateJoinOn(expr string) error` en `internal/guard/guard.go` con un parser mínimo que acepte el patrón `[ident.]ident OP [ident.]ident [AND/OR …]` y rechace lo demás.
2. Marcar la firma string-raw de `Join` como deprecated en godoc.
3. (Fase 2) Introducir API estructurada `Join(table).On(col, op, otherCol)` con AST.

## Historial — bugs cerrados

### P0-2 · `JSONExtract` concatenaba el path con `fmt.Sprintf` (cerrado)

**Era**: `dialect.go` interpolaba el path JSON con `Sprintf("'%s'", path)` (PG) o `'$.%s'` (resto). Una comilla simple rompía el SQL; con path desde input no controlado, vector de inyección.

**Fix aplicado** (defense-in-depth, 2 capas):

1. **Bind del path** en cada dialecto. Nueva firma `Dialect.JSONExtract(column, path string) (sql string, args []any, err error)`. SQL fragment usa `?` como marker neutral; `query_exec.go:substitutePathMarkers` los sustituye por `dialect.Placeholder(N)` al render. PG usa `jsonb_extract_path_text(col, ?, ?, …)` con un bind por segmento; el resto usa `JSON_EXTRACT`/`JSON_VALUE(col, ?)` con `$.path`.
2. **`ValidateJSONPath`** (función libre en `internal/guard/guard.go`) — regex `^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$`, max 256 chars. Cada dialecto la llama antes de construir el bind. Path inválido → error envuelto con `ErrInvalidJSONPath` (sentinel nuevo en `errors.go`).

**Decisión sobre `$.user.name`**: rechazado. La API expone `user.name`; el dialecto añade el prefijo `$.` o construye el variadic. Razón: API uniforme, sin obligar al usuario a conocer la sintaxis de cada motor.

**Cobertura de regresión**: `testJSONPathSecurity` en `json_path_security_test.go` wired a `SharedSuite` (los 6 motores). Aserciones: (a) path en bind args, no en SQL; (b) 8 vectores de inyección rechazados (comillas, `;`, `--`, `/*`, leading `$`, dash, espacios, vacío).

**Anti-pattern a evitar al construir nuevas funciones JSON**: cualquier path o expresión proveniente de input del usuario que se interpole con `Sprintf %s` en lugar de pasarse como bind. Si añades soporte JSON nuevo (containment ops, JSONB queries, arrays), sigue el patrón `Dialect.JSONExtract`: validar + retornar args + bind marker neutro.

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
  - `ValidateJoinOn` (cierra P0-5 hasta que llegue Fase 2 con AST).
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
