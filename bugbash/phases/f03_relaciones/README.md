# F3 — Relaciones

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F3.

## Qué prueba

Cada tipo de relación que Quark soporta, en combinaciones realistas sobre el
dominio ERP. Siembra un grafo pequeño y determinista (org → cliente → 2 pedidos
→ líneas → producto → categoría → padre; usuario → perfil / roles; pedido →
eventos de auditoría) y carga cada relación vía `Preload`, incluyendo caminos
con puntos profundos y un árbol autorreferencial. Un middleware que cuenta
consultas demuestra que el preload agrupa (sin N+1).

## Grupos cubiertos

- **belongs_to:** `Order.Customer`, `User.Organization`, `Product.Category`,
  `Category.Parent` (autorreferencial, FK `*int64`).
- **has_many:** `Customer.Orders`, `Order.Lines` / `Order.Payments`,
  `Product.Inventory`, `Payment.Refunds`.
- **has_one:** `User.Profile` (1:1).
- **many_to_many:** `User.Roles` por la join table `user_roles`.
- **polymorphic:** `Order.AuditEvents` — sólo carga filas con
  `subject_type = "Order"`, excluyendo un evento de tipo `User` que comparte el
  mismo `subject_id` numérico (prueba el filtro por literal de tipo, no sólo
  por id).
- **Nested dotted:** `Orders.Lines.Product.Category.Parent` (5 niveles,
  mezclando has_many y belongs_to) desde un único `Customer`.
- **SelfRefTree:** `Category.Children.Children` (root → A → A1).
- **NoNPlusOne:** listar todos los clientes con `Preload("Orders")` emite
  ~2 consultas (1 clientes + 1 pedidos por `IN`), no 1+N — verificado con un
  middleware contador (`quark.WithMiddleware`).
- **CascadeCreateBelongsTo:** `Create` con un `belongs_to` poblado guarda el
  padre y rellena la FK; verificado en memoria y tras recargar.

## Fuera de scope (y por qué)

- **Preload con `Where` en la relación.** La API `Preload` sólo acepta nombres
  de relación — no hay variante con callback/condición — así que no hay nada
  que ejercitar. Es un **gap de API**, no un fallo: si se quiere filtrar la
  carga hoy hay que hacer una query separada con `Where`.
- **Cascade `Create` de has-many** (`Create(&order)` con `order.Lines`
  poblado). Quark autoguarda padres `belongs_to` en `Create`, no hijos
  has-many (`query_crud.go`). F3 verifica el cascade que **sí** existe
  (belongs_to) y deja el lado has-many documentado como no soportado.
- **Propagación de tenant a través de loads** y el **límite de IN-chunk de
  Oracle (1000)**: territorio de F5 (multi-tenancy) y F4 (volumen). F3
  mantiene el grafo pequeño y monotenant.
- **CTE recursivo para el árbol de categorías:** la travesía del árbol se cubre
  funcionalmente con el preload anidado autorreferencial; la generación de SQL
  de CTE ya la cubre F2.

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestRelations -v ./phases/f03_relaciones/                  # SQLite
go test -tags=bugbash -run TestRelations -v ./phases/f03_relaciones/ -engines=all -timeout 20m
```

## Criterio done

- [x] Cobertura funcional de los 5 tipos de relación + nested + polymorphic +
      N+1.
- [x] **Verde 9/9 en los 6 motores** (SQLite + PostgreSQL + MySQL + MariaDB +
      MSSQL + Oracle) — pasada Docker 2026-05-31, sin hallazgos abiertos.

## Hallazgos de la 1ª pasada (2026-05-31, en `TASKS.md` § "Bug-bash hallazgos")

Los tres se arreglaron y verificaron cross-engine en el mismo PR que añade
esta fase.

- **BB-5** (cerrado) — `Preload` de cualquier relación con FK *nullable*
  (`*int64`) cargaba `nil`/vacío: el match padre/hijo se indexaba por el valor
  crudo del campo, y una clave `*int64` nunca igualaba a la PK `int64` de la
  fila relacionada. Afectaba al árbol autorreferencial (`Parent`/`Children`
  sobre `ParentID *int64`) y al belongs_to opcional (`OrderID *int64`). Fix:
  `normalizeKey` en `preload_loaders.go`. Regresión: `preload_nullable_fk_test.go`.
- **BB-6** (cerrado) — MSSQL: insertar un `Nullable[[]byte]` NULL fallaba
  (`nvarchar`→`varbinary`). Fix: `nullBytesArg` sustituye por `[]byte(nil)`
  tipado. El seed deja `UserProfile.Avatar` NULL a propósito → esta fase es la
  regresión cross-engine. Regresión unit: `nullable_bytes_test.go`.
- **BB-7** (cerrado) — Oracle: `many_to_many` (`User.Roles`) cargaba 0 filas.
  Dos defectos en `loadM2M`: el scan de la fila relacionada no hacía `ToLower`
  del nombre de columna (Oracle las devuelve en mayúsculas) y el scan de la
  join era a `interface{}` (go-ora devuelve `NUMBER` como `string`). Fix:
  `ToLower(col)` + scan tipado a las PK. Regresión: esta fase sobre Oracle.
