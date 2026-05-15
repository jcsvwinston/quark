---
type: playbook
module: tenant
files:
  - tenant_router.go
  - client.go
last_review: 2026-05-15
related_adrs: [0007, 0012]
related_p0: []
phase: 5
---

# Playbook: Multi-Tenancy

## Qué cubrimos

Tres estrategias coexistentes (ADR 0007). Tras ADR-0012 (Fase 5
apertura), las dos modalidades de fila se distinguen explícitamente:

1. **`DatabasePerTenant`**: una DB por tenant. `TenantRouter` mantiene un LRU de `*Client` por tenant ID. Aislamiento físico fuerte.
2. **`SchemaPerTenant`** (sólo PG y MSSQL real, MySQL no tiene schemas): una DB, un schema por tenant. `q.schema = tenantID` en `client.go:170`.
3. **`RowLevelSecurityClient`** (constante actual `RowLevelSecurity`,
   renombrado en F5-1 con alias deprecado): inyecta `WHERE tenant_id = ?`
   en cada query del builder. **Disponible en los 6 motores. NO es RLS
   de motor** — ver "Limitaciones críticas" abajo.
4. **`RowLevelSecurityNative`** (Fase 5, F5-2, PG-only): aislamiento
   por motor mediante `SET LOCAL app.tenant_id = $1` + `CREATE POLICY`
   por tabla. Mutuamente excluyente con `RowLevelSecurityClient` en el
   mismo router. Ver ADR-0012.

`TenantRouter` se construye con `quark.NewTenantRouter(config, factory)`. La estrategia se elige una vez por router; una aplicación puede tener varios routers con estrategias distintas.

`validTenantID` regex: `^[a-z0-9_-]+$`.

## Bugs P0 vivos

Sin P0 vivos en este módulo. Ver histórico abajo.

## Histórico — P0 cerrados

### P0-1 · `Or()` no propaga `tenantID/tenantCol` (cerrado en v0.3.0)

**Severidad original: ALTA — fuga de aislamiento.**

**Cerrado**: `query_builder.go` introdujo `(b *BaseQuery) cloneForGroup() BaseQuery` que copia `tenantID`, `tenantCol`, `schema`, `cache`, `limits`, `client`, `dialect`, `guard`. `Or()` usa ese clone, manteniendo el filtro en el grupo. Test de regresión cubierto en `p0_fixes_test.go`.

**Por qué se mantiene en el playbook**: el patrón `cloneForGroup` es la disciplina obligatoria para cualquier helper nuevo que clone `BaseQuery` (ver "Anti-patterns a vigilar" abajo). Si introduces `WhereGroup`, AST `And/Or/Not`, subqueries componibles — **deben** usar el clone, o replicas el bug en sitio nuevo.

## Limitaciones críticas

### `RowLevelSecurityClient` NO es RLS de motor

Es WHERE-injection cliente. El comentario en el código lo admite (`tenant_router.go:28-29`).

**Consecuencias**:

- `client.Raw()` y `client.Exec()` se saltan la inyección. **Cualquier query que emita el caller fuera del builder NO está aislada.**
- Bugs en la propagación (historial: P0-1 con `Or()`; futuros bugs con subqueries, joins, CTEs introducidos sin `cloneForGroup`) son fugas de seguridad.

**Cómo gestionarlo hoy**:

1. Documentar muy visiblemente en `website/docs/multi-tenancy/row-level.md`.
2. Considerar emitir WARN en logs cuando `client.Raw()` se llama bajo un context que tiene tenantID.
3. Cualquier helper que clone `BaseQuery` debe propagar tenant explícitamente. El `code-reviewer` lo vigila.

**Plan (ADR-0012, Fase 5)**: F5-1 rename `RowLevelSecurity` →
`RowLevelSecurityClient` con alias deprecado. F5-2 introduce
`RowLevelSecurityNative` exclusivo de PG con `SET LOCAL app.tenant_id` +
`CREATE POLICY`. En PG, las dos modalidades son mutuamente excluyentes
por router (ADR-0012 §"Modelo de coexistencia"). Resto de motores
conservan `RowLevelSecurityClient` como única opción de fila —
documentación pública debe seguir cualificando "client-side tenant
scoping" fuera de PG.

### Factory de nuevo tenant ejecuta bajo `mu.Lock`

`tenant_router.go:128` — el factory para crear un `*Client` nuevo se llama bajo el mutex del router. Si el factory es lento (DNS, TLS handshake, ping inicial), **bloquea todos los tenants en cola**.

Mitigación pendiente: `singleflight` por tenant ID. Hasta entonces, mantén factories rápidos (lazy ping, retries en background).

### Eviction de pool en goroutine sin esperar

`tenant_router.go:158` — al evictar de LRU, el `Close()` del pool ocurre en goroutine. Si el factory de un nuevo tenant entra mientras la goroutine cierra el pool antiguo, no afecta directamente, pero las métricas de connection-pool quedan inestables un momento.

### `SchemaPerTenant` no auto-crea schema

`client.go:170` mete `q.schema = tenantID`, y `fullTableName` lo cuotaiza correctamente. **Pero si el schema no existe en la DB**, las queries fallan con error de schema no encontrado.

Hoy el caller debe crear el schema manualmente al onboardear un tenant (`CREATE SCHEMA tenant_xxx`) antes de usar el router. No hay automatismo. Y las migraciones no se aplican automáticamente al schema nuevo — eso es responsabilidad del caller también.

Plan (deuda heredada, fuera de scope explícito de los F5-N pero seguramente cae en algún PR de F5-2/F5-3): `quark tenant onboard <tenantID>` que crea el schema + aplica migraciones. No bloquea la apertura formal de Fase 5.

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

### Asumir que `RowLevelSecurityClient` aísla mediante motor

Es disciplina aplicada por el ORM, no aislamiento del motor. No publicites como "Row-Level Security" sin cualificar — usa "tenant scoping", "client-side row filtering" o similar. La modalidad de motor real es `RowLevelSecurityNative` (PG-only, F5-2).

## Decisiones que afectan al módulo

- **ADR-0012** (Fase 5): RLS real PG vía `SET LOCAL` + `CREATE POLICY`;
  modalidad Native PG-only, Client en resto de motores. Supersede
  ADR-0003.
- **ADR-0007**: tres estrategias coexisten; cualquier helper debe
  respetarlas todas.
- **ADR-0003** (superseded por ADR-0012): histórico que documenta por
  qué la modalidad Client se admitió como WHERE-injection antes de
  Fase 5.

## Roadmap de mejora

- **Fase 5** (apertura formal hecha; entrega esperada v0.9.0; ver
  TASKS.md §"Fase 5"):
  - F5-1 — Renombrar `RowLevelSecurity` → `RowLevelSecurityClient` con
    deprecation.
  - F5-2 — `RowLevelSecurityNative` con `SET LOCAL app.tenant_id` +
    Postgres policies (PG-only, exclusivo con Client en mismo router).
  - F5-3 — `quark tenant install-rls-policies` CLI generador de DDL.
  - (Fuera de scope explícito de F5) `quark tenant onboard <tenantID>`
    para `SchemaPerTenant`, `singleflight` en factory — deuda menor
    documentada abajo.

**Deuda menor heredada (no bloquea Fase 5)**:

- Factory bajo `mu.Lock` en `tenant_router.go:128` (apartado abajo) —
  pendiente `singleflight` por tenant ID.
- `SchemaPerTenant` no auto-crea schema — pendiente CLI `onboard`.

## Tests críticos a no romper

- (Pendiente de crear) `tenant_router_test.go` con suite multi-motor para las tres estrategias.

Hoy hay cobertura limitada — es deuda. Cualquier cambio en `tenant_router.go` debe traer su test de regresión que cubra al menos: `Or()`, `Where(group)`, joins, subqueries cuando existan.

## Cuándo invocar al `code-reviewer`

Antes de cualquier PR que toque `tenant_router.go`, `client.go` (en lo que respecta a `For[T]`), o cualquier helper de query que clone `BaseQuery`. El reviewer vigila especialmente:

- Propagación de `tenantID/tenantCol/schema` en clones.
- Cualquier `client.Raw()` o `Exec` bajo context de tenant lleva justificación explícita en el comentario.
- Documentación pública no afirma RLS de motor.
- Tests cubren las tres estrategias.
