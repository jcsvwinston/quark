# superapp — arnés de aceptación cross-engine de Quark

Arnés **headless** que ejerce la superficie pública de Quark contra los **6
motores** (SQLite, PostgreSQL, MySQL, MariaDB, MSSQL, Oracle) y **demuestra** la
cobertura: no afirma "lo probamos todo", lo reconcilia contra un manifiesto de
la API generado del propio código.

No es producto. Es la versión productizada y permanente del bug-bash F1–F14,
conducida por una capa de servicio→Quark y gateada por manifiesto. Complementa,
no sustituye, la suite unitaria/integración del repo.

## Requisitos

- **Go 1.25.7** (igual que el `go.mod` del repo).
- **Docker**: PG/MySQL/MariaDB/MSSQL vía testcontainers; **Oracle vía `docker run`**
  (no testcontainers — su lifecycle crashea en runners, igual que en
  `.github/workflows/ci.yml`), con `GRANT EXECUTE ON DBMS_LOCK` y pool corto
  (ORA-12516 en la imagen free).

## Layout

```
examples/superapp/
├── README.md            ← este blueprint
├── domain/              ← modelos que fuerzan la amplitud de la API
│   └── models.go
├── control/             ← maquinaria de cobertura/paridad/gate (stdlib, compila solo)
│   ├── capability.go    ← matriz de capacidad por motor (qué espera ErrUnsupportedFeature)
│   ├── report.go        ← status + matriz método×motor + gate
│   └── manifest.go      ← manifiesto de superficie + allowlist + reconciliador
├── recorder/            ← middleware+observer Quark → invocación por símbolo/motor + captura SQL
│   ├── recorder.go
│   ├── recorder_test.go ← e2e contra SQLite real
│   └── infra_test.go    ← [tag superapp_infra] Docker: OTel→Jaeger + logger + caché Redis
├── engine/              ← [pendiente] runner de los 6 motores (DSN, Oracle docker-run, teardown)
├── exercise/            ← [pendiente] exercisers por área + oráculo de paridad + asserts
├── cli/                 ← cobertura del binario cmd/quark (manifiesto de comandos, no de símbolos)
│   ├── doc.go
│   └── cli_test.go      ← [tag superapp_cli] exerciser de los 21 comandos + database-first (SQLite)
├── cmd/gen-apisurface/  ← [pendiente] genera apisurface.json con go/packages
├── apisurface.json      ← [generado] denominador: todo lo que Quark expone
├── allowlist.json       ← out-of-scope justificado (diferidos a v1.2)
└── main.go              ← [pendiente] wiring: corre exercisers, reconcilia, emite matriz, gatea
```

## Ejecución (cuando estén los slices pendientes)

```bash
# un motor
go run ./examples/superapp -engines=sqlite

# todos (Oracle requiere el contenedor levantado + QUARK_TEST_ORACLE_DSN)
go run ./examples/superapp -engines=all -gate=strict
```

DSNs por env (igual que la suite del repo): `QUARK_TEST_POSTGRES_DSN`,
`QUARK_TEST_MYSQL_DSN`, `QUARK_TEST_MSSQL_DSN`, `QUARK_TEST_ORACLE_DSN`. SQLite
corre en proceso.

## Mecanismos de control (mapa archivo → control)

1. **Cobertura de superficie** — `control/manifest.go` (`Reconcile`) + `recorder/` + `cmd/gen-apisurface`. *Gate principal.*
2. **Paridad cross-engine** — `exercise/` (oráculo diferencial; normaliza Oracle `''`→NULL, MSSQL `uuid`, wire UTC).
3. **Snapshots de SQL** — `recorder/` captura el SQL por método/dialecto → golden files.
4. **Aserciones funcionales** — cada `exercise/*` comprueba estado/retorno: cache hit = 0 SQL, N+1 acotado, `ErrStaleEntity`, audit atómico, eventos post-commit.
5. **Seguridad** — `exercise/security.go` (attack suite SQLGuard: identificadores/JSON-path/JOIN-ON hostiles → `ErrInvalid*`).
6. **Concurrencia/resiliencia** — `exercise/ha.go` (deadlock retry real, pool exhaustion sin leak, savepoints, routing de replicas, sharding sin fuga cross-shard).
7. **Migraciones/schema** — `exercise/migrate.go` (round-trip `Migrate`→`PlanMigration` vacío en los 6, `Sync`, Up/Down, `Backfill` con resume).
8. **Observabilidad** — `exercise/observability.go` (exporter OTel en memoria: counter + histogramas + redacción por defecto).
9. **Fugas/teardown** — `engine/` (goroutines + `sql.DBStats.InUse==0`, contenedores sin residuo).
10. **Reporte y gate** — `control/report.go` + `main.go` (matriz método×motor a REPORTS; falla si <100% in-scope o cualquier assert rojo).
11. **CLI (`cmd/quark`)** — `cli/` (cobertura del binario por manifiesto de COMANDOS, no de símbolos: build→exec→assert exit-code + golden output; allowlist para comandos diferidos). El gate de símbolos de S3 no aplica al `package main`; el contrato público del CLI es su interfaz cobra.

## Scope (honesto)

"100% cobertura" = 100% de la superficie **in-scope**. Lo fuera de scope vive en
`allowlist.json` con motivo: **F6-3b** (binder codegen UPDATE/partial/batch),
**scatter-gather + shard-key-from-entity**, **stampede cross-instancia**
(diferidos a v1.2 según `docs/ROADMAP.md`).

Capacidad desigual por motor (no es fallo): `RowLevelSecurityNative` y
`LISTEN/NOTIFY` son PG-only; el lock de migración no está en SQLite/Oracle. La
matriz de `control/capability.go` lo codifica y el exerciser exige
`ErrUnsupportedFeature` en esos motores.

## Estado de construcción

- [x] Blueprint + esqueleto (este drop)
- [x] Núcleo de control: capability/report/manifest (stdlib)
- [x] Dominio
- [x] recorder (`Middleware` símbolo→SQL por `context` + `QueryObserver` filas; `Mark`/`Note`/`Collect` → `control.Invoked`, `Statements` → snapshots; e2e SQLite verde)
- [ ] cmd/gen-apisurface + apisurface.json + allowlist.json
- [ ] engine matrix runner
- [ ] exercisers + paridad + asserts
- [~] CLI `cmd/quark` (S9): exerciser SQLite verde (`cli/`, 20/21 comandos + `tenant provision` en allowlist; database-first `model generate --from-table` → compila); falta manifiesto enumerado de cobra + golden output + cross-engine
- [ ] main + gate + CI

> Nota: este andamiaje se escribió **sin compilador** en el entorno de la sesión;
> compílalo con Go 1.25.7. Los slices pendientes se entregan compile-checked.
