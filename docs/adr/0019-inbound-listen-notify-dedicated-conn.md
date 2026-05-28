---
id: 0019
title: Inbound LISTEN/NOTIFY (PostgreSQL) sobre conexión dedicada del pool, no un pool propio
status: accepted
date: 2026-05-28
implemented: ListenerFactory.CreateListener (postgres); cierra el item v1.1 "inbound LISTEN/NOTIFY"
deciders: jcsvwinston
related: [0013]
supersedes: null
tags: [events, listen-notify, postgres, post-v1.0, v1.1]
---

# 0019 — Inbound LISTEN/NOTIFY (PostgreSQL) sobre conexión dedicada

## Contexto

ADR-0013 §"Listener side" dejó la mitad **inbound** del par LISTEN/NOTIFY
fuera de scope para Fase 5: `ListenerFactory.CreateListener` devolvía
`ErrDialectNotSupported` en todos los dialectos. La razón documentada fue que
PostgreSQL LISTEN/NOTIFY exige una **conexión dedicada** sostenida fuera del
flujo normal del pool, y que eso "es un proyecto en sí mismo". El cierre de
v1.0 lo registró como known-limitation con un waiver explícito y lo difirió a
post-v1.0; este ADR es el sucesor que ADR-0013 §"Cuándo reabrir" pedía ("Si
LISTEN/NOTIFY PG entra al scope, ADR sucesor que documente el modelo de
conexión dedicada").

El lado **outbound** ya existe y no cambia: `Notify(ctx, provider, channel,
payload)` emite vía `pg_notify($1, $2)`. Lo que falta es el consumidor: poder
bloquearse esperando notificaciones de un canal.

Dos hechos restringen la elección:

1. **Quark habla con PostgreSQL vía `database/sql`** usando el adaptador
   `github.com/jackc/pgx/v5/stdlib` (driver registrado como `pgx`). No hay un
   `*pgxpool.Pool` ni un `*pgx.Conn` accesible directamente desde el `Client`;
   sólo un `*sql.DB`.
2. **LISTEN/NOTIFY necesita una conexión física estable.** `LISTEN foo`
   registra la suscripción **en la conexión concreta**; una notificación sólo
   llega a las conexiones que tienen ese `LISTEN` activo. El pool de
   `database/sql` rota conexiones libremente, así que no se puede "escuchar
   sobre el pool" — hay que fijar una conexión y sostenerla.

## Decisión

**Implementar el listener inbound sobre una `*sql.Conn` dedicada, tomada
prestada del pool del `Client` y sostenida durante toda la vida del listener.**
Sólo PostgreSQL; los demás dialectos siguen devolviendo
`ErrDialectNotSupported`.

- `ListenerFactory.CreateListener()` devuelve un `*pgListener` cuando el
  dialecto es `postgres`; `ErrDialectNotSupported` en cualquier otro. La firma
  pública del método **no cambia** (seguía siendo `() (EventListener, error)`).
- La conexión dedicada se adquiere **perezosamente** en el primer `Listen`
  (que ya recibe un `context.Context`), no en `CreateListener` — así la firma
  sin-ctx de `CreateListener` se mantiene y no se toma una conexión hasta que
  hace falta.
- Cada operación accede al `*pgx.Conn` subyacente vía
  `sql.Conn.Raw(func(driverConn any) error { … })`, casteando a
  `*stdlib.Conn` y llamando `.Conn()`. La misma `*sql.Conn` envuelve siempre la
  misma conexión física, así que los `LISTEN` persisten entre llamadas `Raw`
  sucesivas.
  - `Listen(ctx, ch)`: `pgxConn.Exec(ctx, "LISTEN "+ident)`.
  - `Unlisten(ctx, ch)`: `pgxConn.Exec(ctx, "UNLISTEN "+ident)`.
  - `Receive(ctx)`: `pgxConn.WaitForNotification(ctx)` → `EventPayload{Channel,
    Payload}`. Bloquea hasta que llega una notificación de cualquier canal
    suscrito o hasta que el `ctx` se cancela.
  - `Close()`: best-effort `UNLISTEN *` y luego `sql.Conn.Close()` (devuelve la
    conexión al pool). Idempotente.

**Validación del nombre de canal.** El comando `LISTEN`/`UNLISTEN` **no admite
parámetros bind** (a diferencia de `pg_notify`, que es una función). El canal
se concatena al SQL, así que pasa por dos capas: `client.guard.
ValidateIdentifier(channel)` (misma validación que usa `Notify`) y, además,
`pgx.Identifier{channel}.Sanitize()` para el quoting correcto.

**`UNLISTEN *` antes de devolver la conexión.** El `ResetSession` del driver
pgx/stdlib **no** limpia el estado `LISTEN` por defecto (es un no-op salvo que
se configure). Si se devolviera la conexión al pool con suscripciones vivas, un
futuro tomador heredaría notificaciones ajenas. Por eso `Close` ejecuta
`UNLISTEN *` best-effort antes de `sql.Conn.Close()`.

**Contrato de concurrencia: single-goroutine.** Todas las operaciones se
serializan con un mutex (la `*sql.Conn` no es concurrency-safe). `Receive`
bloquea sosteniendo el mutex, así que **no** se puede llamar `Listen`/`Close`
desde otra goroutine mientras un `Receive` está bloqueado. El patrón soportado:
registrar todos los canales con `Listen`, luego hacer loop de `Receive(ctx)` en
una sola goroutine; para parar, cancelar el `ctx` del `Receive` (devuelve el
error del contexto) y luego `Close`.

## Consecuencias

**Positivas:**
- Cierra la asimetría outbound/inbound que v1.0 documentó como known-limitation.
  `Notify` (emit) + listener (consume) son ya un par completo en PostgreSQL.
- No introduce un pool de conexiones paralelo ni una dependencia nueva: reutiliza
  el `*sql.DB` del `Client` y la dependencia pgx/v5 que ya estaba.
- Un `Client` que nunca crea un listener no paga nada (adquisición perezosa).

**Negativas:**
- **Consume una conexión del pool durante toda la vida del listener.** Un
  listener de larga duración resta una conexión a `MaxOpenConns`. Documentado:
  dimensiona el pool o usa un `Client`/`*sql.DB` aparte para listeners de larga
  vida.
- **Semántica fire-and-forget de PostgreSQL.** Si la conexión cae (red, failover,
  reinicio del servidor), las notificaciones emitidas mientras está caída **se
  pierden** — PostgreSQL LISTEN/NOTIFY no tiene buffer durable ni replay. El
  listener devuelve error en `Receive` y el caller debe reconectar (nuevo
  `CreateListener` + `Listen`) y reconciliar el estado por su cuenta. No es un
  sustituto de una cola durable.
- **Sólo PostgreSQL.** MySQL/MariaDB/SQLite/MSSQL/Oracle no tienen LISTEN/NOTIFY;
  siguen devolviendo `ErrDialectNotSupported`. No se emula con polling.
- Cancelar el `ctx` de `Receive` a mitad de espera puede dejar la conexión
  física inutilizable (pgx la marca para cierre tras una cancelación durante una
  lectura de red); es inofensivo porque `database/sql` descarta esa conexión al
  devolverla y `Close` tolera el fallo del `UNLISTEN *`.

## Alternativas consideradas

1. **Pool/conexión pgx nativo (`pgxpool` / `*pgx.Conn`) en paralelo al
   `*sql.DB`.** Daría acceso directo a `WaitForNotification` sin `conn.Raw`.
   **Rechazado**: duplica la configuración de conexión (DSN, TLS, timeouts) en
   dos clientes distintos, y obliga a que el `Client` conozca el driver concreto.
   El adaptador stdlib ya expone el `*pgx.Conn` subyacente vía `Raw`; no hace
   falta un segundo pool.
2. **`lib/pq` con `pq.Listener` (reconexión automática integrada).** Es la API
   clásica de listener en Go. **Rechazado**: añadiría una dependencia de driver
   nueva (`lib/pq`) sólo para esta feature, conviviendo con pgx — peor que
   reutilizar el driver que ya está. La reconexión la puede hacer el caller.
3. **Emular LISTEN/NOTIFY en otros motores con una tabla + polling.**
   **Rechazado**: cambia la semántica (latencia de polling, carga de DB) y
   vende como "LISTEN/NOTIFY" algo que no lo es. Mejor un `ErrDialectNotSupported`
   honesto.
4. **Reconexión automática dentro del listener.** **Rechazado para el alcance
   mínimo**: ocultar la caída de conexión re-suscribiendo en silencio puede
   enmascarar notificaciones perdidas durante la ventana de desconexión. Se
   prefiere devolver el error y dejar la política de reconexión al caller. Se
   puede reabrir si surge demanda.

## Lo que esta decisión NO permite

- **NO** prometer entrega durable ni replay: las notificaciones emitidas
  mientras el listener está desconectado se pierden (límite de PostgreSQL).
- **NO** usar el listener concurrentemente desde varias goroutines: una `*sql.
  Conn` sostiene un único `Receive` bloqueante a la vez.
- **NO** LISTEN/NOTIFY en motores distintos de PostgreSQL.

## Cuándo reabrir

- Si surge demanda de reconexión automática con re-suscripción, abrir ADR
  sucesor que defina la política (y cómo señalar el gap de notificaciones).
- Si se necesita entrega durable (replay tras desconexión), eso es una cola
  (outbox + broker), no LISTEN/NOTIFY — fuera del scope de este ADR.
