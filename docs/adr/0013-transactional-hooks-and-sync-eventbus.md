---
id: 0013
title: Hooks transaccionales + EventBus síncrono en commit-phase
status: accepted
date: 2026-05-15
deciders: jcsvwinston
related: [0001, 0004]
supersedes: null
tags: [hooks, events, transactions, phase-5]
---

# 0013 — Hooks transaccionales + EventBus síncrono en commit-phase

## Contexto

`hooks.go` define hoy seis interfaces (`BeforeCreateHook`,
`AfterCreateHook`, `BeforeUpdateHook`, `AfterUpdateHook`,
`BeforeDeleteHook`, `AfterDeleteHook`) que `query_crud.go` invoca
alrededor de cada operación CRUD. Faltan:

- **`BeforeFindHook` / `AfterFindHook`** (lectura). Útiles para soft-delete
  por defecto, scoping condicional, audit de lecturas.
- **`OnCommit(tx, fn)` / `OnRollback(tx, fn)`** (transaccionales). Hoy no
  hay forma de registrar un side-effect que dependa de que la tx
  commitee. La pauta GORM/Rails es la convención que la comunidad Go
  espera.
- **Semántica clara de "dentro / fuera de la tx"** para los hooks
  existentes. Hoy se ejecutan **fuera** porque no hay tx asociada al
  hook — si el caller usa `client.Tx`, los hooks no se enganchan al
  commit, lo que produce desajustes (un `AfterCreate` que actualiza un
  contador en otra tabla puede correr y luego la tx falla por
  conflict — el contador queda inconsistente).

Paralelamente, `events.go` define un `EventBus` con `Notify()` y un
`CreateListener()` (`events.go:50`) que devuelve `ErrDialectNotSupported`:

```go
// NOTE: EventBus is experimental in V1. Native LISTEN/NOTIFY (PostgreSQL)
// requires a dedicated connection with a driver-specific implementation
// (e.g., github.com/lib/pq). This will be completed in a future release.
func (eb *EventBus) CreateListener() (EventListener, error) {
    return nil, fmt.Errorf("...")
}
```

`Notify()` sólo funciona en PG (`pg_notify`). Es un placeholder. Fase 5
necesita un `EventBus` real que emita `Created`/`Updated`/`Deleted` por
modelo a un sink configurable (logger / OTel / NATS / Kafka). Sin esta
pieza, F5-1 (RLS real) y F5-4 (audit log) quedan tuertos: la audit
table necesita capturar el diff junto al commit, no después.

## Decisión

Fase 5 establece **tres reglas semánticas** para hooks y eventos:

### Regla 1 — `Before*` corren dentro de la tx; un error aborta el commit

Cuando el caller usa `Client.Tx`, los hooks `Before*` se ejecutan
**dentro** de esa tx. Si el hook devuelve error, la tx se rolleaba —
no se requiere `panic` ni callback adicional. Esto convierte los hooks
en parte legítima del flujo transaccional.

Cuando el caller usa la API "sin tx explícita" (`Query[T].Create()`,
etc.), el cuerpo del CRUD envuelve la operación en una tx implícita y
los hooks corren dentro. **Cambio respecto al comportamiento actual,
breaking — minor**. Se documenta en `MIGRATION_v0.9.0.md`.

`BeforeFind` / `AfterFind` se añaden con la misma semántica para
lecturas. `BeforeFind` puede modificar el query (ej. inyectar
soft-delete filter), `AfterFind` recibe la slice cargada.

### Regla 2 — `After*` corren tras commit OK; un error queda como warning, NO revertea

Esto es el cambio importante. Hoy los `After*` corren inline tras la
ejecución del CRUD; si fallan, el error se propaga al caller, pero la
tx (cuando existe) ya está comprometida.

A partir de Fase 5: los `After*` se **encolan en el `*Tx`** y disparan
**fuera de la tx después del commit**. Si la tx hace rollback —ya sea
por un error en `Before*`, por un error de driver durante el CRUD
(constraint violation, deadlock no-retry-able, etc.), o por rollback
explícito— los `After*` encolados se **descartan en su totalidad**.
Esto incluye el caso de tx implícita: si `Query[T].Create` envuelve en
tx implícita y el `INSERT` falla, el `AfterCreate` encolado no se
ejecuta. Si un `AfterUpdate` devuelve error **después** del commit OK,
se loguea como `quark.hook.after_post_commit_error` con OTel span —
**no se revertea la tx**, porque ya commiteó. Eso es honesto: una vez
la tx está confirmada en disco, ningún hook puede deshacerla.

### Regla 3 — `OnCommit(fn)` / `OnRollback(fn)` para side-effects arbitrarios

API nueva sobre `*Tx`:

```go
err := client.Tx(ctx, func(tx *Tx) error {
    if _, err := quark.For[Order](ctx, tx).Create(o); err != nil {
        return err
    }
    tx.OnCommit(func(ctx context.Context) error {
        return bus.Emit(ctx, OrderCreated{ID: o.ID})
    })
    tx.OnRollback(func(ctx context.Context) error {
        log.Warn("order create rolled back", "id", o.ID)
        return nil
    })
    return nil
})
```

`OnCommit` / `OnRollback` callbacks se acumulan en orden FIFO; al
disparar, se ejecutan **secuencialmente**. Un error en cualquiera se
loguea como `quark.hook.on_commit_error` / `quark.hook.on_rollback_error`
y **no para la cadena** — el resto de callbacks corren. Razón: la tx
ya cerró; bloquear la cadena no recupera estado.

### EventBus síncrono en commit-phase

`EventBus.Emit(ctx, event)` se elimina como callsite directo del CRUD.
En su lugar, la integración estándar es:

```go
tx.OnCommit(func(ctx context.Context) error {
    return bus.Publish(ctx, event)
})
```

Más un helper opt-in:

```go
client.UseEventBus(bus)   // wires CRUD → bus.Publish via OnCommit
```

que el `Query[T].Create/Update/Delete` engancha automáticamente. La
emisión es **síncrona** y **at-least-once**. Si el `Publish` falla,
**la fila ya está persistida** — el caller tiene que decidir si
reintentar el emit (idempotencia del subscriber recomendada en doc).

> **Enmienda F5-6 (2026-05-21):** el texto original decía que en el
> path transaccional "el caller recibe el error de `Tx` envuelto con
> `ErrEventEmitFailed`". Al implementar F5-6 sobre el contrato F5-5
> (ya entregado), el callback registrado vía `Tx.OnCommit` corre en
> `drainCtxHooks`, que **nunca propaga** errores al caller de
> `Client.Tx` — el commit ya retornó éxito y no queda canal de
> retorno. La reconciliación adoptada:
> - **Path no-transaccional** (`For[T].Create/Update/Delete`): el
>   `Publish` corre inline tras el statement y su error se devuelve
>   al caller del CRUD envuelto en `ErrEventEmitFailed`.
> - **Path transaccional** (`ForTx[T]` dentro de `Client.Tx`): el
>   `Publish` corre post-commit vía `Tx.OnCommit`; un fallo se
>   loguea con el evento estructurado `quark.event.emit_failure` y
>   **no se propaga** (el commit ya devolvió éxito; propagarlo
>   arriesgaría que el caller reintente y haga doble-write).
>
> Ambos paths registran `quark.event.emit_failure`. La implementación
> vive en `query_crud.go emitEvent()`.

Razón de la elección síncrona vs async fire-and-forget:

- Async fire-and-forget da **falsa impresión de durabilidad**: el
  caller cree que el evento se publicó y no es así.
- Outbox transaccional real (escribir el evento en la misma tx y un
  daemon lo despacha) es la solución correcta a "exactly-once con
  durabilidad" — pero es Fase 6, no Fase 5. Mientras tanto, sync +
  at-least-once + logging es la opción honesta.

`EventBus` se define como interfaz pública para que terceros conecten
NATS / Kafka / Redis Streams:

```go
type EventBus interface {
    Publish(ctx context.Context, event Event) error
}

type Event interface {
    Kind() string      // "created" | "updated" | "deleted"
    Table() string
    Payload() any      // model snapshot or diff
}
```

Implementaciones in-tree para Fase 5: `LoggerEventBus` (slog) y
`OTelEventBus` (span emit). Conectores externos viven en módulos
satélite (`github.com/jcsvwinston/quark-nats`, etc.).

### Listener side (LISTEN/NOTIFY PG)

`events.go:CreateListener` queda fuera de scope para Fase 5. Devolverá
`ErrDialectNotSupported` hasta Fase 6, donde se evaluará. La razón:
LISTEN/NOTIFY de PG requiere conexión dedicada fuera del pool y no
casa con el modelo de `*sql.DB`. Es un proyecto en sí mismo.

> **Resuelto post-v1.0 por [ADR-0019](0019-inbound-listen-notify-dedicated-conn.md):**
> el listener inbound de PostgreSQL se implementa sobre una `*sql.Conn`
> dedicada tomada del pool del `Client` (vía `conn.Raw` →
> `*pgx.Conn.WaitForNotification`). Sólo PostgreSQL; los demás dialectos
> siguen devolviendo `ErrDialectNotSupported`.

## Consecuencias

**Positivas:**

- Hooks pasan a tener semántica clara y predecible. El playbook
  `query-builder.md` puede documentar "Before aborta, After observa, OnCommit
  para side-effects".
- F5-4 (audit log) cae natural: registra vía `tx.OnCommit` con el
  snapshot del diff capturado en `Tracked.Save` (F1-1 ya existe).
- F5-1 (RLS real) puede emitir eventos `tenant.scoped.created` sin
  romper aislamiento — la emisión va por `OnCommit` con `tenant_id` ya
  en el payload.
- El `EventBus` se vuelve útil de verdad (hoy es placeholder PG-only).

**Negativas:**

- Cambio de semántica de `After*` es **breaking minor**. Código que
  asume "AfterCreate corre inline" cambia: ahora corre post-commit.
  La inmensa mayoría del código real no se ve afectado (los hooks
  hacen side-effects, no modifican la entidad en memoria del CRUD
  que los disparó). Documentado en `MIGRATION_v0.9.0.md`.
- Encolar `After*` en el `*Tx` requiere que el wrapper de tx implícita
  (Regla 1) pase el `*Tx` al motor de hooks. Refactor de `query_crud.go`
  no trivial pero contenido.
- Emisión síncrona en commit-phase añade latencia al `Tx.Commit`
  retornado: el tiempo total = commit + ΣPublish. Documentado; el
  caller puede usar `OnCommit` manualmente con `go fn()` si acepta el
  trade-off de fire-and-forget en su caso concreto.

## Alternativas descartadas

1. **Mantener `After*` inline + añadir `OnCommit` separado**. Confuso:
   dos APIs para lo mismo, los usuarios no saben cuál usar. Mejor
   reglas claras (Before → abort, After → post-commit observacional,
   OnCommit → side-effects arbitrarios).
2. **Async fire-and-forget para EventBus**. Rechazado por falsa
   durabilidad (apartado anterior).
3. **Outbox transaccional para EventBus en Fase 5**. Rechazado por
   alcance: requiere tabla de outbox + daemon + at-least-once con
   dedup en subscriber. Es Fase 6.
4. **Hooks reciben `*Tx` como argumento**. Considerado, pero rompe la
   superficie de `hooks.go` (todas las interfaces cambian). Alternativa
   más ligera: el hook puede acceder al tx vía `quark.TxFromContext(ctx)`
   si lo necesita. Implementado como helper opcional.
5. **`OnCommit` con prioridades / ordenes garantizados entre callbacks**.
   Rechazado: FIFO es suficiente y predecible. Las cadenas complejas
   se modelan a nivel de aplicación.

## Lo que esta decisión NO permite

- **NO** prometer exactly-once en EventBus. La doc pública debe decir
  "at-least-once, sin durabilidad transaccional". Outbox queda
  pendiente.
- **NO** asumir que un `After*` que falla revierte la operación.
- **NO** ejecutar `Before*` fuera de tx — siempre dentro (implícita o
  explícita). El reviewer rechaza PRs que lo violen.

## Cuándo reabrir

- Cuando un caso de uso real exija exactly-once con durabilidad
  (cobranza, fulfillment legal), abrir ADR sucesor que evalúe outbox
  transaccional in-tree.
- Si LISTEN/NOTIFY PG entra al scope, ADR sucesor que documente el
  modelo de conexión dedicada.
