---
type: playbook
module: tenant
files:
  - tenant_router.go
  - client.go
last_review: 2026-05-10
related_adrs: [0003, 0007]
related_p0: [P0-1]
phase: 0
---

# Playbook: Multi-Tenancy

## Qué cubrimos

Tres estrategias coexistentes (ADR 0007):

1. **`DatabasePerTenant`**: una DB por tenant. `TenantRouter` mantiene un LRU de `*Client` por tenant ID. Aislamiento físico fuerte.
2. **`SchemaPerTenant`** (sólo PG y MSSQL real, MySQL no tiene schemas): una DB, un schema por tenant. `q.schema = tenantID` en `client.go:170`.
3. **`RowLevelSecurity`** (renombrar a `RowLevelSecurityClient` en Fase 5, ver ADR 0003): inyecta `WHERE tenant_id = ?` en cada query del builder.

`TenantRouter` se construye con `quark.NewTenantRouter(config, factory)`. La estrategia se elige una vez por router; una aplicación puede tener varios routers con estrategias distintas.

`validTenantID` regex: `^[a-z0-9_-]+$`.

## Bugs P0 vivos

### P0-1 · `Or()` no propaga `tenantID/tenantCol` (FUGA DE AISLAMIENTO)

**Severidad: ALTA. Bug de seguridad explotable.**

**Localización**: `query_builder.go:175-186` (en el módulo query-builder, pero el impacto se siente aquí). `Or()` crea un `BaseQuery` blanco hardcodeado sin copiar los campos de aislamiento.

**Escenario que rompe**:

```go
// Con estrategia RowLevelSecurity, contexto del tenant A
result, _ := quark.For[Order](ctx, router).
    Where("status", "=", "pending").
    Or(func(q quark.QueryBuilder) {
        q.Where("status", "=", "paid")
    }).
    List()
// Resultado: incluye filas de tenant B, porque el grupo OR
// no llevó la condición tenant_id = A.
```

**Fix esperado**: `Or()` debe clonar TODOS los campos de aislamiento del padre. Patrón: extraer `(b *BaseQuery) cloneForGroup() BaseQuery` que copie `tenantID`, `tenantCol`, `schema`, `cache`, `limits`, `client`, `dialect`, `guard`.

**Test de regresión obligatorio**: levantar dos tenants en estrategia RLS, ejecutar la query con `Or` desde tenant A, y asertar:
1. El SQL emitido contiene `tenant_id = ?` (no se pierde).
2. No se devuelven filas de tenant B.

Tests en los 6 motores.

## Limitaciones críticas

### `RowLevelSecurity` NO es RLS de motor

Es WHERE-injection cliente. El comentario en el código lo admite (`tenant_router.go:28-29`).

**Consecuencias**:

- `client.Raw()` y `client.Exec()` se saltan la inyección. **Cualquier query que emita el caller fuera del builder NO está aislada.**
- Bugs en la propagación (P0-1 con `Or()`, futuros bugs con subqueries, joins, CTEs) son fugas de seguridad.

**Cómo gestionarlo hoy**:

1. Documentar muy visiblemente en `website/docs/multi-tenancy/row-level.md`.
2. Considerar emitir WARN en logs cuando `client.Raw()` se llama bajo un context que tiene tenantID.
3. Cualquier helper que clone `BaseQuery` debe propagar tenant explícitamente. El `code-reviewer` lo vigila.

**Plan**: ADR 0003. Fase 5 introduce `RowLevelSecurityNative` con `SET LOCAL app.tenant_id` + `CREATE POLICY` Postgres. La estrategia actual queda con sufijo `Client` y deprecation warning.

### Factory de nuevo tenant ejecuta bajo `mu.Lock`

`tenant_router.go:128` — el factory para crear un `*Client` nuevo se llama bajo el mutex del router. Si el factory es lento (DNS, TLS handshake, ping inicial), **bloquea todos los tenants en cola**.

Mitigación pendiente: `singleflight` por tenant ID. Hasta entonces, mantén factories rápidos (lazy ping, retries en background).

### Eviction de pool en goroutine sin esperar

`tenant_router.go:158` — al evictar de LRU, el `Close()` del pool ocurre en goroutine. Si el factory de un nuevo tenant entra mientras la goroutine cierra el pool antiguo, no afecta directamente, pero las métricas de connection-pool quedan inestables un momento.

### `SchemaPerTenant` no auto-crea schema

`client.go:170` mete `q.schema = tenantID`, y `fullTableName` lo cuotaiza correctamente. **Pero si el schema no existe en la DB**, las queries fallan con error de schema no encontrado.

Hoy el caller debe crear el schema manualmente al onboardear un tenant (`CREATE SCHEMA tenant_xxx`) antes de usar el router. No hay automatismo. Y las migraciones no se aplican automáticamente al schema nuevo — eso es responsabilidad del caller también.

Plan Fase 5: `quark tenant onboard <tenantID>` que crea el schema + aplica migraciones.

## Anti-patterns a vigilar

### Crear nuevos helpers que clonen `BaseQuery` sin propagar tenant

**Recurrente**. Cualquier vez que se introduzca `WhereGroup`, AST `And/Or/Not`, subqueries componibles, **deben propagar tenant**. Si no, replicas el bug P0-1 en sitio nuevo.

Patrón obligatorio:
```go
func (b BaseQuery) cloneForGroup() BaseQuery {
    return BaseQuery{
        ctx:       b.ctx,
        client:    b.client,
        dialect:   b.dialect,
        guard:     b.guard,
        tenantID:  b.tenantID,
        tenantCol: b.tenantCol,
        schema:    b.schema,
        cache:     b.cache,
        limits:    b.limits,
        // NO copiar where/orderBy/limit — eso es local al grupo
    }
}
```

### Saltarse el router con `client.Raw()` bajo contexto de tenant

```go
// MAL — bypass de aislamiento
client, _ := router.GetClient(ctx)
client.Raw().Query("SELECT * FROM orders")  // ¡sin filtro de tenant!

// BIEN
quark.For[Order](ctx, router).List()
```

Si necesitas raw bajo tenant context, **construye la query con el filtro explícito** y documéntalo:

```go
client, _ := router.GetClient(ctx)
tenantID := ctx.Value("tenant_id").(string)
client.RawQuery(ctx, "SELECT * FROM orders WHERE tenant_id = ?", tenantID)
```

### Asumir que `RowLevelSecurity` aísla mediante motor

Es disciplina aplicada por el ORM, no aislamiento del motor. No publicites como "Row-Level Security" sin cualificar — usa "tenant scoping", "client-side row filtering" o similar.

## Decisiones que afectan al módulo

- **ADR 0003**: RLS hoy es cliente, motor en Fase 5.
- **ADR 0007**: tres estrategias coexisten; cualquier helper debe respetarlas todas.

## Roadmap de mejora

- **Fase 0**: cerrar P0-1 (`Or()` propagation).
- **Fase 5**:
  - `RowLevelSecurityNative` con `SET LOCAL app.tenant_id` + Postgres policies.
  - `quark tenant install-rls-policies` CLI.
  - `quark tenant onboard <tenantID>` para `SchemaPerTenant`.
  - `singleflight` en factory.
  - Renombrar `RowLevelSecurity` → `RowLevelSecurityClient` con deprecation.

## Tests críticos a no romper

- (Pendiente de crear) `tenant_router_test.go` con suite multi-motor para las tres estrategias.

Hoy hay cobertura limitada — es deuda. Cualquier cambio en `tenant_router.go` debe traer su test de regresión que cubra al menos: `Or()`, `Where(group)`, joins, subqueries cuando existan.

## Cuándo invocar al `code-reviewer`

Antes de cualquier PR que toque `tenant_router.go`, `client.go` (en lo que respecta a `For[T]`), o cualquier helper de query que clone `BaseQuery`. El reviewer vigila especialmente:

- Propagación de `tenantID/tenantCol/schema` en clones.
- Cualquier `client.Raw()` o `Exec` bajo context de tenant lleva justificación explícita en el comentario.
- Documentación pública no afirma RLS de motor.
- Tests cubren las tres estrategias.
