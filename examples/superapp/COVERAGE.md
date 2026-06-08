# Cobertura de la API de Quark por el superapp

> **Qué es esto.** Un inventario *manual* de qué superficie pública de Quark
> ejerce cada componente del arnés de aceptación, hoy. Es un mapa de "qué se ha
> tocado", no el gate: el denominador autoritativo (todo lo que Quark expone) lo
> generará **S3 (`cmd/gen-apisurface` → `apisurface.json`)** y el reconciliador
> de `control/manifest.go` lo cruzará contra lo invocado. Hasta entonces, este
> documento evita que algo "se escape" sin querer.
>
> **Cómo regenerarlo** (cuando se añadan slices): `grep` sobre las cuatro
> superficies — ver § "Regeneración" al final.

El arnés ejerce a Quark desde **cuatro superficies**, cada una con un foco:

| Superficie | Foco | Cómo toca a Quark |
| --- | --- | --- |
| `domain/` | Modelado | Tags + tipos en structs |
| `recorder/` | Observabilidad / instrumentación | `Middleware` + `QueryObserver` + CRUD en el e2e |
| `cli/` | Binario `cmd/quark` | **Indirecto**: el test ejecuta el binario; éste llama al ORM |
| `workload/` | Runtime a volumen | La mezcla más amplia: CRUD/tx/cache/relaciones |

---

## 1. `domain/models.go` — superficie de modelado

| Concern | API empleada |
| --- | --- |
| Tags de columna | `db:"…"`, `pk:"true"` (×7), `quark:"unique,not_null,version,tz=…,size=…"`, `default:"…"`, `validate:"…"` |
| Relaciones | `rel:"belongs_to"` (×3), `rel:"has_many"` (×2), `rel:"many_to_many"` (×1), `join:"…"` |
| Tipos ricos | `quark.JSON[T]`, `quark.Array[string]`, `quark.Nullable[time.Time]`, `quark.Nullable[string]` |
| Hooks / iface | `BeforeCreate`, `BeforeUpdate`, `Validate(ctx)`, `TableName()` |
| Patrones | PK compuesta (Membership), soft-delete (`deleted_at`), optimistic lock (`version`), binario (`[]byte`, `Nullable[[]byte]`) |

## 2. `recorder/` (S2) — hooks de observabilidad

| Concern | API empleada |
| --- | --- |
| Construcción | `quark.New`, `quark.DefaultLimits`, `quark.WithLimits` |
| Middleware | `quark.WithMiddleware` + iface `quark.Middleware` (`WrapExec`/`WrapQuery`/`WrapQueryRow`); tipos `ExecFunc`/`QueryFunc`/`QueryRowFunc`; `quark.Executor` |
| Observer | `quark.WithQueryObserver` + iface `quark.QueryObserver` (`ObserveQuery`, `quark.QueryEvent`) |
| CRUD en el e2e | `For[T]`, `.Create`, `.List`, `.First`, `.Delete`, `.Where`, `.Limit`, `.Count`, `.Cache` |
| `infra_test.go` (tag `superapp_infra`) | `quark.WithLogger`, `quark.WithSlowQueryThreshold`, `quark.WithCacheStore` (+ `cache/redis`, `otel.New`); `quark.WithReplicas` (marcado por `Note`, no ejecutado) |

## 3. `cli/` (S9) — ejercido **indirectamente** vía el binario `cmd/quark`

El test ejecuta el binario (`go run`/exec); los comandos llaman internamente al ORM:

| Comando | API ORM interna |
| --- | --- |
| inspect / validate / model-gen | `GetTableInfo` (introspección, ×5), `client.Raw`, `client.Dialect` |
| migrate up/down/status/version | `migrate.NewMigrator`, `.Up`, `.Down`, `.GetApplied` |
| sync | `client.Sync`, `client.Migrate` |
| tenant list/migrate/migrate-all | `client.Exec`, `client.Raw` |
| gen | `codegen.Load` / `Render` / `Generate` (codegen forward F6) |
| (todos) | `quark.New` con `WithLimits{AllowRawQueries:true}`, `client.Close` |

> Nota: la cobertura del CLI es a nivel **comando** (build→exec→assert), no a
> nivel símbolo Go — `cmd/quark` es `package main` y su contrato es la interfaz
> cobra. Ver `cli/doc.go`.

## 4. `workload/` — runtime a alto volumen

| Concern | API empleada |
| --- | --- |
| Construcción + obs. | `quark.New`, `WithLimits` (MaxResults/SafeMigrations), `WithLogger`, `WithSlowQueryThreshold`, `WithCacheStore` (`cache/memory`), recorder (mw+observer) |
| Migración | `client.Migrate` |
| Escritura | `.CreateBatch` (×6, con chunking), `.Create` |
| Transacciones | `client.Tx`, `quark.ForTx[T]` (Account, Project) |
| Lectura | `.List`, `.First`, `.Count`, `.Paginate`, `.Where`, `.OrderBy`, `.Limit` |
| Relaciones | `.Preload` (has_many / belongs_to) |
| Caché | `.Cache(ttl)` (hit-rate) |
| Mutación | `.Update`, `.Delete` (soft-delete) |
| Tipos ricos en seed | `quark.JSON`, `quark.Array` |
| Cierre | `client.Close` |

---

## Consolidado — superficie cubierta hoy

- **Construcción/opciones:** `New`, `DefaultLimits`, `WithLimits`, `WithMiddleware`, `WithQueryObserver`, `WithLogger`, `WithSlowQueryThreshold`, `WithCacheStore`, `WithReplicas`(declarado).
- **CRUD:** `Create`, `CreateBatch`, `Update`, `Delete`.
- **Lectura:** `List`, `First`, `Count`, `Paginate`.
- **Builder:** `Where`, `OrderBy`, `Limit`, `Offset`, `Preload`, `Cache`.
- **Tx:** `Tx`, `ForTx`.
- **Schema (vía CLI):** `Migrate`, `Sync`, migrator `Up`/`Down`/`GetApplied`, `GetTableInfo`.
- **Tipos:** `JSON`, `Array`, `Nullable`.
- **Subpaquetes:** `cache/memory`, `cache/redis`, `otel`.

## Aún SIN ejercer (lo cubrirá S5 `exercise/`)

Esto es lo que falta para que el gate de S3 cierre al 100% in-scope:

- **Lectura avanzada:** streaming `Iter()` / `Cursor()`, `Upsert` / `UpsertBatch`.
- **Builder avanzado:** `Join` / CTE / window / setops / locking (`FOR UPDATE … SKIP LOCKED`).
- **JSON:** `WhereJSON` (JSON-path) — y el guard de path (`ValidateJSONPath`).
- **Multi-tenancy real:** `DBPerTenant` / `SchemaPerTenant` / `RowLevelSecurityClient` / `RowLevelSecurityNative` (PG) — hoy solo se declara la estrategia, no se ejerce el aislamiento.
- **HA en ejecución:** réplicas (read/write split, failover) y sharding (routing por shard key) — hoy `WithReplicas` se *declara* pero no se *ejerce*.
- **Eventos / auditoría:** `EventBus`, `OnCommit` / `OnRollback`, audit log + diffs.
- **Seguridad:** attack-suite de `internal/guard.SQLGuard` (identificadores/JSON-path/JOIN-ON hostiles).
- **Schema programático:** `Sync` con diffs (ADD/RENAME/DROP COLUMN), `PlanMigration`, `Backfill` resumible.
- **Codegen runtime:** paridad generated-vs-reflect (hoy el CLI ejercita `gen`, pero no se corre el binder/scanner generado contra reflect).

---

## Regeneración

Para refrescar este inventario tras añadir slices, desde `examples/superapp/`:

```bash
# CRUD/builder/options por componente
grep -rhoE 'quark\.[A-Z][A-Za-z]+(\[[a-zA-Z.]+\])?|client\.[A-Z][A-Za-z]+\(|\.(Create|CreateBatch|List|First|Count|Paginate|Update|Delete|Where|OrderBy|Limit|Offset|Cache|Preload|Iter|Cursor|Upsert|Join|Tx)\(' \
  recorder/ workload/ cmd/ | sort | uniq -c | sort -rn

# Tipos/tags del dominio
grep -rhoE 'quark\.(JSON|Array|Nullable)\[[a-zA-Z.]+\]|rel:"[a-z_]+"|pk:"true"' domain/models.go | sort | uniq -c

# API que ejerce el binario CLI (indirecto)
grep -rhoE 'GetTableInfo|NewMigrator|codegen\.(Load|Render|Generate)|\.(Up|Down|GetApplied|Sync|Migrate|Raw|Dialect|Exec)\(' \
  ../../cmd/quark/commands/*.go ../../cmd/quark/internal/db/*.go | sort | uniq -c
```

Cuando exista `apisurface.json` (S3), este documento pasa a ser una vista humana
y el gate cuantitativo manda.
