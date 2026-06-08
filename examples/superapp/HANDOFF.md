# Handoff a Claude Code — superapp de aceptación cross-engine

> Para la sesión de Code que continúe este trabajo. **Lee primero**:
> `examples/superapp/README.md` (blueprint), `TASKS.md` § "Superapp", y las
> firmas/tags que se citan abajo. Arranca con `/next-session auto` — este trabajo
> es el foco propuesto (no es P0; no gatea por la regla 4 de `CLAUDE.md`).

## Objetivo

Arnés **headless** en `examples/superapp/` que ejerce TODA la superficie pública
de Quark contra los 6 motores y **demuestra** la cobertura reconciliándola
contra un manifiesto generado del código. Es la versión permanente del bug-bash
F1–F14 conducida por una capa servicio→Quark y gateada por manifiesto.
Complementa, no sustituye, la suite del repo.

## Premisas (no negociables)

1. **Cobertura demostrada, no afirmada.** Cada símbolo del manifiesto queda
   invocado en cada motor o justificado en `allowlist.json`. El gate estricto
   falla si no se cumple.
2. **Headless**, dentro de `examples/superapp/`, sin framework web y **sin deps
   nuevas** si se puede (stdlib: `runtime`, `database/sql` DBStats).
3. **6 motores.** PG/MySQL/MariaDB/MSSQL por testcontainers (ya en `go.mod`);
   **Oracle por `docker run gvenzl/oracle-free:23-slim`** (NO testcontainers —
   crashea), con `GRANT EXECUTE ON DBMS_LOCK` y pool corto (ORA-12516). Replica
   `.github/workflows/ci.yml:138-172`.
4. **Capacidad desigual ≠ fallo.** `RowLevelSecurityNative` y `LISTEN/NOTIFY`
   son PG-only; el lock de migración no está en SQLite/Oracle. Espera
   `quark.ErrUnsupportedFeature` ahí (matriz en `control/capability.go`).
5. **Reglas del repo (`CLAUDE.md`).** Conventional Commits sin mezclar tipos;
   `code-reviewer` + `docs-auditor` antes de PR; **sin lenguaje de marketing**;
   API+docs en el mismo PR; los 6 motores verdes antes de merge a `main`; nada
   de `t.Skip` por env var (build tags / testcontainers). Di **archivo:línea**
   antes de tocar.
6. **Slices compilables.** Cada paso termina compilando y corriendo al menos en
   SQLite. El slice 1 se escribió **sin toolchain Go** en el entorno de origen:
   el primer `go build ./examples/superapp/...` (Go 1.25.7) es tuyo — corrige
   firmas si algo no cuadra.

## Hecho (slice 1 — working tree, sin compilar en origen)

- `README.md` (blueprint), `control/{capability,report,manifest}.go` (solo
  stdlib, compila aislado), `domain/models.go` (tags verificados vs
  `website/docs/guides/modeling.mdx`).

## Orden de trabajo

**S2 · `recorder/`** — Lee primero las firmas reales: `option.go`
(`WithQueryObserver`, `WithMiddleware`, tipo del observer), `cache.go`
(`CacheStore`), el paquete raíz (`For`, `ForTx`, `New`, `Client.Tx`),
`errors.go` (sentinels). Implementa un observer que registre `(símbolo, engine,
sql, dur, rows)`; el símbolo se estampa por `context` en cada call-site del
exerciser. Expón `control.Invoked` y la captura de SQL (para los snapshots).

**S3 · `cmd/gen-apisurface/`** — `go/packages`+`go/types` sobre `quark` y los
subpaquetes públicos (`cache/memory`, `cache/redis`, `otel`, `migrate`,
`quarkmigrate`, `quarktenant`) → `apisurface.json` (vía `go:generate`). Crea
`allowlist.json` con los diferidos a v1.2 (claves exactas `Symbol.Key`): F6-3b
(binder codegen UPDATE/partial/batch), scatter-gather + shard-key-from-entity,
stampede cross-instancia.

**S4 · `engine/`** — Runner SQLite en proceso + matriz. Luego testcontainers
(PG/MySQL/MariaDB/MSSQL) y Oracle docker-run. Teardown + chequeo de fugas
(goroutines, `DBStats.InUse==0`).

**S5 · `exercise/`** — Empieza por `crud.go` como patrón canónico (asserts
funcionales + hook de paridad), luego `builder.go` (CTE/window/setops/locking),
`relations.go` (**confirma tags m2m/polimórfica vs
`website/docs/guides/relations.mdx`**), `tx.go`, `cache.go` (query-count:
hit=0 SQL, N+1 acotado), `tenant.go`, `migrate.go` (round-trip
`Migrate`→`PlanMigration` vacío), `security.go` (attack suite SQLGuard),
`ha.go` (replicas/sharding/deadlock), `observability.go` (OTel in-memory).

**S6 · `main.go`** — flags `-engines`, `-gate`; corre exercisers por motor,
`Reconcile`, `Render` la matriz a `REPORTS/`, `Gate`.

**S7 · CI** — job que corre la superapp en los 6 (patrón `integration` de
`ci.yml`; Oracle docker-run). Gate estricto bloqueante.

**S8 · cierre** — snapshots SQL golden estables, paridad completa, página
pública si el sidebar lo pide (regla 3: docs en el mismo PR).

## Definición de hecho (gate)

`apisurface.json` reconciliado al **100% in-scope** en los 6 motores (o
allowlist justificada), todos los asserts funcionales/seguridad/paridad en
verde, matriz emitida a `REPORTS/`, y CI verde.

## No te dejes

- Los desfases **Doc-sync DS-1..DS-5** (`TASKS.md` § "Doc-sync") siguen
  pendientes de verificación: `cd website && npm run build`, confirmar el mínimo
  real de Go con compilador (DS-4), y la propagación de `quark-docs` en
  release-notes históricas (DS-3). Ciérralos.
