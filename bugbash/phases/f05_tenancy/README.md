# F5 — Multi-tenancy

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F5.
> Playbook: [`docs/playbooks/tenant.md`](../../../docs/playbooks/tenant.md).

## Qué prueba

Las estrategias de aislamiento de tenant que Quark soporta (ADR-0007/0012),
contra motores reales. Cierra la cobertura cross-engine que el playbook de
tenant marca como deuda ("`tenant_router_test.go` con suite multi-motor —
pendiente de crear").

## Grupos cubiertos

- **RLSClient** (los 6 motores) — `RowLevelSecurityClient`: inyección de
  `WHERE tenant_id = ?`. Verifica: cada tenant ve sólo sus filas; el write
  estampa el `tenant_id` (`ensureTenantID`); `Or()` no rompe el aislamiento
  (regresión de **P0-1**); un tenant no puede `UpdateMap`/`DeleteBy` filas de
  otro.
- **RLSClientConcurrent** (los 6 motores) — N goroutines, cada una fijada a un
  tenant, hacen CRUD entrelazado sobre el `BaseClient` compartido. Una fuga de
  propagación (estado mutable compartido filtrando un tenant entre goroutines)
  aparecería como una fila con `tenant_id` ajeno. Carga acotada (logueada); el
  objetivo de 10k ops sostenidas del spec es tier de F14 soak.
- **DatabasePerTenant** (SQLite) — el factory del router crea un pool por
  tenant (un fichero SQLite por tenant). Aislamiento físico. En motores
  contenedor usaría bases de datos separadas — fuera de scope de F5 (logueado
  en runtime, no es un skip silencioso: los demás grupos sí corren en todos
  los motores).
- **SchemaPerTenant** (PostgreSQL) — un schema por tenant. El caller crea el
  schema y migra (responsabilidad documentada en el playbook); F5 lo hace con
  DDL raw, enruta por `SchemaPerTenant` y verifica aislamiento. Scoped a PG
  (schemas son PG/MSSQL; MySQL conflaciona schema/database, SQLite no tiene).
- **RLSNative** (PostgreSQL) — aislamiento **forzado por el motor**. El test
  crítico conecta como un rol **NO superusuario** (el superusuario `postgres`
  se salta RLS incluso con `FORCE`, así que sólo un rol normal observa la
  policy) y prueba: (a) el path del builder aísla por tenant; (b) una query
  **raw** del rol sin la variable de tenant no ve **ninguna** fila — el motor,
  no el cliente, enforza (contraste con `RowLevelSecurityClient`, donde el raw
  se salta el aislamiento); (c) `WITH CHECK` rechaza un write cross-tenant. En
  cada motor no-PG la estrategia devuelve `ErrUnsupportedFeature` — **aserción,
  no `t.Skip`** (CLAUDE.md regla #7), vía `For[T]` y `router.Tx`.

## Severidades

Las fugas entre tenants se reportan **P0** (rompen la garantía de aislamiento);
los errores de operación (create/list falla) se reportan P1.

## Fuera de scope (y por qué)

- **10k ops concurrentes sostenidas** — tier de F14 soak; F5 corre una pasada
  acotada que ya expone la race de propagación Go-side.
- **DatabasePerTenant en motores contenedor** — requiere N bases de datos
  separadas; F5 lo demuestra en SQLite (N ficheros). Logueado.
- **SchemaPerTenant en MSSQL** — MSSQL tiene schemas, pero el DDL difiere; F5
  scopea a PG. MySQL/MariaDB/SQLite no tienen schemas reales.
- **`quark tenant onboard`** (auto-crear schema + migrar) — no existe aún
  (deuda documentada en el playbook); F5 hace el DDL a mano.

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestTenancy -v ./phases/f05_tenancy/                       # SQLite
go test -tags=bugbash -run TestTenancy -v ./phases/f05_tenancy/ -engines=all -timeout 40m
```

## Criterio done

- [x] RLSClient + concurrencia verde en los 6 motores; cero fugas entre
      tenants.
- [x] DatabasePerTenant (SQLite), SchemaPerTenant (PG), RLSNative (PG,
      engine-enforced vía rol no-superusuario) verdes.
- [x] RLSNative devuelve `ErrUnsupportedFeature` en los 5 motores no-PG.
