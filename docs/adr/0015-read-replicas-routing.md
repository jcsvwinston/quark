---
id: 0015
title: Read replicas — execution-time read/write routing, opt-in, with sticky read-your-writes
status: accepted
date: 2026-05-24
implemented: F6-5 (skeleton); F6-6 (failover)
deciders: jcsvwinston
related: [0007, 0012, 0013]
supersedes: null
tags: [architecture, ha, scaling, phase-6]
---

# 0015 — Read replicas: read/write routing model

## Contexto

Hasta v0.12 un `Client` envuelve un único `*sql.DB` (el primary): todas las
lecturas y escrituras van a la misma base. Para cargas read-heavy, el pillar de
HA de Fase 6 (F6-5/F6-6) quiere repartir las **lecturas** entre una o varias
**réplicas de lectura**, dejando las escrituras en el primary.

El profiling de F6-9 (`docs/benchmarks/stress/`) refuerza la motivación: bajo
concurrencia el cuello de botella es el motor + el pool, no el mapping de Quark.
Repartir lecturas entre réplicas ataca exactamente ese límite — más conexiones
de lectura, en más motores.

Antes de escribir el código hay que fijar tres decisiones que condicionan todo:
**(1)** dónde se decide el routing, **(2)** el modelo de consistencia (las
réplicas replican de forma asíncrona → pueden ir atrasadas), **(3)** cómo
convive con las piezas que ya fijan el `Executor` de una query — transacciones
(`ForTx`, ADR-0013) y RLS nativa (ADR-0012, que exige que la lectura ocurra en
la misma conexión donde se puso la session var).

## Decisión

- **Opt-in vía `WithReplicas(dsns ...string)`.** Abre un `*sql.DB` por DSN de
  réplica al construir el `Client`, con las mismas `PoolOption`s que el primary.
  Sin la opción, el comportamiento es idéntico a hoy (un solo DB) — coste cero.

- **El routing se decide en tiempo de EJECUCIÓN, no de construcción.** `For[T]`
  no sabe todavía si la query será lectura o escritura (el mismo `Query[T]`
  puede hacer `.List()` o `.Create()`), así que `q.exec` se queda apuntando al
  primary en construcción. La elección se hace en los puntos de ejecución:
  - **Lecturas multi-fila** (`executeQuery`: `List`/`Iter`/eager-loading) → una
    réplica elegida por estrategia.
  - **Escrituras** (`executeExec`: INSERT/UPDATE/DELETE) → siempre el primary.
  - **`executeQueryRow` NO se enruta** (se queda en `q.exec`/primary). Es el
    primitivo de una-fila **compartido** entre lecturas (`First`/`Find`/`Count`)
    y el **camino de escritura** (`INSERT ... RETURNING`, `SCOPE_IDENTITY()` de
    MSSQL): enrutarlo mandaría escrituras a una réplica. El skeleton F6-5 enruta
    sólo `executeQuery`; separar las lecturas de una-fila del RETURNING para
    enrutar también `First`/`Find`/`Count` es **follow-up** (no bloquea el
    modelo; es trabajo mecánico de partir el primitivo). Hasta entonces esas
    lecturas van al primary — correcto, sólo no aprovechan las réplicas.

- **Estrategia de selección pluggable; el skeleton entrega round-robin.** El
  diseño contempla round-robin (default), random y least-conn; F6-5 implementa
  round-robin y deja las otras como extensión (un campo de estrategia en el
  Client, no una bifurcación de API).

- **Modelo de consistencia: eventual por defecto, read-your-writes vía
  `Sticky(ctx)`.** Las réplicas van potencialmente atrasadas, así que una
  lectura normal puede devolver datos viejos. `quark.Sticky(ctx)` devuelve un
  contexto que fuerza las lecturas subsiguientes al **primary** — el escape
  hatch para "acabo de escribir y necesito leerlo". Las lecturas dentro de un
  `Client.Tx` usan la conexión de la tx (primary) y por tanto **siempre** ven
  las escrituras de esa tx.

- **Exclusiones de routing (se quedan en el primary / la conexión actual):**
  1. Queries ligadas a una transacción (`q.exec` es `*sql.Tx`).
  2. RLS nativa (`q.exec` es el executor de ADR-0012; la policy se evalúa en la
     conexión que tiene la session var — enviar la lectura a otra conexión
     rompería el aislamiento).
  3. `Sticky(ctx)`.
  
  Mecánicamente esto cae solo: `readExec` sólo desvía cuando `q.exec` **es** el
  `*sql.DB` primary del Client; cualquier otro `Executor` (tx, RLS) pasa intacto.

- **Healthcheck + failover (F6-6, implementado).** Una lectura enrutada a una
  réplica que falla con un error de conexión transitorio (`isTransientConnErr`:
  `driver.ErrBadConn` / `sql.ErrConnDone` / error de red / códigos de
  conexión por dialecto / handle SQLite cerrado) **hace failover al primary**
  (reintento de la lectura allí) y la réplica se **saca de rotación** durante
  `replicaDownCooldown` (default 5s). `pickReplica` salta las réplicas en
  cooldown; si todas están caídas devuelve nil y la lectura va al primary.
  Recuperación **pasiva**: pasado el cooldown la réplica se reintenta (sin
  goroutine de health-check activo). Con esto `WithReplicas` deja de ser
  experimental: una réplica caída ya no rompe las lecturas.
  
  **No hay "failover de primary" multi-primary** en este modelo: hay un único
  primary (el destino del fallback). Promoción de una réplica a primary es otro
  modelo (replicación gestionada por el operador / el motor), fuera de alcance.

## Consecuencias

**Positivas:**
- Escalado de lecturas sin cambiar la API: `quark.For[T]` es idéntica con o sin
  réplicas; la única diferencia observable es a qué DB fue la lectura.
- El escape hatch (`Sticky`) y la regla "tx siempre primary" cubren los casos
  donde la consistencia importa, sin obligar a anotar cada query.
- Seam único y testeable (`readExec`); opt-in con coste cero si no hay réplicas.

**Negativas:**
- **Lecturas potencialmente stale**: sorpresa clásica de read-replicas. Mitigado
  por `Sticky` + documentación explícita; no se puede eliminar (es la naturaleza
  de la replicación asíncrona).
- **Ventana de staleness en failover**: tras sacar una réplica de rotación las
  lecturas van al primary (consistentes) hasta que el cooldown expira; si la
  réplica sigue caída al reintentarse, vuelve a hacer failover. La recuperación
  pasiva (sin health-check activo) significa que una réplica que vuelve no se
  reincorpora hasta el primer reintento tras el cooldown.
- Carga operativa: el usuario gestiona las réplicas y su replicación; Quark sólo
  enruta.

## Alternativas consideradas

1. **Routing en construcción (`For[T]`).** Rechazado: en construcción no se sabe
   si la query es lectura o escritura.
2. **API explícita por query (`.OnReplica()` / `.OnPrimary()`).** Rechazado como
   *default*: verboso y propenso a olvidos (olvidar `.OnPrimary()` tras un write
   da lecturas stale silenciosas). El auto-routing con `Sticky` como escape hatch
   es el inverso seguro. (Una anotación por-query explícita podría añadirse encima
   más adelante sin romper esto.)
3. **`ReplicaClient` separado.** Rechazado: bifurca la API y traslada al usuario
   la decisión de routing en cada call site; contradice el espíritu de ADR-0002
   (una sola API).

## Restricciones que esta decisión impone

- Cualquier nuevo punto que elija `Executor` para una lectura debe respetar las
  exclusiones (tx / RLS nativa / Sticky) — reusar `readExec`, no reimplementar.
- Las escrituras **nunca** van a una réplica.
- `Client.Close` debe cerrar también los pools de réplica.

## Cuándo reabrir

- Si se necesita replicación síncrona / quorum reads (consistencia fuerte en
  réplica) — es otro modelo.
- Si surge demanda de control de routing por-query explícito como primer-class.
- F6-6 (failover) extiende este ADR; no lo supersede.
