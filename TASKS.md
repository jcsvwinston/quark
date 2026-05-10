# Quark — backlog táctico

> Backlog vivo de **Fase 0** del plan (`docs/ANALISIS_MADUREZ.md` §4). Mientras haya items en `## Bugs P0`, no se trabaja en otras fases.
>
> Convención: cada tarea lleva su archivo:línea de origen, criterio de "done" y dónde queda la documentación al cerrar.

---

## Bugs P0 (bloqueantes; producción frágil o insegura)

### ~~P0-1 · `Or()` no propaga `tenantID` → fuga de aislamiento entre tenants~~

**Cerrado** — fix mediante `BaseQuery.cloneForGroup()` (interno) que propaga
`tenantID/tenantCol/schema/cache/limit/offset/hasLimit/unscoped` al blank
recibido por el callback de `Or()` y pre-inyecta el predicado de tenant en su
`where`. Esto cierra la fuga por precedencia SQL (`A AND B OR C` ≡ `(A AND B) OR C`)
con doble inyección intencional (en `client.go:For[T]` para el outer y en
`cloneForGroup` para los OR groups). Regresión cubierta por `testOrRLSLeak` en
`tenant_router_test.go` (subtests `FlatOrRespectsTenant` / `NestedOrRespectsTenant` /
`OtherTenantUnaffected`), wired into `SharedSuite` para los 6 motores. Doc:
`CHANGELOG.md` bajo `[Unreleased] / ### Security`; nota en
`website/docs/advanced/multi-tenant.mdx` sobre la garantía de aislamiento en `Or()`.

### P0-2 · `WhereJSON` concatena el path con `fmt.Sprintf` sin escapar

- **Origen**: `dialect.go` — método `JSONExtract` por dialecto. El path se interpola con `Sprintf("'%s'", path)` o equivalente, sin escapar comillas simples.
- **Impacto**: si el path JSON viene de input no controlado, vector de inyección SQL.
- **Fix esperado**: validar el path contra una regex `^[a-zA-Z_$][a-zA-Z0-9_$.]*$` antes de interpolarlo (o pasar el path como bind param donde el motor lo permita: PG `jsonb_extract_path_text(col, VARIADIC)`, MySQL `JSON_EXTRACT(col, ?)`). El validador vive en `internal/guard/`. Por defecto, rechazar paths con `'`, `;`, `--`, `/*`.
- **Test de regresión**: tabla con columna JSON; asertar que `WhereJSON("data", "x'; DROP TABLE--", "=", "y")` devuelve un error tipado `ErrInvalidIdentifier`, no ejecuta SQL.
- **Doc**: actualizar `website/docs/` sección "JSON queries" con la regla de paths permitidos. Entrada en CHANGELOG bajo `### Security`.

### P0-3 · `linkM2M` traga errores silenciosamente

- **Origen**: `query_crud.go:~1697`, comentario `// Ignore duplicate key errors - already linked` que retorna `nil` para cualquier error, no sólo duplicados.
- **Impacto**: una FK violation, conexión rota o constraint violation se enmascara como éxito. Corrupción silenciosa.
- **Fix esperado**: discriminar el error por código del driver:
  - PG: `*pgconn.PgError` con `Code == "23505"` (unique violation) → ignorar; otros → propagar.
  - MySQL: `*mysql.MySQLError` con `Number == 1062` → ignorar; otros → propagar.
  - SQLite: `errors.Is(err, sqlite3.ErrConstraintUnique)` (con modernc, comprobar el equivalente).
  - MSSQL: número 2627 / 2601 → ignorar.
  - Oracle: ORA-00001 → ignorar.
- **Test de regresión**: insertar la misma relación dos veces (debe ser idempotente, ok); luego provocar una FK violation (apuntar a un ID inexistente con FK habilitada) y asertar que `linkM2M` devuelve `ErrConstraint` envuelto, no `nil`. En los 6 motores.
- **Doc**: documentar la semántica idempotente de M2M link en `website/docs/relations/many-to-many.md`.

### P0-4 · `isZeroValue` impide `Update` con valores cero (false / 0 / "")

- **Origen**: `query_crud.go:649` (función `isZeroValue`). `Update(entity)` salta los campos cuyo valor es el zero-value del tipo.
- **Impacto**: imposible poner un bool a `false`, un int a `0`, un string a `""` con `Update(entity)`. Sorpresivo y peligroso (silencioso). El usuario debe usar `UpdateMap` con WHERE manual.
- **Fix esperado** (compatible con el plan: dirty tracking ligero llega en Fase 1, este es el parche P0):
  1. Añadir API explícita `UpdateFields(entity, fields ...string)` que actualiza sólo los campos nombrados, sin filtrar zero-values.
  2. Documentar la trampa de `Update(entity)` con un warning visible en `website/docs/crud/update.md`.
  3. Loguear (a nivel WARN) cuando `Update(entity)` salta campos zero-value, para que el desarrollador note el efecto.
- **Test de regresión**: actualizar un `bool` de `true` a `false` con `UpdateFields(u, "active")`; asertar que la fila quedó con `active = false`.
- **Doc**: warning en `website/docs/crud/update.md` y entrada en CHANGELOG.

### P0-5 · `JOIN ON` se concatena al SQL sin pasar por el guard

- **Origen**: `query_builder.go:229` (firma `Join(table, onClause)`) + `query_exec.go:467` (concatena el `onClause` raw).
- **Impacto**: inconsistencia de seguridad. `WHERE col` se valida contra `ValidateIdentifier`, pero `JOIN ON` acepta cualquier string. Si `onClause` viene de input dinámico, vector de inyección.
- **Fix esperado**: introducir API estructurada `Join(table).On(col, op, otherCol)` y `JoinExpr(table, expr Expr)` donde `Expr` es el AST de la Fase 2. Mientras tanto:
  - Validar el `onClause` actual con un parser mínimo que acepte el patrón `[ident.]ident OP [ident.]ident [AND/OR …]` y rechace lo demás.
  - Marcar la firma string-raw como deprecated en godoc, con plazo de eliminación en v0.4.
- **Test de regresión**: `Join("users", "users.id = orders.user_id; DROP TABLE orders")` debe devolver `ErrInvalidJoin`, no ejecutar.
- **Doc**: entrada de deprecación en CHANGELOG `### Deprecated` y nota en `website/docs/queries/joins.md`.

---

## Limpieza de Fase 0 (no son bugs P0 pero bloquean credibilidad pública)

### F0-1 · Reconciliar versionado público

- **Estado actual**: `RELEASE_NOTES_V1.md` anuncia v1.0.0; `CHANGELOG.md` sólo tiene 0.1.0/0.1.1; `SECURITY.md` dice "pre-1.0"; README dice "v0.x"; ROADMAP marca features de v0.2 como "Completed" sin tag v0.2.
- **Acciones**:
  1. Renombrar `docs/RELEASE_NOTES_V1.md` → `docs/RELEASE_NOTES_v0.2.md` (texto sin marketing, lista honesta de cambios desde 0.1.1).
  2. Actualizar `CHANGELOG.md` con la entrada `[0.2.0] - 2026-MM-DD` que consolide todo lo entre 0.1.1 y hoy.
  3. Alinear `README.md`: badge de versión, snippets que digan "v0.2".
  4. Alinear `SECURITY.md`: cambiar "pre-1.0" por "v0.x — supported only on `main`".
  5. Sincronizar `docs/ROADMAP.md` con el plan de fases de `docs/ANALISIS_MADUREZ.md` §4.
- **Done**: taggear `v0.2.0` en git. Acción de release publica el sitio versionado.

### F0-2 · Crear `examples/blog-api/` o eliminar las menciones

- **Estado**: README enlaza dos veces a `examples/blog-api/` (sección de demo y "go run"). El directorio no existe.
- **Acción recomendada**: crear el ejemplo. Es una buena demo de multi-tenancy + relaciones + migraciones. Si no hay tiempo, eliminar las dos menciones del README.
- **Done**: `cd examples/blog-api && go run main.go` arranca un servidor HTTP de ejemplo, o las menciones desaparecen.

### F0-3 · Corregir paths en `examples/README.md`

- **Estado**: `examples/README.md` instruye `go run pkg/quark/examples/sqlite/main.go` (path heredado de monorepo previo). La ruta real es `examples/sqlite/main.go`.
- **Acción**: reemplazar todos los `pkg/quark/` → ``.
- **Done**: cada comando `go run` del README funciona desde la raíz del repo.

### F0-4 · Consolidar Quick Start duplicado en README

- **Estado**: README tiene dos Quick Starts (líneas ~34-92 y ~164-222). Copy-paste error.
- **Acción**: dejar uno solo, el más actualizado, en posición tras la sección de "Why Quark".
- **Done**: una sola sección Quick Start; lectura lineal sin duplicados.

### F0-5 · Reemplazar badge de coverage hardcoded

- **Estado**: README muestra "Coverage 87%" como badge estático.
- **Acción**: configurar codecov o usar `go tool cover` artifact en CI; badge dinámico que enlace al reporte real. Aceptable interim: eliminar el badge hasta que sea real.
- **Done**: el porcentaje del badge se corresponde con `go test -coverprofile`.

---

## Setup de infraestructura (Fase 0, requerido para Fase 1+)

### F0-6 · Pipeline de publicación de `website/` a `quark-docs`

- **Objetivo**: cada release de Quark publica el sitio Docusaurus al repo `jcsvwinston/quark-docs` rama `gh-pages`. URL pública (`jcsvwinston.github.io/quark-docs/`) intacta.
- **Acción**:
  1. En `website/docusaurus.config.ts`: confirmar `baseUrl: '/quark-docs/'`, `organizationName: 'jcsvwinston'`, `projectName: 'quark-docs'`, `deploymentBranch: 'gh-pages'`.
  2. Generar PAT con scope `repo` para push a `quark-docs` y guardarlo como secret `DOCS_DEPLOY_TOKEN` en el repo de Quark.
  3. Crear `.github/workflows/deploy-docs.yml` que en push a tag `v*` builda `website/` y pushea `website/build/` a `quark-docs:gh-pages`.
  4. Archivar el repo `quark-docs` como read-only para fuente; sólo `gh-pages` queda activa.
- **Done**: hacer un tag de prueba `v0.2.0-rc1`, verificar que el sitio se actualiza sin intervención.

### F0-7 · Inicializar versioning de Docusaurus

- **Objetivo**: que `website/versioned_docs/` exista con el snapshot inicial de la versión actual.
- **Acción**: `cd website && npm run docusaurus docs:version 0.2.0`. Commit del directorio generado.
- **Done**: `versions.json` lista `["0.2.0"]`. Sitio sirve `/docs/` (next) y `/docs/0.2.0/`.

### F0-8 · Setup testcontainers-go para los 6 motores

- **Objetivo**: que `go test ./...` arranque containers de Postgres, MySQL, MariaDB, MSSQL, Oracle (XE) por sí solo. Eliminar los `t.Skip` por env var.
- **Acción**:
  1. Añadir dependencia `github.com/testcontainers/testcontainers-go` y los módulos de cada motor.
  2. Refactorizar `*_suite_test.go` por motor: helper `setupContainer(t)` que devuelve DSN, sin env vars.
  3. Build tag `//go:build integration` para tests caros; default rápido sigue siendo SQLite.
  4. CI matrix con job por motor.
- **Done**: `go test -tags=integration ./...` levanta los 6 motores y corre el suite completo. CI verde con matriz.

### F0-9 · Instalar `release-please` o `semantic-release`

- **Objetivo**: automatizar bump de versión + CHANGELOG desde Conventional Commits.
- **Acción**: añadir `.github/workflows/release-please.yml`. Configurar release type `go` (single-module).
- **Done**: tras un merge a `main` con commits `feat:`/`fix:`, aparece un PR de release automático con CHANGELOG y version bump.

### F0-10 · Linter de docs

- **Objetivo**: detectar drift entre código y docs. Bash o Go script en CI.
- **Checks mínimos**:
  - Cada feature listada en `ROADMAP.md` como "Completed" debe tener entrada en `CHANGELOG.md`.
  - No debe haber referencias a `RELEASE_NOTES_V1` cuando la última tag no es v1.
  - Enlaces internos en `docs/**/*.md` y `website/docs/**/*.md` no deben estar rotos.
  - Cualquier API pública nueva (`go doc`) debe tener su página en `website/docs/`.
- **Done**: CI rojo si alguno falla; verde tras corregir.

---

## Cómo cerrar un item

1. Crear branch `fix/p0-1-or-rls-leak` (o lo que aplique).
2. Implementar fix + test de regresión.
3. Correr `go test -tags=integration ./...` localmente con los 6 motores.
4. Invocar el subagente `code-reviewer` antes del PR.
5. PR con título Conventional Commit (`fix(query): propagate tenant context in Or() clauses`).
6. Verificar que `code-reviewer` aprueba, CI verde en los 6 motores, CHANGELOG actualizado.
7. Mergear con squash.
8. Marcar el item como `~~tachado~~` en este archivo o borrar la sección.

## Cuándo pasar a Fase 1

Cuando este archivo se queda con secciones tachadas y los puntos de **Setup de infraestructura** estén verdes en CI. Antes no.
