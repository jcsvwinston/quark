# Auditoría docs↔código — certificación v1.1.4

> **Estado:** ABIERTO (2026-06-18). Evidencia de respaldo de los items **DS-7…DS-15**
> de `TASKS.md` § "Doc-sync — certificación docs↔código v1.1.4". Este documento es
> material interno (no se publica en el sitio). La auditoría **no modificó** ninguna
> página de `website/docs/`; las correcciones las ejecuta la próxima sesión vía
> `/next-session auto` (o `/doc-sync`), cada una con su PR, `code-reviewer` y CHANGELOG.

## Alcance y método

- **Objeto auditado:** las 41 páginas de `website/docs/` (Guides, Advanced, API Reference, Reference, intro) — la fuente viva ("next"), que ya declara v1.1.4.
- **Contra:** código en HEAD de `main` (tag `v1.1.4`, 2026-06-17, árbol limpio).
- **Método:** verificación estática por lectura de fuente (Read/Grep), cuatro pasadas por cluster + inventario de la API exportada. Evidencia citada como `archivo:línea`.
- **Cobertura:** ~490 afirmaciones comprobables, ~449 verificadas (≈92%). No hay features inventadas ni over-promising sistémico; la tabla "Why QUARK" del intro, multi-tenancy (4 estrategias), RLS nativa, caché con stampede protection, read replicas, sharding, migraciones, observabilidad OTel y la superficie CRUD/Querying/Transactions están verificadas contra el fuente.

### Limitaciones de la certificación
1. **Sin toolchain Go en el entorno de auditoría** → no se compiló ni se re-ejecutaron tests/benchmarks. Se certifica correspondencia de fuente (firma/símbolo/comportamiento documentado vs implementado), no "compila y pasa CI" (eso lo gatea la matriz de 6 motores aparte). Los ejemplos marcados "no compilan" se derivaron comparando la firma documentada con la real.
2. **Los números de `benchmarks.mdx` no son verificables estáticamente.** Sólo se verificó coherencia interna (coinciden con `benchmarks/README.md`; versiones de libs vs `benchmarks/go.mod`).
3. Se auditó `website/docs/` (next). El sitio **sirve hoy por defecto el snapshot `v1.1.0`** (ver DS-15) — más antiguo que lo auditado.

## Veredicto

**CERTIFICADO CON SALVEDADES.** Apto como referencia general. 16 desajustes confirmados (❌) que se reducen a **9 problemas distintos** (DS-7…DS-15), dos de severidad CRÍTICA. Requiere cerrarlos para poder afirmar correspondencia 1:1.

| Item | Sev. | Página(s) | Snapshot v1.1.0 |
| --- | --- | --- | --- |
| DS-7 · ejemplos de `JOIN` no compilan | CRÍTICO | `reference/api/query-builder.mdx`, `guides/querying.mdx:293` | query-builder **idéntico** en snapshot (afectado) |
| DS-8 · SQLGuard descrito como semántico | CRÍTICO | `reference/sqlguard.mdx`, `guides/getting-started.mdx:305-313`, `reference/comparison.mdx` | getting-started **idéntico** en snapshot (afectado) |
| DS-9 · la CLI está infravalorada / "no hay CLI" | ALTO | `guides/cli.mdx:9-16`, `guides/migrations.mdx:16-17` | ambas difieren del snapshot |
| DS-10 · `rel:"m2m"` en el ejemplo m2m | ALTO | `reference/api/modeling.mdx` | **idéntico** en snapshot (afectado) |
| DS-11 · interface `Dialect` mal documentada | ALTO | `reference/api/dialects.mdx` | **idéntico** en snapshot (afectado) |
| DS-12 · `CreateListener` "no implementado" (sí lo está) | ALTO | `reference/api/observability.mdx` | **idéntico** en snapshot (afectado) |
| DS-13 · tag de caché PK-compuesta "join con `:`" | MEDIO | `advanced/caching-observability.mdx:64` | difiere del snapshot |
| DS-14 · roadmap "bulk bypass hooks" obsoleto | MEDIO | `reference/roadmap.mdx:235` | difiere del snapshot |
| DS-15 · sitio sirve docs v1.1.0, código v1.1.4 | PROCESO | `website/versions.json` | — |

---

## DS-7 · Los ejemplos de `JOIN` no compilan (forma de 2 argumentos eliminada en v0.4) — CRÍTICO

**Doc dice:** firma `Join(table, on string)` (y `LeftJoin`/`RightJoin` igual). Ejemplo en `guides/querying.mdx:293`: `.Join("top_orders", "users.id = top_orders.user_id")`. `reference/api/query-builder.mdx` documenta las tres firmas con dos argumentos y todos sus ejemplos los usan.

**Código hace:** `Join(table string) *JoinBuilder[T]` — `query_builder.go:418`; `LeftJoin` `:423`; `RightJoin` `:428`. Se completa con `.On(left, op, right)` o `.OnRaw(clause)`. La forma de dos argumentos no existe.

**Agravante:** la propia `guides/querying.mdx` documenta la forma correcta en `:542-590` y en `:590` afirma que la firma de 2 args "is removed in v0.4" → la página se contradice. Son las dos páginas de mayor tráfico y el código pegado no funciona.

**Fix:**
- `guides/querying.mdx:293` → `.Join("top_orders").OnRaw("users.id = top_orders.user_id")`.
- `reference/api/query-builder.mdx` → reescribir las firmas de `Join`/`LeftJoin`/`RightJoin` a `(table string) *JoinBuilder[T]` y todos los ejemplos a `.On(...)`/`.OnRaw(...)`.
- Revisar también `reference/api/errors.mdx` (la nota de `ErrInvalidJoin` describe el builder como "pending Phase 2 AST"; ya está entregado).
- `reference/api/query-builder.mdx` es **idéntico** en `versioned_docs/version-1.1.0/` → arrastra el mismo error; corregir allí también o resolver vía DS-15.

---

## DS-8 · `sqlguard.mdx` describe validación semántica que el código no hace — CRÍTICO

**Doc dice:** la tabla "What gets validated" afirma que las columnas se "Checked against registered model fields" y las tablas "against known schema"; el ejemplo dice que una columna inexistente da `ErrInvalidQuery: column "X" not found on model User`.

**Código hace:** `internal/guard/guard.go:79-99` valida **léxicamente**: regex `^[a-zA-Z_][a-zA-Z0-9_]*$`, lista de palabras reservadas, longitud ≤64. **No** comprueba pertenencia al modelo/esquema. Una columna mal escrita pero léxicamente válida (`nonexistent_column`) **pasa el guard** y falla en el motor, no en la API. Además:
- Columna/tabla mal formada → `ErrInvalidIdentifier` + "invalid identifier" (`guard.go:89-92`), no `ErrInvalidQuery` ni "column not found on model".
- Operador inválido → string `"ErrInvalidQuery: operator %q is not allowed"` **sin** envolver el sentinel (`guard.go:248-250`); `errors.Is(err, quark.ErrInvalidQuery)` da **false**.
- Único ejemplo de error literalmente correcto en la página: el de `WhereSubquery` (envuelve bien el sentinel).

**Propagación:** `guides/getting-started.mdx:305-313` (el `switch` con `errors.Is(err, quark.ErrInvalidQuery)` no captura columnas/operadores) y los ejemplos de runtime de `reference/comparison.mdx`.

**Por qué CRÍTICO:** el proyecto se posiciona como "security-first" y esta página respalda esa afirmación; describe una garantía (rechazo de columnas inexistentes) que el ORM no ofrece.

**Fix:**
- Reescribir `sqlguard.mdx` para describir validación **léxica** (regex + reserved words + longitud ≤64), no semántica. Corregir los mensajes/sentinels de ejemplo: columna/tabla → `ErrInvalidIdentifier`; operador → texto sin sentinel (o, si se prefiere, **arreglar el código** para envolver `ErrInvalidQuery` con `%w` — decisión del owner: doc o código).
- Ajustar `getting-started.mdx:305-313` (capturar `ErrInvalidIdentifier` además de `ErrInvalidQuery`, o aclarar el alcance).
- Ajustar los ejemplos de `comparison.mdx`.
- `getting-started.mdx` es **idéntico** en el snapshot 1.1.0 → afectado.

---

## DS-9 · La CLI está infravalorada; `migrations.mdx` afirma que no existe — ALTO

**Doc dice:**
- `guides/cli.mdx:9-16`: `quark gen` es "the first generally-available subcommand"; migraciones/schema/tenant "are still best expressed as small Go commands".
- `guides/migrations.mdx:16-17`: "There is no standalone migration CLI in the current module tree."

**Código hace:** `cmd/quark` despacha **9 subcomandos** con sub-acciones: `init`, `migrate` (create/up/down/status/version — `cmd/quark/commands/migrate.go:37,45-84`), `model generate`, `inspect` (schema/table/sql), `validate`, `seed` (create/run/list), `sync`, `tenant` (provision/migrate/list/migrate-all), `gen`. Contradice además a `guides/codegen.mdx`, que sí referencia el binario.

**Fix:** reescribir `cli.mdx` y `migrations.mdx:16-17` para reflejar los 9 subcomandos reales. Cruzar con la cobertura del exerciser S9 (`examples/superapp/cli/cli_test.go`) como fuente de verdad de los paths de comando.

---

## DS-10 · `modeling.mdx` usa `rel:"m2m"`, que falla en escritura/migración — ALTO

**Doc dice:** el ejemplo many-to-many de `reference/api/modeling.mdx` (≈ líneas 49 y 59) usa `rel:"m2m"`.

**Código hace:** sólo `Preload` tolera el alias `m2m` (`query_exec.go:1366`). **Create no escribe los enlaces** (`query_crud.go:1425`, sólo `case "many_to_many"`) y **Migrate no crea la tabla join** (`migrator.go:274`, exige `== "many_to_many"`). Las páginas `relations.mdx` y `query-builder.mdx` usan correctamente `many_to_many` → inconsistencia entre páginas; la incorrecta falla en runtime sin error claro.

**Fix:** cambiar `rel:"m2m"` → `rel:"many_to_many"` en `reference/api/modeling.mdx`. Es **idéntico** en el snapshot 1.1.0 → afectado.

---

## DS-11 · La interface `Dialect` documentada está mal (bloquea dialectos custom) — ALTO

**Doc dice:** `reference/api/dialects.mdx` afirma "Custom dialects must implement the full `Dialect` interface" y lista `JSONExtract(column, path string) string` (un retorno).

**Código hace:**
- `JSONExtract(column, path string) (sql string, args []any, err error)` — `dialect.go:86` (tres retornos).
- La interface incluye además `LockSuffix(opts LockOptions) (tableHint, suffix string, err error)` — `dialect.go:124` — que la doc **omite**.

Quien implemente un dialecto siguiendo la doc no satisface la interface real.

**Fix:** corregir la firma de `JSONExtract` y añadir `LockSuffix` (y revisar que la lista de métodos de la interface esté completa contra `dialect.go:15-132`). **Idéntico** en el snapshot 1.1.0 → afectado.

---

## DS-12 · `observability.mdx`: `CreateListener` documentado como NO implementado — ALTO

**Doc dice:** "`CreateListener` is not implemented and returns `ErrDialectNotSupported` — LISTEN/NOTIFY is out of scope for Fase 5 (ADR-0013)".

**Código hace:** está implementado para PostgreSQL vía `pgListener` (`events.go:174`); sólo devuelve `ErrDialectNotSupported` en motores no-PG. La feature se entregó en v1.1.0 (BB / PG listener). El ADR correcto es **ADR-0019**, no 0013.

**Fix:** documentar que `CreateListener` funciona en PostgreSQL desde v1.1.0 (fire-and-forget, conexión dedicada, ADR-0019) y devuelve `ErrDialectNotSupported` en los otros 5 motores. Cruzar con `advanced/events.mdx`, que ya lo describe correctamente. **Idéntico** en el snapshot 1.1.0 → afectado.

---

## DS-13 · El tag de caché per-row no une PKs compuestas con `:` — MEDIO

**Doc dice:** `advanced/caching-observability.mdx:64` — "The per-row tag (`<table>:<pk>`)… Composite PKs join with `:`".

**Código hace:** `cache_invalidation.go:21-44` — `rowTag` devuelve `""` con PK compuesta y la invalidación cae al tag de tabla (comentario "Composite PKs aren't supported by this helper yet"; test "composite pk yields empty (gap)"). El join con `:` existe en el **audit log** (`audit.go:193`), no en la caché — probable copy-paste cruzado.

**Fix:** alinear el texto con la realidad ("las PKs compuestas caen al tag de tabla, igual que los métodos bulk"). Difiere del snapshot 1.1.0.

---

## DS-14 · `roadmap.mdx` dice que el batch "bypass hooks" (ya no del todo) — MEDIO

**Doc dice:** `reference/roadmap.mdx:235` (§ "Known current boundaries") — `CreateBatch`/`UpdateBatch` "bypass hooks".

**Código hace:** v1.1.4 añadió `Before*` per-entity en `CreateBatch`/`UpdateBatch` (CHANGELOG 1.1.4; `release-notes.mdx:42-45`). Hoy `Before*` **sí** corre; sólo `After*` sigue bypassed.

**Fix:** matizar la frase ("`After*` no se disparan en batch; `Before*` sí, desde v1.1.4"). Cruzar con `guides/hooks.mdx`, que ya lo documenta correctamente. Difiere del snapshot 1.1.0.

---

## DS-15 · El sitio sirve docs v1.1.0 mientras el código va por v1.1.4 — PROCESO

**Hecho:** `website/versions.json` congela como última versión `1.1.0`. No existen snapshots `1.1.1`–`1.1.4`. El sitio sirve por defecto el snapshot **v1.1.0**; el `intro.mdx` de "next" ya dice v1.1.4. El paso 4 del checklist de `/release` ("`npm run docusaurus docs:version X.Y.Z`") no se ejecutó en v1.1.1–v1.1.4 (releases de `fix:`).

**Impacto:** los parches son API-compatibles, así que la mayoría del snapshot 1.1.0 sigue correcto, pero queda desactualizado al menos en DS-12 (`CreateListener` se implementó en 1.1.0+) y DS-14 (`Before*` batch, 1.1.4). Varios items (DS-7, DS-8, DS-10, DS-11, DS-12) viven **idénticos** en el snapshot 1.1.0 (ver tabla).

**Fix (decisión del owner):**
- **(a) Preferida:** `cd website && npm run docusaurus docs:version 1.1.4` para que el sitio sirva la versión actual, tras corregir DS-7…DS-14 en `website/docs/` (next). Así el snapshot nace ya correcto y no hay que tocar snapshots inmutables.
- **(b) Alternativa:** parchear los snapshots `versioned_docs/version-1.1.0/` afectados como "error explícito" (precedente DS-1), respetando la regla de no reescribir snapshots salvo error.
- Reforzar el paso 4 de `/release`. Considerar un check en CI que extraiga los bloques ```go de `website/docs/` y los compile (habría cazado DS-7 y DS-10 automáticamente).

---

## Lo que SÍ queda certificado (sin discrepancias)

- **CRUD** (`crud.mdx`): Create/Update/Delete/Upsert/Batch + Track/Tracked — firmas exactas.
- **Querying** (`querying.mdx` salvo DS-7): First/Find/List/Count/agregados, Paginate/Cursor/Iter, ventanas, CTEs, subqueries, set ops, locking (incl. Oracle ORA-02014), JSON por dialecto.
- **Transactions**: Tx, savepoints, OnCommit/OnRollback FIFO, ForTx, WithDeadlockRetry con códigos por motor.
- **Migraciones** (`migrations.mdx` salvo DS-9): Migrate/Sync, Diff/PlanMigration/ApplyPlan, lock distribuido por dialecto, Backfill, registry.
- **Multi-tenancy**: las 4 estrategias, RLS nativa PG (`set_config`+`CREATE POLICY`+CLI `quarktenant`), alias deprecado presente.
- **Eventos / Audit**: EventBus post-commit/inline, semántica tx vs no-tx, audit atómico (34/34).
- **Caché / Observabilidad** (salvo DS-13): stampede protection, invalidación por tags, OTel traces+metrics con nombres de span exactos, `WithSpanRedaction` default-on.
- **Read replicas / Sharding**: routing, failover/cooldown, sticky, ShardRouter (28/28).
- **Codegen** (`codegen.mdx`): `quark gen`, `<Model>Columns`, `WhereP`, contract v3, fallback a reflect — preciso y sin hype.
- **Versión declarada** (`intro`, `roadmap`, `release-notes`): v1.1.4 coherente con tag y CHANGELOG; la tabla "Why QUARK" del intro es real.
- **Dialectos** (`reference/dialects.mdx`): alta fidelidad (incl. coerción empty-string→NULL en Oracle, JSON inlining con ORA-40454).
