---
id: 0025
title: El inquilino de una transacción lo fija quien la abre; una consulta dentro que nombre a otro se rechaza
status: accepted
date: 2026-09-20
implemented: v1.14.x (QK-26, arco A8 S1)
deciders: jcsvwinston
related: [0007, 0012]
supersedes: null
tags: [multi-tenancy, transactions, security]
---

# 0025 — La transacción fija el inquilino

## Contexto

El banco del arco A8 (`internal/enterprisebench`, control `RLS-13`) midió que
el confinamiento por inquilino **no sobrevivía a una transacción**: `For[T]`
aplicaba el schema o el predicado cuando el proveedor era un `TenantRouter`, y
`ForTx[T]` construía su consulta desnuda, porque el `*Tx` que `router.Tx`
entregaba no sabía de ningún router en toda estrategia que no fuera
`RowLevelSecurityNative`. Dentro de una transacción una consulta leía las filas
de todos los inquilinos y escribía en el schema por defecto. Es QK-26, el P1
del arco.

El primer corte del arreglo estampó el router en el `*Tx` y llamó a la misma
función de confinamiento desde los dos constructores. La revisión adversarial
(seis lentes, tres escépticos por hallazgo) dejó seis defectos, y uno de ellos
era una **decisión sin tomar**: el inquilino de una consulta dentro de la
transacción se re-resolvía del contexto de LA CONSULTA, no del que abrió la
transacción, y las estrategias no se ponían de acuerdo sobre cuál manda:

- Bajo `RowLevelSecurityNative` el motor ya filtra por el inquilino que
  `set_config` fijó en ESA conexión al abrir la transacción. El contexto de la
  consulta no puede cambiarlo; si nombra a otro, la consulta miente sobre lo
  que va a leer.
- Bajo `DatabasePerTenant` el pool ya se eligió para un inquilino. Re-resolver
  del contexto de la consulta hizo fallar un `ForTx` con un contexto sin
  inquilino donde antes bastaba el pool: una regresión.
- Bajo `SchemaPerTenant` y `RowLevelSecurityClient` el estampado va por
  sentencia y podría, técnicamente, obedecer al contexto de cada consulta —
  con lo que una transacción "de acme" contendría sentencias de globex.

## Decisión

**La transacción fija el inquilino.** Quien abre la transacción lo nombra, y
todo lo que se construye dentro con `ForTx` se confina a ÉSE.

1. El `*Tx` lleva el router que lo confina y el `tenantID` para el que se
   abrió. Los pone **una sola función**, `TenantRouter.confineTx`, llamada
   desde `router.Tx` y desde `Client.BeginTx` cuando el cliente es el
   `BaseClient` de un router. Bajo Native es ahí donde corre `set_config`.
2. Dentro de una transacción, `applyTenantConfinement` toma el inquilino de
   la transacción (`tenantFor`). Un contexto de consulta que **no resuelve
   inquilino lo hereda** —`ForTx[T](context.Background(), tx)` es válido—, y
   uno que **resuelve otro distinto** falla con `ErrTenantMismatch` antes de
   ejecutar nada. Nunca se obedece en silencio al contexto de la consulta.
3. `router.GetClient(ctx)` + `client.Tx(ctx, …)` sobre el `BaseClient`
   compartido es **la misma puerta** que `router.Tx`: `NewTenantRouter`
   estampa el router en el `BaseClient` de las tres estrategias de pool
   compartido, y `BeginTx` confina la transacción si el contexto lleva
   inquilino. Sin inquilino en el contexto, la transacción queda como siempre
   fue —sobre el pool compartido, sin confinar—, que es como corren las
   migraciones y el aprovisionamiento; un inquilino no vacío pero inválido es
   error, nunca una transacción desnuda.
4. Bajo `DatabasePerTenant` no se estampa nada en la sentencia: el pool ES el
   confinamiento. `router.Tx` sí registra router e inquilino en el `*Tx`,
   para que la regla 2 se aplique igual.
5. Una consulta que **falló al construirse** (`q.err`) no ejecuta nada ni
   dispara hooks: los mutadores lo comprueban a la entrada, las copias
   internas de `BaseQuery` arrastran `err`, y `queryRowOn` acuña el error en
   el `*sql.Row` —el comentario que afirmaba que "aflora solo en `Scan`"
   describía un mecanismo que no existía.
6. Toda tabla que una consulta toca —la suya, la de una relación precargada,
   la join table— pasa por `qualifiedTable`, que antepone el schema del
   inquilino. `Preload` leía las relaciones del schema por defecto.

## Consecuencias

- `ErrTenantMismatch` es API pública nueva (aditiva). Quien hacía
  `router.Tx(ctxA, …)` con `ForTx(ctxB, …)` dentro obtiene ahora un error
  donde antes obtenía una lectura sin confinar; es el defecto, no una
  ruptura.
- `client.Tx` sobre un `BaseClient` estampado, con inquilino en el contexto,
  emite `set_config` bajo Native. `UpdateBatch`, que abre su propia
  transacción con `q.client.Tx`, pasa a estar filtrado por el motor donde
  antes la política filtraba por nada.
- Si dos routers comparten un `BaseClient`, el último estampado gana —la
  misma salvedad que ya tenía `nativeTenantResolver`. Las estrategias son
  excluyentes por router, así que no es un caso soportado.
- Los tests de regresión viven en `tenant_tx_confinement_test.go`, uno por
  hallazgo de la revisión y **cada uno comprobado revirtiendo su arreglo**.

## Alternativas descartadas

- **Obedecer al contexto de la consulta** (lo que hacía el primer corte).
  Coherente con `For`, pero bajo Native es una mentira —el motor filtra por
  otro— y bajo `DatabasePerTenant` una regresión.
- **Hacer que `client.Tx` sin inquilino falle** cuando el cliente está
  estampado. Rompería migraciones y aprovisionamiento, que corren sobre el
  pool compartido a propósito, y cambiaría el comportamiento de una función
  publicada sin un major (QADR-0010).
- **No estampar el `BaseClient`** y documentar que sólo `router.Tx` confina.
  Deja la fuga abierta a una llamada de distancia, y el documento no la
  cierra.

## Cuándo reabrir

Si aparece una estrategia en la que el inquilino pueda cambiar legítimamente
dentro de una transacción (un `set_config` por sentencia bajo Native, por
ejemplo), esta decisión se sustituye con un ADR sucesor: la regla 2 sería
entonces demasiado estricta y habría que decidir qué la reemplaza.
