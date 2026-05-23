---
id: 0014
title: Codegen coexiste vía registry de funciones tipadas por tipo con fallback a reflect
status: accepted
date: 2026-05-22
implemented: F6-1
deciders: jcsvwinston
related: [0001, 0002]
supersedes: null
tags: [architecture, performance, codegen, phase-6]
---

# 0014 — Mecanismo de coexistencia codegen ↔ reflect

## Contexto

[ADR-0002](0002-reflect-default-codegen-fase-6.md) fijó la **política**:
reflect por defecto y permanente, codegen **opt-in** en Fase 6, **misma
API pública**, y "el codegen instala implementaciones tipadas que
reemplazan las reflect-paths internamente". Lo que ADR-0002 dejó abierto
es el **mecanismo** de esa instalación. Fase 6 (items F6-1..F6-4)
necesita anclarlo antes de escribir el generador, porque condiciona la
forma del código emitido y los hooks que el runtime debe exponer.

Las hot paths reflect viven hoy en `scanRow` / `loadRelations`
(lectura), `buildInsert` / `buildUpdate` / `buildUpdateMap` / `saveAny`
(escritura) y la construcción de condiciones en `query_builder.go`.
`buildUpdateMap` es el path de `UpdateFields` (partial update) — el
binder tipado de F6-3 debe cubrirlo, no sólo el INSERT/UPDATE completo.
Cualquier mecanismo debe poder reemplazar al menos lectura y escritura
sin bifurcar `Query[T]`.

## Decisión

> **Enmienda 2026-05-22 (pre-implementación, ADR aún `proposed`):** el
> generador se entrega como **subcomando `quark gen` del binario
> `cmd/quark`** y obtiene la metadata del modelo **parseando el código
> fuente del usuario con `go/packages` + `go/types` (AST)**, no por
> reflexión. Un binario standalone instalado vía `go install` no puede
> reflejar tipos que no compiló; el AST es la única vía para un
> `quark gen` instalable. Se prioriza la UX (`//go:generate quark gen`,
> sin `main.go` thin) sobre la reutilización de `internal/schema`. Esto
> reemplaza la asunción previa (reflexión / "reusa internal/schema
> meta") que arrastraban TASKS.md e issue #92.

El código generado se registra en **registries package-level keyed por
`reflect.Type`**, uno por capability:

- `typedScanners[reflect.Type]` — función que escanea un `*sql.Rows` a un
  `*T` sin reflect.
- `typedBinders[reflect.Type]` — función que devuelve columnas + args de
  un `*T` sin reflect, para INSERT/UPDATE.
- Accesores de columna tipados (`Where().Name().Eq("x")`) — azúcar
  **compile-time** que se expone como API generada aparte; no reemplazan
  nada en runtime.

Mecánica:

1. `quark gen ./pkg` (subcomando de `cmd/quark`) carga el paquete del
   usuario con `go/packages` (`NeedTypes|NeedSyntax`), encuentra los
   structs con tags `db:`/`pk:`, resuelve sus tipos con `go/types`
   (incluidos los genéricos `quark.JSON[T]`/`Array[T]`/`Nullable[T]`), y
   emite un fichero `*_quark_gen.go` por package con un `func init()`
   que llama a los registradores exportados (`quark.RegisterTypedScanner` /
   `RegisterTypedBinder` / `RegisterGeneratedMeta`). Son exportados —y no
   `registerTyped*` como decía el primer sketch— porque el `init()` generado
   vive en el paquete del usuario (externo a `quark`); coincide con la
   etiqueta "superficie semi-pública" de §Restricciones. (Implementado en
   F6-1.)
2. En runtime, `scanRow` / `buildInsert` consultan el registry por
   `reflect.Type` **antes** de caer al path reflect. Hit → fast-path sin
   reflect. Miss → reflect (comportamiento actual, sin cambio).
3. La API pública (`quark.For[T]`, `Query[T]`) es **idéntica** con o sin
   codegen. La única diferencia observable es la latencia.

Decisiones derivadas:

- **Opt-in puro**: sin generación el registry está vacío y todo cae a
  reflect. Coste para quien no usa codegen = un map-lookup por op,
  despreciable.
- **Sin DSL de esquema**: a diferencia de ent/sqlc, que *generan* los
  structs, Quark genera **accesores para structs escritos a mano**. El
  modelo Go sigue siendo la fuente de verdad (coherente con ADR-0001,
  Active Record).
- **El código generado no aparece en firmas públicas** (restricción de
  ADR-0002): el registry es interno; quien no usa codegen importa todo
  igual.

## Consecuencias

**Positivas:**
- API estable: el usuario no elige entre dos sabores hasta que necesita
  el upgrade, y al activarlo no reescribe código.
- Adopción incremental: se puede generar sólo los modelos en hot paths.
- Fallback transparente: codegen ausente o stale degrada a reflect, no
  rompe.

**Negativas:**
- Un map-lookup por op en el path actual (medible en benchmark;
  esperado despreciable — F6-8 lo confirma).
- **Codegen stale es silencioso**: si el modelo cambia y no se re-corre
  `quark gen`, el runtime usa reflect sin avisar. Se detecta por
  benchmark/CI, no por crash. F6-1 debe emitir un hash del modelo para
  que un check opcional avise de drift.
- **Dos intérpretes de tags `db:` (consecuencia del enfoque AST)**: el
  runtime parsea los tags por reflexión (`internal/schema`); el
  generador los parsea por AST. Pueden divergir en silencio (se añade
  una opción de tag al runtime y se olvida el generador → código
  generado que no coincide con el runtime). **Mitigación**: un test de
  conformidad que, para un modelo de muestra, compare la metadata que
  deriva el generador (AST) contra la que deriva `internal/schema`
  (reflexión) y falle si difieren. Es deuda inherente al binario
  standalone; la alternativa (reflexión) no la tendría pero pierde la
  UX de `go install`.
- Generador y runtime hay que mantenerlos en sync.

## Alternativas consideradas

1. **Build tags (reflect vs codegen).** Rechazado: produce dos binarios,
   confunde, y no permite adopción por-modelo.
2. **Interface en el modelo (`TypedScanner`).** Rechazado: obliga al
   usuario a implementar/embeber código generado en su struct; el
   `init()` + registry es invisible y no contamina el modelo.
3. **Reemplazar reflect del todo (sin fallback).** Rechazado: viola
   ADR-0002 — reflect es el default permanente y el on-ramp idiomático.

## Restricciones que esta decisión impone

- Toda hot path reflect nueva debe exponer un punto de extensión obvio
  (lookup en el registry antes del reflect) — ya exigido por ADR-0002.
- Los registradores (`RegisterTypedScanner`, etc.) son superficie
  semi-pública: cambiarlos rompe el código generado de versiones
  previas. Versionar el contrato del generador (un `//quark:gen vN`
  header) desde F6-1.

## Cuándo reabrir

- Si Go añade acceso estático a campos de struct sin reflect suficiente
  para el ORM completo (haría el codegen innecesario).
- Si el map-lookup resulta medible en el benchmark de F6-8 (improbable,
  pero el dato manda).
