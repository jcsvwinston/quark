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
├── COVERAGE.md          ← inventario de la API de Quark ejercida por fase (snapshot manual)
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
├── engine/              ← runner por motor (docker-run, no testcontainers) + anti-fugas (goroutines/pool)
│   ├── engine.go        ← Up/Down/waitReady (espeja bugbash); SUPERAPP_DSN_<ENGINE> override
│   ├── leak.go          ← Run(): client por motor → fn → Close → verifica pool=0 + goroutines
│   ├── engine_test.go   ← SQLite in-process, sin Docker
│   └── engine_docker_test.go ← [tag superapp_engine] Postgres docker-run real
├── exercise/            ← exercisers por área (reusan engine.Run) + cobertura por símbolo
│   ├── suite.go         ← Run(): recorder por motor → exercisers → cobertura (control.Invoked)
│   ├── crud.go          ← patrón canónico: Create/First/Count/Update/Delete(soft)/List
│   └── tx.go            ← transacciones: commit multi-entidad + rollback atómico
├── cli/                 ← cobertura del binario cmd/quark (manifiesto de comandos, no de símbolos)
│   ├── doc.go
│   └── cli_test.go      ← [tag superapp_cli] exerciser de los 21 comandos + database-first (SQLite)
├── workload/            ← carga de alto volumen + informe ejecutivo (datos relacionados/tx/caché)
│   ├── workload.go      ← seed masivo + mezcla de queries/tx/cache; métricas vía recorder
│   └── report.go        ← executive-report.md + metrics.json
├── cmd/
│   ├── workload/        ← runnable: go run … → REPORTS/workload-<stamp>/{report,metrics,log}
│   └── gen-apisurface/  ← go/packages+go/types → apisurface.json (determinista; go:generate)
├── REPORTS/             ← [generado, gitignored] artefactos de cada corrida del workload
├── apisurface.json      ← [generado, versionado] denominador: 655 símbolos en 7 paquetes
├── allowlist.json       ← out-of-scope justificado (Symbol.Key → motivo)
└── main.go              ← [pendiente] wiring: corre exercisers, reconcilia, emite matriz, gatea
```

## Ejecución (cuando estén los slices pendientes)

```bash
# un motor
go run ./examples/superapp -engines=sqlite

# todos (Oracle requiere el contenedor levantado + QUARK_TEST_ORACLE_DSN)
go run ./examples/superapp -engines=all -gate=strict
```

### Carga de alto volumen + informe ejecutivo (ya disponible)

```bash
# SQLite, ~93k filas relacionadas (escala ×3 por defecto)
go run ./examples/superapp/cmd/workload

# más volumen / otro motor
go run ./examples/superapp/cmd/workload -scale=10
go run ./examples/superapp/cmd/workload -driver=pgx -dsn="$QUARK_TEST_POSTGRES_DSN"
```

Siembra datos relacionados (accounts→projects→tasks, memberships con PK
compuesta, attachments binarios), ejerce queries/paginación/preload, transacciones
multi-entidad, updates/deletes y lecturas cacheadas; con el recorder midiendo cada
statement. Emite a `REPORTS/workload-<stamp>/`: **`executive-report.md`** (volumen,
perfil SQL, latencias p50/p95/p99, hit-rate de caché, throughput), `metrics.json`
y `quark.log` (slog JSON: fases + slow-query WARN). Flags: `-scale`, `-driver`,
`-dsn`, `-out`, `-slow-ms`.

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

"100% cobertura" = 100% de la superficie **in-scope** (655 símbolos hoy). Lo
fuera de scope vive en `allowlist.json` con motivo (`Symbol.Key → razón`). Nota
verificada al generar el manifiesto: los **diferidos a v1.2** (F6-3b binder
codegen, scatter-gather + shard-key-from-entity, stampede cross-instancia) **no
están en el denominador** —no son símbolos exportados hoy (el binder vive en
internals de codegen; scatter-gather/stampede son features futuras sin símbolo)—
así que **no necesitan entrada de allowlist**. La allowlist se reserva para
símbolos que SÍ existen pero el exerciser no debe cubrir (p.ej. el alias
deprecado `RowLevelSecurity`).

Capacidad desigual por motor (no es fallo): `RowLevelSecurityNative` y
`LISTEN/NOTIFY` son PG-only; el lock de migración no está en SQLite/Oracle. La
matriz de `control/capability.go` lo codifica y el exerciser exige
`ErrUnsupportedFeature` en esos motores.

## Estado de construcción

- [x] Blueprint + esqueleto (este drop)
- [x] Núcleo de control: capability/report/manifest (stdlib)
- [x] Dominio
- [x] recorder (`Middleware` símbolo→SQL por `context` + `QueryObserver` filas; `Mark`/`Note`/`Collect` → `control.Invoked`, `Statements` → snapshots; e2e SQLite verde)
- [x] cmd/gen-apisurface + apisurface.json (655 símbolos, determinista) + allowlist.json (S3)
- [x] engine matrix runner (`engine/`, S4): docker-run + anti-fugas; verde en SQLite in-process + Postgres docker-run (pool 0/0, goroutines estables)
- [~] exercisers (S5, en curso): harness `suite.go` + `crud` + `tx` verdes en SQLite y PG real; faltan builder/relations/cache/tenant/migrate/security/ha/observability + oráculo de paridad
- [~] CLI `cmd/quark` (S9): exerciser SQLite verde (`cli/`, 20/21 comandos + `tenant provision` en allowlist; database-first `model generate --from-table` → compila); falta manifiesto enumerado de cobra + golden output + cross-engine
- [x] workload de alto volumen + informe ejecutivo (`workload/` + `cmd/workload/`): ~310k filas / 0 errores en SQLite ×10; report.md + metrics.json + quark.log
- [ ] main + gate + CI

> Nota: este andamiaje se escribió **sin compilador** en el entorno de la sesión;
> compílalo con Go 1.25.7. Los slices pendientes se entregan compile-checked.
