# F0 — Install & boot

> Primera fase del bug-bash. Verifica que un consumidor externo puede
> instalar Quark desde cero y arrancarlo contra cada motor. Spec:
> [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F0.

## Qué prueba

Que `quark.New(dialect, dsn)` conecta y que `Migrate(...)` crea el
**dominio entero** ([`bugbash/domain`](../../domain)) sin fallar, en cada
motor seleccionado.

## Ejercita

- `tools.Up(engines)` — boot por `docker run` (no testcontainers, patrón
  CI / ADR-0018), con override por `BUGBASH_DSN_<ENGINE>`.
- `quark.New(driver, dsn)` con DSNs de producción típicos.
- `client.Raw().PingContext(ctx)` — la conexión responde.
- `client.Migrate(ctx, domain.AllModels()...)` — las 20 tablas migran,
  incluida la join table explícita `user_roles` (composite PK) y los tipos
  ricos (decimal vía mapper, uuid vía mapper, JSON[T], Array[T], time.Time
  con TZ, Nullable[T], BLOB).

## Cómo correr

```bash
cd bugbash

# Sólo SQLite (sin Docker) — la ruta mínima end-to-end
go test -tags=bugbash -run TestInstallAndMigrate -v ./phases/f00_install/...

# Un motor con contenedor
go test -tags=bugbash -engines=postgres -v ./phases/f00_install/...

# Los 6 motores
go test -tags=bugbash -engines=all -timeout 20m -v ./phases/f00_install/...
```

El flag `-engines` acepta `all` o una lista CSV
(`sqlite,postgres,mysql,mariadb,mssql,oracle`). Default: `sqlite`.

## Criterio done

- [x] SQLite: dominio entero migrado + ping.
- [ ] 6/6 motores: dominio entero migrado + ping (corre en CI / manual con
      Docker; PG verificado en el PR de bootstrap).
- [ ] Codegen: `quark gen ./bugbash/domain/` emite `quark_gen.go` que
      compila. **Pendiente** — leg de codegen de F0, fuera del scope del PR
      de bootstrap (el binder/scanner generado es opt-in, ADR-0002).

La parte de codegen se cierra en una pasada posterior; el bootstrap deja
F0 arrancable en su forma básica (instalar, conectar, migrar).
