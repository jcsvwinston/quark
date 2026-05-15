---
id: 0007
title: Multi-tenancy — tres estrategias coexisten
status: accepted
date: 2026-05-10
deciders: jcsvwinston
related: [0003]
supersedes: null
tags: [multi-tenancy, architecture, scope]
---

# 0007 — Multi-tenancy: tres estrategias coexisten

## Contexto

La industria reconoce tres patrones canónicos de multi-tenancy en aplicaciones SaaS:

1. **Database-per-tenant**: cada cliente tiene su propia DB. Aislamiento físico fuerte, pero connection pools por tenant y migraciones × N.
2. **Schema-per-tenant** (sólo Postgres y MSSQL real, MySQL no tiene schemas distintos): una DB, un schema por tenant. Aislamiento de namespace, pools compartidos, migraciones más complejas.
3. **Row-Level (shared schema)**: todos los tenants comparten tablas con columna `tenant_id`. Máxima eficiencia de pool, mínimo aislamiento (depende de disciplina aplicada o de RLS de motor).

Cada patrón sirve a un caso de uso distinto y los proyectos serios suelen necesitar elegir uno desde el principio. Algunas plataformas (Salesforce) mezclan estrategias dentro de la misma instalación según tipo de dato.

Hibernate Multi-Tenancy soporta los tres; ent y GORM no tienen soporte nativo, requieren scopes a mano.

## Decisión

**Quark soporta las tres estrategias y las hace coexistibles en la misma aplicación**:

- `DatabasePerTenant`: `TenantRouter` mantiene un LRU de `*Client` por tenant; `client.go:130` rutea según contexto.
- `SchemaPerTenant`: `q.schema = tenantID` se inyecta en `client.go:170`; `fullTableName` añade el prefijo de schema al SQL.
- `RowLevelSecurityClient` (renombrado en Fase 5, F5-1; el nombre legacy `RowLevelSecurity` queda como alias deprecado hasta v1.0): inyecta `WHERE tenant_id = ?` en cada query del builder. Ver ADR-0012 para la modalidad de motor Native introducida en Fase 5.

Una aplicación puede usar `DatabasePerTenant` para datos críticos (PCI, médicos) y `RowLevelSecurityClient` para datos no sensibles, en el mismo proceso.

La estrategia se configura en `TenantRouterConfig` — no en runtime por query (a menos que el usuario instancie dos `TenantRouter` distintos).

## Consecuencias

**Positivas:**
- Diferenciador frente a GORM/ent que delegan multi-tenancy en el desarrollador.
- Atrae usuarios de Hibernate al ecosistema Go.
- Coherente con el target de Nucleus: aplicaciones empresariales con multi-tenancy real.

**Negativas:**
- Tres caminos = tres superficies de bugs. Cada cambio del query builder debe verificarse en las tres estrategias.
- Documentación más larga: el usuario debe entender los trade-offs antes de elegir.
- `RowLevelSecurityClient` es client-side (ver ADR-0012 sucesor de ADR-0003) — riesgo de que usuarios crean que es RLS de motor. El rename de F5-1 mitiga esa confusión nombrando el cliente explícitamente; `RowLevelSecurityNative` (F5-2, PG-only) entrega aislamiento por motor para quien lo necesite.

**Restricción que esto impone al query builder:**
Cualquier helper que clone `BaseQuery` (`Or`, `Where(group)`, futuro AST, subqueries) **debe propagar `tenantID/tenantCol/schema`** o la estrategia se rompe. Esto es la causa del bug P0-1 (`Or()` sin propagación) — y la razón por la que el `code-reviewer` lo lista como anti-pattern crítico.

**Restricción que esto impone a `Raw()` y `Exec`:**
Estas APIs **se saltan el TenantRouter**. La doc lo debe avisar; el `code-reviewer` debe vetar usos de `Raw()` en código que esté bajo contexto de tenant salvo justificación explícita.

## Alternativas consideradas

1. **Una sola estrategia (Row-Level con RLS de motor cuando se pueda).** Rechazado: cierra la puerta a aplicaciones que requieren aislamiento físico (DBPerTenant) — es un requisito frecuente en sectores regulados.
2. **Multi-tenancy como capa middleware sobre el ORM, no integrada.** Rechazado: la propagación a `Or()`, joins, subqueries requiere conocer el AST del query builder; un middleware externo no llega tan adentro.
3. **Sólo DBPerTenant (lo más simple).** Rechazado: en muchas aplicaciones SaaS pequeñas/medianas, una DB por cliente es overkill (pool explosión, migraciones costosas).

## Cuándo reabrir

- Cuando Fase 5 entregue `RowLevelSecurityNative`, este ADR se actualiza para listar 4 estrategias y describir cuándo elegir cada una.
- Si emerge un patrón nuevo (sharding por hash de tenantID, time-based partitioning), evaluar si entra como cuarta estrategia o como capa ortogonal.
