---
id: 0012
title: RLS real Postgres vía `SET LOCAL app.tenant_id` + `CREATE POLICY`
status: accepted
date: 2026-05-15
deciders: jcsvwinston
related: [0003, 0007]
supersedes: 0003
tags: [security, multi-tenancy, postgres, phase-5]
---

# 0012 — RLS real Postgres vía `SET LOCAL` + `CREATE POLICY`

## Contexto

ADR-0003 (2026-05-10) declaró públicamente que la estrategia
`RowLevelSecurity` actual es **WHERE-injection cliente**, no Row-Level
Security de motor. El comentario en `tenant_router.go:27-29` lo admite
explícitamente:

```go
// RowLevelSecurity uses a single database connection pool and injects
// WHERE tenant_id = ? in every query the builder constructs.
RowLevelSecurity
```

`client.go:233-235` lo aplica:

```go
case RowLevelSecurity:
    q.tenantID = tenantID
    q.tenantCol = router.config.TenantColumn
```

Consecuencias documentadas en `docs/playbooks/tenant.md`:

- `client.Raw()` / `client.Exec()` **se saltan la inyección**. No es
  aislamiento, es disciplina.
- Bugs de propagación en clones de `BaseQuery` son fugas (el P0-1
  histórico en `Or()` lo demostró antes de cerrarse en v0.3.0).
- La documentación pública debe cualificar — no afirmar "Row-Level
  Security" sin matiz.

ADR-0003 dejó abierto el sucesor para Fase 5. Este ADR es ese sucesor.

## Decisión

**Fase 5 introduce `RowLevelSecurityNative`: una nueva estrategia
exclusiva de Postgres que delega el aislamiento al motor mediante
`SET LOCAL app.tenant_id = $1` por transacción y `CREATE POLICY` por
tabla.** Convive con la estrategia actual sin reemplazarla en el resto
de motores.

### Modelo de coexistencia

| Motor | Estrategia disponible para multi-tenancy de fila |
| --- | --- |
| PostgreSQL | `RowLevelSecurityNative` (recomendada) **o** `RowLevelSecurityClient` (compatibilidad) |
| MySQL / MariaDB / MSSQL / Oracle / SQLite | `RowLevelSecurityClient` (única opción de fila; o `SchemaPerTenant` / `DatabasePerTenant`) |

En PG, las dos modalidades son **mutuamente excluyentes por router**:
quien quiera aislamiento de motor configura `RowLevelSecurityNative`; la
defensa en profundidad (engine + WHERE-injection simultáneos) **no se
soporta**. Razón: complica el debug ("¿quién filtró, la policy o el
WHERE?") y crea casos en los que el cliente cree estar aislado por
motor cuando en realidad sólo el WHERE lo está filtrando (si la policy
se cayó por accidente).

En el resto de motores, `RowLevelSecurityClient` sigue siendo la única
estrategia de fila — no tienen RLS de motor equivalente.

### Renombrado y deprecation

- La constante actual `RowLevelSecurity` pasa a llamarse
  `RowLevelSecurityClient`. Se añade un alias deprecado
  `RowLevelSecurity = RowLevelSecurityClient` con `// Deprecated:` para
  no romper código existente.
- Se añade la constante `RowLevelSecurityNative` (PG-only).
- En `client.go:233`, el `switch` ramifica las dos: la rama
  `Native` **no** inyecta `q.tenantID/q.tenantCol`; en su lugar, el
  router se asegura de que toda query salga dentro de una tx con
  `SET LOCAL app.tenant_id = $tenantID` ya ejecutado.

### Cómo se ejecuta `SET LOCAL` por query

`SET LOCAL` requiere estar dentro de una tx. Tres modos:

1. **Caller usa `Client.Tx`**: el router intercepta `Tx` para emitir
   `SET LOCAL app.tenant_id = $1` como primer statement de la tx.
   Transparente.
2. **Caller usa `Query[T]` sin tx explícita**: el router envuelve la
   ejecución en una tx implícita de una sola query. Coste: una tx
   adicional por query no-batch. Aceptable a cambio del aislamiento.
3. **Caller usa `Client.Raw()`**: el router emite warning estructurado
   `quark.tenant.raw_under_native_rls` y **no** garantiza aislamiento.
   La policy de motor sigue activa (eso es lo importante) — pero si el
   raw no establece `SET LOCAL`, la policy ve `current_setting('app.tenant_id', true) = NULL`
   y bloqueará la fila. Comportamiento seguro por defecto.

### Generador de policies

`quark tenant install-rls-policies [--dry-run]` emite el SQL templated
por cada modelo registrado en el `Client`:

```sql
ALTER TABLE orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE orders FORCE ROW LEVEL SECURITY;  -- excluye al owner

CREATE POLICY orders_tenant_isolation ON orders
    USING (tenant_id = current_setting('app.tenant_id', true)::text);
```

Con `--dry-run` emite a stdout para revisión; sin flag aplica vía
`Client.AcquireMigrationLock` (F3-1) para evitar carreras con otros
nodos.

La nomenclatura de la columna sigue `TenantConfig.TenantColumn`
(default `tenant_id`). El tipo se infiere del modelo registrado
(`text` / `uuid` / `bigint`).

### `FORCE ROW LEVEL SECURITY`

Por defecto `CREATE POLICY` exime al owner de la tabla. Quark lo
fuerza con `ALTER TABLE ... FORCE ROW LEVEL SECURITY` para que ni
siquiera el rol que corre migraciones pueda saltarse la policy en
runtime. Razón: los procesos de aplicación corren con el mismo rol que
las migraciones en la mayoría de despliegues; sin `FORCE`, la policy
es decorativa.

### Onboarding y offboarding de tenants

Fuera del scope de este ADR; lo cubre `quark tenant onboard <id>` en
el playbook tenant (Fase 5 también, pero ítem F5 independiente).

## Consecuencias

**Positivas:**

- En PG, `client.Raw()` ya no es un boquete. La policy filtra incluso
  si el caller emite SQL fuera del builder.
- Cero overhead de inyección de `WHERE tenant_id = ?` en cada query
  cuando se usa Native — la policy lo hace en el plan.
- ADR-0003 queda formalmente cerrado.

**Negativas:**

- Dependencia de capacidades específicas de PG (Native sólo aplica
  ahí). El playbook tenant tendrá que documentar que la dicotomía
  cliente/motor es asimétrica entre motores. Aceptado: PG es el motor
  de referencia para multi-tenancy en producción según la encuesta
  informal de usuarios alpha.
- Una tx adicional por query no-batch en el modo (2). Mitigable: el
  caller que quiere rendimiento abre una tx explícita y batchea.
- Las migraciones deben aplicar `BYPASSRLS` al rol de migración si se
  usa `FORCE` agresivamente. Lo gestiona `quark tenant install-rls-policies`
  documentando explícitamente el rol esperado.
- Tests cross-engine: el suite tendrá que skipear Native fuera de PG
  con justificación explícita (no `t.Skip` por env var — regla CLAUDE.md
  #7).

**Coste de migración para usuarios actuales:**

- Quien use `RowLevelSecurity` hoy seguirá funcionando con el alias
  deprecado. En v1.0 el alias se retira.
- Quien quiera migrar a Native necesita: (a) renombrar constante,
  (b) correr `quark tenant install-rls-policies`, (c) verificar que
  ningún consumer de `client.Raw()` queda colgado sin policy aplicada.
  La doc de migración (`docs/MIGRATION_v0.9.0.md`, a crear en F5-4
  según TASKS.md) cubrirá el paso a paso al cerrar la fase.

## Alternativas descartadas

1. **Coexistencia simultánea (engine + WHERE) en PG**. Rechazada por
   complejidad de debug y falsa sensación de seguridad (apartado
   "Modelo de coexistencia").
2. **Sustituir `RowLevelSecurityClient` por Native en PG sin alias**.
   Rechazada: rompe a usuarios alpha con código en producción.
   Deprecation graceful manda hasta v1.0.
3. **Hacer Native multi-motor con emulación**. MySQL/MariaDB no tienen
   policies; emularlo desde el cliente equivale a... WHERE-injection.
   No aporta. Mantener honesta la matriz por motor.
4. **`SET app.tenant_id` (no LOCAL) en cada query**. Setea la variable
   de sesión sin ámbito de tx — quedaría stale entre tenants si el
   pool reutiliza conexión. `SET LOCAL` es la garantía correcta:
   la variable expira al `COMMIT`/`ROLLBACK`.
5. **`current_user`-based policies**. Requiere un rol PG por tenant
   (`SET ROLE tenant_xxx`). Operacionalmente caro: cada onboarding
   crea un rol y permisos. `SET LOCAL` con `current_setting` es el
   estándar de la comunidad PG para multi-tenancy SaaS — adoptado.

## Lo que esta decisión NO permite

- **NO** publicitar "Row-Level Security" en motores que no son PG.
  Cualquier doc fuera de PG debe usar "client-side tenant scoping".
- **NO** asumir que la policy cubre data hits a través del bus de
  eventos (Fase 5 también — ADR-0013). Los eventos llevan tenantID en
  el payload y el subscriber filtra.
- **NO** mezclar `RowLevelSecurityNative` y `RowLevelSecurityClient`
  en el mismo router (validación en `NewTenantRouter` rechaza la
  combinación).

## Cuándo reabrir

- Si MySQL 9.x o MariaDB 12+ introducen policies nativas equivalentes,
  evaluar extender Native a esos motores. Mientras tanto, asimetría
  documentada.
- Si aparece demanda real de "engine + WHERE simultáneos" como modo
  paranoid, abrir ADR sucesor que justifique con caso de uso concreto;
  rechazo por defecto.
