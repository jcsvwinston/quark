# F2 — API surface coverage

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F2.

## Qué prueba

Que un caso por método exportado del query builder produce SQL válido y el
resultado esperado en cada motor. Crea su propia fixture pequeña y
determinista (1 org, árbol de categorías, productos, 1 cliente, 4 pedidos
con líneas, 1 usuario con `version`) y ejercita la API contra ella.

## Grupos cubiertos

- **Predicados:** `Where`(operadores), `WhereIn`, `WhereBetween`, `WhereNot`,
  `WhereJSON`, `Or` (grupo), `WhereExpr` + Expr AST (`Col`/`Lit`/`Eq`/`Ne`/
  `Gt`/`And`/`Or`), subconsulta (`InSub`, `Exists` vía `AsSubquery`).
- **Agregados:** `Sum`/`Avg`/`Min`/`Max` (→ `float64`), `Count`; `GroupBy` +
  `Having`/`HavingAggregate`/`HavingExpr` (execute-only — la fila agregada
  no mapea a `T`).
- **Orden/paginación:** `OrderBy`, `Limit`, `Offset`, `Distinct`, `Paginate`
  (`*Page[T]`).
- **Streaming:** `Iter` (callback), `Cursor` (`Next`/`Scan`/`Close`).
- **Joins:** `Join`/`LeftJoin` + `On`.
- **Set ops:** `Union`/`UnionAll` (todos); `Intersect`/`Except`
  **dialect-aware** — MySQL/MariaDB devuelven `ErrUnsupportedFeature` (el test
  lo espera, no lo reporta como fallo).
- **Locking:** `ForUpdate`/`ForShare`/`SkipLocked`/`NoWait` (no-op en SQLite;
  execute-only).
- **Soft delete:** `Delete`/`WithTrashed`/`OnlyTrashed`/`Restore`/`Unscoped`.
- **Batches:** `CreateBatch`/`UpsertBatch`/`UpdateBatch`/`DeleteBatch`/
  `UpdateMap`/`DeleteBy`.
- **Optimistic locking:** colisión real → `ErrStaleEntity`.
- **Preload:** anidado (`Customer.Orders`, `Order.Lines.Product`).
- **Window / CTE:** `Over`+`RowNumber`/`Rank`/`Lag` vía `SelectExpr`; `With`
  no-recursivo (execute-only).

## Diferido (no en este PR)

- **`coverage.json` ≥95%** (medición automática de superficie vía
  `tools/coverage.go`) — el criterio cuantitativo de F2 queda pendiente; esta
  pasada entrega la cobertura *funcional* de los grupos de arriba.
- **`WhereP` / columnas tipadas** — requiere codegen (`quark gen`), opt-in y
  diferido (ADR-0002).
- **CTE recursivo real** — `WithRecursive` no puede modelar el `UNION` interno
  con la superficie actual de `Subquery` (limitación documentada en
  `cte.go`); el walk recursivo de `categories` queda fuera.

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestAPISurface -v ./phases/f02_api_surface/...                  # SQLite
go test -tags=bugbash -run TestAPISurface -v ./phases/f02_api_surface/... -engines=all -timeout 20m
```

## Criterio done

- [x] Cobertura funcional de los grupos listados, verde en los 6 motores
      (con BB-2/3/4 filtrados como hallazgos conocidos — ver abajo).
- [ ] `coverage.json` ≥95% de métodos exportados (diferido).

## Hallazgos de la 1ª pasada (2026-05-28, en `TASKS.md` § "Bug-bash hallazgos")

- **BB-2** — `Join` sobre query tipada hace `SELECT *` sin acotar a la tabla
  base (+ filtro soft-delete sin cualificar): colisión de columnas duplicadas
  (ambiguous `deleted_at`/`id`, NULL-scan en outer joins). El grupo `Joins`
  valida la generación de SQL vía `AsSubquery`, no ejecuta el join-en-`T`.
- **BB-3** — MariaDB rechaza `FOR SHARE` (sintaxis MySQL-8 en dialecto
  compartido). Tolerado como hallazgo conocido en el grupo `Locking`.
- **BB-4** — Oracle `ForUpdate` + el `Limit` implícito de `List()` → ORA-02014.
  Tolerado como hallazgo conocido en `Locking`.

`GroupBy` con el `SELECT *` por defecto es SQL inválido en motores estrictos
(only_full_group_by / ORA-00979): no es bug de Quark, el test proyecta sólo la
columna agrupada. PG y SQLite quedaron limpios. Cualquier diff nuevo →
`reporter.Fail` → `TASKS.md`.
