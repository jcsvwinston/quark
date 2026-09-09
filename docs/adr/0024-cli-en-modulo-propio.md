---
id: 0024
title: El CLI a su propio módulo — propuesta, con el coste de registro y la ruptura del `go install` medidos
status: proposed
date: 2026-09-09
implemented: null
deciders: jcsvwinston
related: [0023]
supersedes: null
tags: [architecture, packaging, cli, release, dx]
---

# 0024 — El CLI a su propio módulo

## Contexto

[ADR-0023](0023-driver-modules.md) sacó los cinco motores del grafo de
**imports** de la biblioteca. Lo que sigue dentro es el grafo de **módulos**:
el `go.mod` raíz requiere seis módulos de driver —uno por motor, y dos para
SQLite— y cinco de `testcontainers-go` que ninguna aplicación que importe Quark
enlaza jamás.
Están ahí porque comparten módulo con la biblioteca dos cosas que sí los
necesitan: `cmd/quark` —una herramienta que se apunta a la base de datos que
haya, así que enlaza todos los motores— y las suites por motor, que levantan
bases de datos reales en contenedores.

El hueco está medido, con los comandos al lado de cada cifra, en
[`docs/dependency-surface.md`](../dependency-surface.md). Lo que hay que saber
para decidir:

| | consumidor de `quark` v1.12.0 |
| --- | ---: |
| módulos en la build list (`go list -m all`, sin contarse a sí mismo) | 128 |
| módulos que el binario enlaza (`go list -deps`) | 11 |
| módulos que la build list carga y el binario nunca enlaza | **117** (91%) |
| bytes de zip de esos 117 | 142,8 MiB de 168,4 MiB (85%) |
| `go mod download all` sobre una caché vacía | ~911 MiB |
| avisos de `govulncheck` que vengan de uno de esos 117 | **0, hoy** |

Y la cifra que ordena las demás: **el binario no cambia**. El consumidor de
`v1.12.0` y el de un árbol partido enlazan los mismos 159 paquetes de los
mismos 12 módulos y producen un binario del mismo tamaño —7.204.994 bytes en
la medición—. Partir el módulo no quita código de ningún programa; quita
entradas de un grafo. Ese grafo es lo que leen un SBOM, un panel de
dependencias, `go mod download all` y cualquier escáner que mire
`go list -m all` en vez del grafo de llamadas.

El aviso importa aunque hoy sea cero: son 117 oportunidades de que aparezca
uno, sobre una superficie que ningún binario usa, y cada equipo que importe
Quark tendría que establecer a mano lo que aquella página establece una vez.
Además, la versión que Quark requiere es el **suelo** de la build list de todos:
subir `modernc.org/sqlite` aquí para contestar a un aviso lo sube en
aplicaciones que nunca lo llaman.

### Ninguno de los dos movimientos sirve por separado

Medido igual, sobre el árbol de `v1.12.0`, mirando la build list del mismo
consumidor:

| árbol | build list | requires de driver | requires de testcontainers |
| --- | ---: | ---: | ---: |
| tal como se publica | 128 | 6 | 5 |
| sin `cmd/` ni `examples/superapp` | 106 | 6 | 5 |
| sin los ficheros `*_test.go` | 77 | 5 | 0 |
| sin ninguna de las dos cosas | **28** | 0 | 0 |

Sacar el CLI y dejar las suites donde están no quita **ni un solo driver** del
`go.mod` raíz: los ficheros de test los siguen requiriendo, e
`internal/driverclassify` —que guarda los cuerpos de los predicados que el CLI
y el binario de tests comparten (ADR-0023)— los sigue importando. Los 22
módulos que se van son la familia de `cobra`/`viper`/`tablewriter`/`x/tools`,
que es real pero no es de lo que trata este ADR.

## Decisión propuesta

Mover `cmd/quark` a su propio módulo, `github.com/jcsvwinston/quark/cmd/quark`,
**y en el mismo movimiento** sacar del módulo raíz lo que arrastra los mismos
requisitos: `examples/superapp` e `internal/driverclassify` acompañan al CLI, y
las suites por motor pasan a un módulo de harness. Cualquier mitad sola deja la
tabla de arriba casi igual.

Una nota que el ADR-0023 dejó pendiente y que este movimiento cambia: el CLI no
podía importar los módulos `drivers/*` porque esos importan Quark y el CLI vivía
en el módulo de Quark —el requisito sería circular—. Con el CLI en su propio
módulo la circularidad desaparece: puede requerir los cinco `drivers/*` como
cualquier aplicación. Un módulo anidado además **sí** puede importar los
paquetes `internal/` del módulo padre (la regla de Go es de prefijo de ruta, no
de módulo; comprobado con dos módulos de juguete), así que la ruta de escape
sigue existiendo si conviene compartir los cuerpos en vez de los módulos.

## Coste en la maquinaria de release

Es la razón por la que esto es una propuesta y no un cambio. Un módulo de Go
nuevo hay que darlo de alta en **cuatro** sitios, y ninguno de los cuatro falla
ruidosamente si se olvida:

1. **`release-please-config.json`**, en `packages`, con `component: cmd/quark`,
   `include-component-in-tag: true` y `tag-separator: "/"`, como los cinco
   drivers; más su línea en `.release-please-manifest.json`. Sin la entrada,
   release-please no corta tag nunca: nada se rompe, el módulo simplemente no
   tiene versión. Este proyecto ya se comió esa forma exacta de fallo en un
   repo hermano —módulos en `main` sin entrada de release-please, existiendo
   sin que nada que enumere módulos lo supiera—.
2. **`versions.yaml` del paraguas**, bloque `quark_modules`. El manifest-guard
   **descubre** los módulos del árbol (cualquier `go.mod` fuera de
   `examples/`, `website/`, `benchmarks/`, `bugbash/` e `internal/`), así que
   aquí sí hay ruido: un `cmd/quark/go.mod` sin entrada rompe la certificación
   del set. Con una trampa de nombre: la clave del manifiesto es el último
   segmento de la ruta, o sea `quark`, que junto al `modules.quark` del pilar
   se lee mal. Conviene resolverlo antes, no en el tren.
3. La regla de ancestros del propio guard: el tag del módulo tiene que ser
   ancestro del pin de la raíz y la raíz no puede llevar código del módulo que
   el tag no cubra. El bump del CLI viaja **dentro** de la release de la raíz,
   no detrás.
4. **`.github/dependabot.yml`**, que ya declara un `go.mod` por directorio y
   cuyo lint falla si aparece uno sin entrada.

Y una quinta, distinta de las anteriores porque no es de registro sino de
build: `.goreleaser.yaml` construye `main: ./cmd/quark` desde la raíz del repo
y estampa `-X github.com/jcsvwinston/quark/cmd/quark/commands.version=v{{ .Version }}`
con la versión del tag raíz. Con el CLI en un módulo anidado hay que construir
dentro de él, y esa versión pasa a venir de otra serie de tags que la del
binario que se publica.

## Qué se rompe para quien hoy hace `go install`

`go install github.com/jcsvwinston/quark/cmd/quark@latest` sigue funcionando:
la ruta de import no cambia. Lo que cambia es contra qué se resuelve.

- `@latest` pasa a resolver el módulo `.../cmd/quark`, cuyos tags son
  `cmd/quark/vX.Y.Z` y **cuya serie de versiones empieza de cero**. La versión
  del CLI deja de ser la de la biblioteca: quien hoy instala «Quark v1.12.0»
  mañana instala un `v0.1.0` que es el mismo programa.
- Un selector que nombre una versión de la raíz deja de servir para las
  releases posteriores al corte: el directorio de un módulo anidado queda
  **fuera** del módulo padre, así que un tag raíz futuro ya no contiene ese
  paquete y `go install .../cmd/quark@v1.13.0` falla. Los tags anteriores al
  corte siguen sirviendo, lo que hace el fallo intermitente de leer: funciona
  con las versiones viejas y no con las nuevas.
- Todo lo que dice hoy una versión del CLI —lo que imprime `quark version`, el
  nombre de los archivos de release, la guía de instalación y las notas de
  verificación de firmas— habla de la serie de la raíz.

## Alternativas

- **No hacer nada.** Defendible mientras el número de avisos sea cero: el coste
  de hoy es ruido de escáner y unos 730 MiB de descarga en el camino de
  `go mod download all`, no un binario peor. Se paga cada vez que un consumidor
  audita su grafo.
- **Sacar sólo el harness de tests.** Quita los cinco módulos de contenedores y
  uno de los seis drivers (la build list baja a 77), sin tocar el `go install`
  de nadie. Es la mitad barata, y deja los cinco motores en el `go.mod` que
  ve un SBOM.
- **Sacar sólo el CLI.** La mitad cara: rompe la serie de versiones del binario
  publicado y no quita ni un driver. No hay razón para elegir ésta.

## Recomendación

Hacerlo, con dos condiciones. La primera es que se haga **entero** —CLI,
`examples/superapp`, `internal/driverclassify` y las suites por motor—, porque
las cifras dicen que media reforma no mueve el grafo. La segunda es que las
cuatro altas de registro y la entrada de `versions.yaml` viajen en el **mismo
PR** que el `go.mod` nuevo, y que el primer tag del módulo se corte dentro de la
release de la raíz, que es lo único que la regla de ancestros del manifest-guard
acepta.

Si esa condición no se puede cumplir en el tren que toque, la alternativa
correcta es la barata: sacar el harness de tests, que baja la build list de 128
a 77 sin cambiar la ruta ni la versión de nada que alguien haya instalado.

Lo que no recomienda este ADR es venderlo como una mejora del binario. No lo
es: el binario no cambia. Es higiene del grafo, y su beneficio se cobra en el
escáner de otra persona.

## Cuándo reabrir

Si aparece un aviso real sobre uno de los 117 módulos que ningún consumidor
enlaza, la urgencia cambia de orden: deja de ser higiene y pasa a ser una
respuesta que hoy obliga a subir un suelo para todos. Y si la serie de
versiones del CLI se vuelve un problema mayor que el grafo —porque la
instalación es lo primero que hace un lector—, la alternativa barata pasa a ser
la decisión, no el plan B.
