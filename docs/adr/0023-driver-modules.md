---
id: 0023
title: Los drivers salen a módulos propios; Quark deja de cargar sus tipos de error
status: accepted
date: 2026-09-01
implemented: v1.9 (drivers/postgres, drivers/mysql, drivers/sqlite, drivers/mssql, drivers/oracle)
deciders: jcsvwinston
related: [0019]
supersedes: null
tags: [architecture, packaging, drivers, dx]
---

# 0023 — Los drivers salen a módulos propios

## Contexto

Quark nunca registró un driver: abre el `*sql.DB` que le entregan, y la guía de
instalación siempre pidió el `import _` del motor. Lo que **sí** cargaba eran
los **tipos de error** de cinco drivers, porque clasificar un fallo es
reconocerlo: un deadlock de MySQL es un `*mysql.MySQLError` con número 1213, y
nombrar ese tipo importa MySQL entero.

El resultado era que cada binario de Quark arrastraba los cinco motores
hablase con el que hablase. Medido sobre un hola-mundo que sólo enlaza el
paquete raíz:

| | Binario | Paquetes | Módulos |
| --- | --- | --- | --- |
| Antes | 24 MB | 304 | 171 |
| Después | **6 MB** | **159** | **129** |

Dos bloques, no uno. Los tipos de error valían ~66 paquetes; el otro era el
**listener de LISTEN/NOTIFY** ([ADR-0019](0019-inbound-listen-notify-dedicated-conn.md)),
que alcanza la conexión pgx por debajo de `database/sql` y por eso importaba
pgx en la biblioteca.

## Decisión

Cinco módulos hermanos, uno por motor:

```
drivers/postgres   drivers/mysql   drivers/sqlite   drivers/mssql   drivers/oracle
```

Cada uno registra el driver de `database/sql` **y** lo que Quark necesita saber
de sus errores. El contrato vive en `quarkdriver`, un paquete hoja.

### Los tres predicados van juntos

`quarkdriver.Classifier` pide los tres —violación de unicidad, deadlock,
conexión transitoria— en una sola estructura, precisamente para que entregar
dos de tres no sea expresable.

La razón es que **ninguno falla cuando no reconoce un error: contesta `false`**,
y Quark actúa sobre esa respuesta. Un `false` del predicado de unicidad
convierte un 409 en un 500. Un `false` del de deadlock es una transacción que
deja de reintentarse en silencio, bajo carga, meses después. Un `false` del de
conexión sigue mandando lecturas a una réplica caída. Ninguno de los tres
produce un error que alguien vea.

### PostgreSQL no registra clasificador, a propósito

Cualquier driver de PostgreSQL expone el SQLSTATE por el método
`SQLState() string`, así que Quark lo lee sin nombrar ningún tipo — y por eso
cubre igual `lib/pq`, `pq` y `pgx`, que es lo que `dialect.go` acepta. Añadir
un clasificador tipado a uno solo dejaría de cubrir los otros **sin fallar**.
Hay un test que lo fija, para que nadie «arregle» la omisión aparente.

### El listener se muda con su driver

`pgListener` pasa a `drivers/postgres`. `EventPayload` y `EventListener` se
convierten en **alias** de los tipos de `quarkdriver`, igual que hizo Nucleus
con sus contratos de plugin: quien ya escribía `quark.EventListener` no cambia
una línea. `CreateListener` sin el módulo devuelve un error que nombra el
import, en vez de un listener nulo que revienta tres líneas después.

### Un solo sitio con los predicados

`internal/driverclassify` guarda los cuerpos. Tres consumidores legítimos los
necesitan y no podrían compartirlos de otro modo: los módulos, el **CLI**
—que enlaza todos los motores, porque es una herramienta que se instala una
vez y se apunta a la base de datos que haya— y el binario de tests. Escritos
tres veces, derivarían; y un clasificador que deriva no falla, contesta
`false`.

El CLI no puede importar los módulos: esos importan Quark, y el CLI vive en el
módulo de Quark, así que el requisito sería circular. Enlaza los drivers
directamente y registra los mismos predicados, desde el mismo sitio.

## Consecuencias

- **Una aplicación añade un import.** El que la guía de instalación ya pedía
  para el driver, ahora apuntando al módulo que además trae la clasificación.
- **Ruptura de fuente sin ventana de deprecación**, en una minor: quien no
  añada el módulo no compila peor — compila igual y clasifica peor, que es
  justo lo que no se puede permitir. Por eso el módulo, y no un aviso.
- **`quarkdriver/drivertest`** verifica lo que hace seguro consultar
  clasificadores en cadena, sobre todo que ninguno reclame errores ajenos.
- **mattn/go-sqlite3 deja de clasificarse en el árbol.** Estaba tras un build
  tag para que `CGO_ENABLED=0` siguiera compilando. Reporta los mismos códigos
  numéricos que modernc, así que quien lo use registra su clasificador con las
  mismas tres líneas que tiene `drivers/sqlite`.

## Cuándo reabrir

Si aparece un motor cuyos errores no se distingan por código —o un driver que
no exponga ni tipo ni número—, el contrato de tres predicados se queda corto y
habría que decidir si Quark admite clasificación por mensaje, con todo lo que
eso implica en servidores que traducen.
