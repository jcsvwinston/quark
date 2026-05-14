---
id: 0010
title: Timezones por columna — híbrido Client default + tag, wire UTC
status: accepted
date: 2026-05-14
deciders: jcsvwinston
related: [0002]
supersedes: null
tags: [types, time, query-builder, schema]
---

# 0010 — Timezones por columna — híbrido Client default + tag, wire UTC

## Contexto

`time.Time` era el último tipo diferido del Bloque B de Fase 1
(`TASKS.md` § Fase 1). Quark v0.6 hacía pass-through: el `time.Time`
del struct iba al driver tal cual y volvía tal cual, con la zona que
cada driver decidiera. Eso deja dos problemas sin resolver:

1. **Sin contrato cross-engine.** PostgreSQL con `TIMESTAMPTZ` puede
   devolver la hora en la zona de la sesión; SQLite guarda lo que le
   pasen como texto; MySQL sin `parseTime` devuelve `[]byte`. El mismo
   modelo persiste instantes potencialmente distintos según el motor.
2. **Sin control de la zona en memoria.** Una app que quiere ver sus
   timestamps en `Europe/Madrid` tenía que hacer `.In(loc)` a mano en
   cada lectura.

El diseño se discutió y cerró en la sesión post-v0.6.0 (2026-05-14);
las seis decisiones quedaron ancladas en `TASKS.md` antes de abrir
esta implementación (PR de preload de decisiones, #62).

## Decisión

### 1. Estrategia híbrida

Dos knobs, con precedencia **tag de columna → default del Client →
nil (pass-through)**:

- `quark.WithDefaultTZ(loc *time.Location)` — Client option, fallback
  para columnas `time.Time` sin tag.
- `quark:"tz=Europe/Madrid"` — tag de columna, override per-columna.
  Tag-key `tz`, alineado con `size` / `precision` / `scale` del F1-4.

Cubre el caso mono-tz (una sola `WithDefaultTZ`) y el multi-tz (tag
sobre la columna que lo necesite) sin bifurcar la API.

### 2. Semántica wire: UTC siempre

Cuando una columna resuelve a una `*time.Location` no nula:

- **Bind**: el `time.Time` se convierte a UTC antes de ir al driver.
  Todos los motores guardan el mismo instante.
- **Scan**: el valor se lee y se le aplica `.In(loc)` en memoria.

`loc` afecta sólo a cómo el struct Go ve el valor, nunca a lo que se
persiste. Es lo que hace SQLAlchemy con `timezone=True` y lo que `pgx`
asume por defecto.

### 3. Validación fail-fast

`time.LoadLocation` se ejecuta **eager**, en `computeModelMeta` (una
vez por tipo, cacheado). Una zona IANA inválida no se parsea en cada
query: se registra en `ModelMeta.TZError` y la superan fail-fast
`Client.RegisterModel` y `Client.Migrate`, que la envuelven en el
sentinel público `ErrInvalidTimezone`. Una app con un typo en el tag
rompe al arrancar, no en la primera query que toca la columna.

### 4. `Nullable[time.Time]` respeta el tag

El bind detecta `sql.Null[time.Time]` (== `quark.Nullable[time.Time]`)
y aplica la conversión al `.V` interno cuando `.Valid`. El scan instala
un `nullableTimeScanner` que reusa el `timeScanner` robusto (manejo de
`[]byte` / `string`) y reconstruye el wrapper. Contrato uniforme con
`time.Time` directo y `*time.Time`.

### 5. Sin opt-in: pass-through del driver

Sin `WithDefaultTZ` y sin tag, el comportamiento es exactamente el de
v0.6 — el `time.Time` pasa al driver intacto. La feature es 100%
aditiva; una app que actualiza a v0.7 sin tocar nada no nota ningún
cambio. El hot path de bind/scan gatea sobre `BaseQuery.tzActive()`
(lectura de un flag O(1)): modelos y clients sin tz no pagan ni un
lookup ni un type switch (respeta ADR-0002, sin reflect adicional).

### 6. Tipos custom vía `RegisterTypeMapper`: gap documentado

Un tipo del usuario que envuelva `time.Time` con su propio
`Scanner` / `Valuer` **no** es interceptado por el bind/scan de Quark.
El contrato tz aplica a `time.Time`, `*time.Time` y
`Nullable[time.Time]`. Quien registre un tipo-tiempo custom maneja su
zona en su propio `Scanner` / `Valuer`. No se expande el bind layer
para cubrir esto.

## Consecuencias

**Positivas:**

- Contrato wire estable cross-engine: el mismo instante se persiste
  igual en los 5 motores con CI + SQLite.
- La zona en memoria es declarativa (tag) o configurable (Client),
  sin `.In(loc)` manual repartido por el código de la app.
- Cero coste para quien no use la feature; cero breaking changes.

**Negativas:**

- El bind dejó de ser un `v.Field(i).Interface()` plano: pasa por
  `bindColumnArg`, que en ~7 call sites (insert, update, updateMap,
  batch, upsert ×2, tenant col) resuelve la columna. Mitigado por el
  gate `tzActive()` — sin tz, retorno inmediato.
- `OpAlterColumn` de F3-3 no propaga cambios de tz (no es un cambio de
  tipo SQL); cambiar el tag de una columna no genera migración. No es
  un problema real: el tag no altera el tipo de columna, sólo la
  representación en memoria.

## Alternativas descartadas

- **Tag-only** (sin Client default): verboso en apps con muchos
  timestamps que comparten zona.
- **Client-option-only** (sin override per-columna): rompe el caso
  "esta tabla legacy está en horario local".
- **Respetar la `loc` del valor en wire** (no normalizar a UTC):
  reintroduce el drift cross-engine que esta ADR resuelve.
- **Pre-registrar `shopspring/decimal` / `google/uuid`**: fuera de
  alcance; el usuario los registra con `RegisterTypeMapper` (F1-4).

## Cuándo reabrir

Si aparece demanda de PG-native `TIMESTAMPTZ` con offset preservado
(en vez de UTC normalizado) o de tz dinámica por-fila (no por-columna),
abrir un ADR sucesor. El diseño actual asume tz estática declarada en
el schema.
