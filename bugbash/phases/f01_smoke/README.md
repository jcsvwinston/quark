# F1 — Smoke per engine

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F1.

## Qué prueba

Que las primitivas CRUD y el round-trip de cada tipo rico funcionan en
cada motor contra el dominio real — no contra una tabla de juguete.

## Ejercita

- **Round-trip de tipos** (INSERT → SELECT → compara): `uuid.UUID`,
  `decimal.Decimal` (con y sin precision/scale), `JSON[T]` struct y
  `JSON[map[string]any]`, `Array[T]`, `time.Time` con TZ por columna,
  `time.Duration`, `[]byte`, `Nullable[T]` (set y NULL).
- **CRUD**: `Create` / `Find` / `Count` / `Update` / `UpdateFields`
  (campo único) / `List`+`Where` / `Delete` (soft) / `HardDelete`.

Es self-contained: crea sus propias filas, no depende del seed grande
(eso es F4).

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestSmoke -v ./phases/f01_smoke/...                 # SQLite
go test -tags=bugbash -run TestSmoke -v ./phases/f01_smoke/... -engines=postgres
go test -tags=bugbash -run TestSmoke -v -timeout 20m ./phases/f01_smoke/... -engines=all
```

Los fallos se reportan con `reporter.Fail` (loud vía `t.Errorf` + JSONL en
`BUGBASH_REPORT_DIR/failures.jsonl` cuando el harness lo fija). La fase **no
aborta al primer fallo** — agrega y deja que `bugbash-reporter` clasifique.

## Criterio done

- [x] Cero diffs INSERT↔SELECT para cada tipo en cada motor.
- [x] CRUD primitivo verde en los 6 motores.

Corrida 2026-05-28 sobre **los 6 motores** (SQLite/PG/MySQL/MariaDB/MSSQL/
Oracle): 6/6 verde. La 1ª pasada destapó **BB-1** (uuid corrompido en MSSQL
si se mapea a `UNIQUEIDENTIFIER` — byte-order; ver `TASKS.md` § "Bug-bash
hallazgos"); el harness mapea uuid a `VARCHAR(36)` en MSSQL (camino
documentado de Quark) y queda verde. Cualquier diff futuro por motor es un
hallazgo real → entra en `TASKS.md` § "Bug-bash hallazgos".
