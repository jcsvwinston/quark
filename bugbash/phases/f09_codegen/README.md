# F9 — Codegen

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F9.
> Superficie: `codegen_registry.go` (registry + version gate + `CheckGeneratedDrift`
> + `ModelHash` + `GeneratedBinderRegistered`), `typed_columns.go` (`WhereP`,
> `TypedColumn`), `cmd/quark gen` (el generador). ADR-0014.

## Qué prueba

Que el codegen opt-in (`quark gen`) mantiene **paridad** con el reflect path, y
que los gates de coexistencia (contract-version, drift, PK no-entero) degradan a
reflect sin error silencioso.

## Topología / artefacto generado

`model/quark_gen.go` está **commiteado**, emitido por el binario real
`quark gen ./phases/f09_codegen/model/` — el flujo del consumidor externo (el
módulo de bug-bash **no puede importar** el generador interno de Quark). Su
`init()` registra:

- **`Account`** (PK entera) → scanner tipado **+ binder INSERT tipado** (v3).
- **`Doc`** (PK string) → scanner tipado + **`quark.StubBinder`** → su write path
  **cae a reflect** (el binder v3 es sólo para PK entera).

El paquete `model/` no lleva build tag (para que el generador y el toolchain lo
vean); el test (`//go:build bugbash`) lo importa, disparando el registro.

## Grupos cubiertos (SQLite)

- **GeneratedParity** — una fila escrita+leída por `Account` (scanner+binder
  generados) es idéntica a la misma fila por un gemelo reflect-only → cero drift.
- **NonIntPKBinderFallsBack** — `Doc` tiene scanner generado pero **no** binder
  real (`GeneratedBinderRegistered==false`); el write cae a reflect y aun así
  round-trip correcto.
- **WherePSQLParity** — `WhereP(AccountColumns.Email.Eq(v))` emite SQL
  **byte-idéntico** a `Where("email","=",v)` (capturado vía `QueryObserver`).
- **VersionGateFallsBack** — código generado registrado bajo una contract-version
  **vieja** se ignora: la runtime usa reflect, **sin error silencioso** (el
  scanner/binder falsos, que panican si se invocan, nunca se alcanzan).
- **DriftDetected** — `CheckGeneratedDrift` marca un modelo cuyo hash guardado ya
  no coincide con su forma (cambiado-sin-regenerar), y reporta `Account` (en
  sync) como no-drifted.
- **DryRunWritesNothing** — `quark gen --dry-run ./model` imprime el source a
  stdout y **no toca** el `quark_gen.go` commiteado. (Construye el binario en el
  módulo raíz y lo ejecuta desde el módulo de bug-bash.)

## Fuera de scope (logueado)

- **Parity cross-engine F1+F2**: el scanner generado enruta cada campo por el
  mismo `quark.ScanTarget` que reflect, así que la paridad vale en los 6 motores;
  SQLite es la prueba barata. La cobertura cross-engine vive en F1.
- **Binder de UPDATE/partial/batch** (F6-3b): diferido; sólo el binder INSERT es
  v3. No se ejercita aquí.
- **Regeneración del artefacto en CI**: el `quark_gen.go` se commitea; el spec de
  "cambiar un modelo sin regenerar" se cubre con `VersionGateFallsBack` +
  `DriftDetected` (mecanismo), no recompilando en vivo.

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestCodegen -v ./phases/f09_codegen/
# Regenerar el artefacto (si cambias model/):
go build -o /tmp/quarkgen ../cmd/quark && /tmp/quarkgen gen ./phases/f09_codegen/model/
```

## Hallazgos (en `TASKS.md` § "Bug-bash hallazgos")

**Sin hallazgos.** Pasada 2026-06-02 (SQLite): 6/6 grupos verdes. Paridad
generated-vs-reflect, gate de versión y drift, fallback de PK no-entera, SQL de
`WhereP` byte-idéntico, y `--dry-run` no-escribe, todos sólidos. Fase test-only
(sin cambio de código de producción).

## Criterio done

- [x] Round-trip idéntico generated vs reflect.
- [x] `WhereP` produce SQL byte-idéntico a `Where`.
- [x] Contract-version vieja → degrada a reflect sin error silencioso.
- [x] PK no-entera → binder cae a reflect conscientemente.
- [x] Drift detectado (`CheckGeneratedDrift`).
- [x] `--dry-run` no escribe archivos.
