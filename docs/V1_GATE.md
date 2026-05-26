# Gate v1.0 — qué falta para taggear honesto

> **Fecha:** 2026-05-25
> **Estado actual:** `v0.13.0` taggeada; F6-5 / F6-6 / F6-7 / F6-9 entregados.
> **Progreso §A:** **3/5 cerrados** (Items 3, 4 vía *Salida B*; Item 2
> alcance mínimo, 2026-05-25). Abiertos: **Item 1** (Oracle en CI — **Salida A
> elegida (Full), programa multi-sesión EN PROGRESO**; diagnóstico local
> 187/24) y **Item 5** (`RELEASE_NOTES_v1.0.0.md` — DRAFT, se finaliza al
> cerrar Item 1).
> **Origen:** [ADR-0017](adr/0017-codegen-type-safety-not-perf-gate.md) §3 retira el gate
> ≥3× p99 de ADR-0002 y delega el nuevo gate a *"el checklist honesto de
> `docs/ANALISIS_MADUREZ.md` §3 (cobertura cross-engine, gaps estructurales)"*.
> **Este documento es ese checklist.** Vive aquí porque ANALISIS_MADUREZ es
> análisis fechado, no backlog vivo; el gate sí lo es.

## Por qué existe este documento

ADR-0002 fijó un gate de performance (≥3× p99) que ADR-0017 retiró con datos:
el cuello no es reflect sino allocs arquitectónicos + driver. Retirar un gate
**no equivale a cumplirlo**. El nuevo gate sustituye "medida cuantitativa
inalcanzable" por "checklist cualitativo verificable" — pero sólo es honesto
si los items están listados, son comprobables, y v1.0 NO se taggea hasta
cerrarlos todos (o aceptarlos explícitamente en `RELEASE_NOTES_v1.0.0.md`
como Known limitations).

**Antes de invocar `/release v1.0.0`, los 5 items de §A deben estar
cerrados o waivers documentados en §B.** Si algún punto cae en "decisión
del mantenedor", debe haber un commit que lo documente — no basta con
"lo pensé y está bien".

---

## §A · Items bloqueantes (cierra antes de v1.0)

### Item 1 — Oracle en CI · 🚧 Salida A elegida (Full), EN PROGRESO

> **Decisión del mantenedor (2026-05-25): Salida A — Oracle en CI bloqueante.**
> Programa multi-sesión. Se ejecuta como una secuencia de PRs enfocados (abajo).
>
> **Diagnóstico local (2026-05-25), `gvenzl/oracle-free:23-slim` + `go-ora`
> (driver puro Go, sin Instant Client):** `TestSuiteOracle` (SharedSuite)
> corre **187 PASS / 24 FAIL**. **La imagen NO es el bloqueante** — el
> contenedor arranca en ~30 s en local. El bloqueante real es **completitud
> de dialecto**. Reparto de los 24 fallos:
>
> | Causa | Fallos | Tipo | Task |
> | --- | --- | --- | --- |
> | Schema introspection Oracle (F3-2) sin implementar | 5 | Feature grande | #30 |
> | Lock de migración distribuido Oracle sin implementar | 4 | Feature | #31 |
> | ~~JSON path como literal (ORA-40454: path not a literal)~~ ✅ | 3 | Fix de dialecto (Oracle-only) | #28 |
> | ~~`''` → NULL al escanear a `string`~~ ✅ | 2 | Fix de scan | #27 |
> | `TEXT` no es tipo Oracle (ORA-00902) | 1 | Parte de F3-2 (col.Type ya es dialect-native vía catálogo) | #30 |
> | Resto (CTE/UpdateZeroValues/ORA-00942/ORA-00001 rerun) | ~9 | Triage dialecto-vs-test | #29 |
>
> **Hallazgo clave:** el cluster `PlanMigration` (incl. el `TEXT`) **no son
> fixes sueltos** — dependen de la introspección F3-2 (el ejecutor trata
> `col.Type` como tipo nativo del catálogo, que Oracle no produce todavía).
>
> **Orden de PRs:** (a) fixes de dialecto contenidos y verificables en local
> (JSON-path-literal #28 + `''`→NULL #27); (b) introspección F3-2 Oracle #30
> (PR grande, incluye el vocabulario de tipos / `TEXT`→`CLOB`/`VARCHAR2`);
> (c) lock distribuido #31; (d) triage del resto #29; (e) **flip final**:
> añadir Oracle a la matriz de CI bloqueante #32 sólo cuando el SharedSuite
> esté **211/211**. Cada PR con `code-reviewer` + sin regresión en los otros
> 5 motores (SQLite local + 4 en CI; Oracle local vía contenedor).
>
> **Progreso (2026-05-26) — PR (a) entregado:** SharedSuite Oracle **187/24
> → 194/17** (medido en local, contenedor `gvenzl/oracle-free:23-slim`).
> #28 (Oracle `JSON_VALUE` con path inline como literal, injection-safe vía
> `ValidateJSONPath`) y #27 (NULL→`""` al escanear a `string` no-puntero, vía
> `emptyStringScanner`) cerrados; el fix de scan además resolvió en cascada
> 2 subtests de DirtyTracking que abortaban por el error de scan. Sin
> regresión en PG/MySQL/MariaDB/MSSQL (4 motores CI verdes vía
> testcontainers) ni SQLite.
>
> **Progreso (2026-05-26) — PR (d) entregado (#29 triage):** SharedSuite
> Oracle **194/17 → 199/12**. Triage de los 3 fallos test-vs-dialecto: los 3
> eran del lado del test (Oracle uppercasea identifiers, los asserts buscaban
> columnas en minúscula; el SQL emitido era correcto, incluido el threading de
> args del CTE). Fix test-only: comparar contra el SQL en minúscula en
> `cte_test.go` (`CTEArgsAreThreadedBeforeWHERE`) y `dirty_track_test.go`
> (`WritesZeroValuesWhenChanged`, `SnapshotRefreshesAfterSave`). Sin cambio de
> producción. Verificado en los 6 motores. **Restan 9 fallos-hoja**, los dos
> bloques grandes: PlanMigration ×6 (#30 / F3-2 introspección) y MigrationLock
> ×3 (#31 lock distribuido). **Siguiente PR sugerido:** (b) introspección
> F3-2 #30 (desbloquea PlanMigration) o (c) lock distribuido #31.
>
> **Progreso (2026-05-26) — PR (b) entregado (#30 / F3-2 Oracle):**
> SharedSuite Oracle **199/12 → 211/5**. Implementada
> `OracleDialect.IntrospectSchema` (data dictionary `USER_*`: tablas,
> columnas, índices no-PK, FKs, checks; identifiers en minúscula, NOT-NULL
> system checks filtrados, `SEARCH_CONDITION_VC` para predicados). Cerrados
> el cluster PlanMigration ×6 **y** el contrato `SchemaIntrospection` ×5 (que
> se activó al implementar el introspector). Tres piezas: introspección +
> normalización de tipos en el diff (identity Oracle: bare `NUMBER` ≡
> `NUMBER(19)` y secuencia identity tratada como autoincrement) + interfaz
> opcional `ColumnTypeMapper` (`TEXT`→`CLOB`, evita `ORA-00902`). Sin
> regresión en los otros 5 motores. **Resta sólo MigrationLock ×3 (#31 lock
> distribuido Oracle)** para 216/0, luego el flip de CI #32. **Siguiente PR:**
> (c) lock distribuido #31 — el último bloque del Item 1.
>
> **Progreso (2026-05-26) — PR (c) entregado (#31 lock distribuido Oracle):**
> SharedSuite Oracle **211/5 → 216/0** ✅. `OracleDialect.AcquireMigrationLock`
> vía `DBMS_LOCK` (session-scoped, `release_on_commit => FALSE` para sobrevivir
> a los commits implícitos del DDL; ADR-0018). Se eligió `DBMS_LOCK` sobre
> lock-table `FOR UPDATE` porque la interfaz `DBConn` del locker es
> session-scoped y no expone transacciones. Requiere `GRANT EXECUTE ON
> DBMS_LOCK` (lo aplica el contenedor de test vía SYSDBA; documentado en
> `migrations.mdx`). Sin regresión en los otros 5 motores. **El SharedSuite
> Oracle está en verde total (216/0).** Sólo queda el **flip de CI #32**:
> añadir Oracle a la matriz bloqueante de `.github/workflows/ci.yml` (+ el
> grant DBMS_LOCK en el setup del job) → cierra §A Item 1 y el gate.

**Por qué bloqueante:** Quark se posiciona como *"el ORM con Oracle real"*
(ver `comparison.mdx` y la justificación competitiva del análisis de
madurez). Un v1.0 que se vende por Oracle **sin** validar Oracle en CI es
contradictorio. La promesa pública supera a la cobertura real.

**Estado hoy:**

- Tests Oracle existen (`oracle_suite_test.go`); driver `go-ora` (puro Go).
- ~~Oracle queda fuera de CI mientras dure el testcontainers image issue~~
  **corregido el diagnóstico:** la imagen arranca bien; lo que falta es
  completitud de dialecto (187/24 en local).
- Validación: manual por release con DSN env var (mientras dure el programa A).

**Cómo cerrar (elige una salida):**

1. **Salida A — cerrar el image issue de testcontainers.** Investigar el
   bloqueo actual (probablemente la imagen `gvenzl/oracle-free` o
   `gvenzl/oracle-xe` no arranca en GitHub Actions por límites de
   memoria/CPU del runner free). Si requiere runner self-hosted, abrir
   issue de coste antes de comprometer. **Done:** Oracle pasa en la
   job-matrix de CI bloqueante, igual que PG/MySQL/MariaDB/MSSQL.
2. **Salida B — degradar el posicionamiento públicamente.** Quitar
   "Oracle real" de la fila top de `comparison.mdx` y reescribirla como
   "Oracle: validación manual del mantenedor; no en CI bloqueante hasta
   v1.x". Coste competitivo real pero honesto.
3. **Salida C — job programado.** Workflow `.github/workflows/oracle-nightly.yml`
   que corre la suite Oracle una vez al día (no bloqueante de PR pero
   visible). Punto medio entre A y B. **Done:** el job existe, ha corrido
   verde 7 días seguidos, y el badge se muestra en README.

**Comando de verificación:**

```bash
# Oracle en CI bloqueante (Salida A)
grep -A5 strategy .github/workflows/ci.yml | grep -i oracle
# debe devolver una entrada en la matriz; ahora mismo no la hay
```

**Decisión tomada (2026-05-25): Salida A (Full).** Programa multi-sesión en
curso (ver banner 🚧 arriba). Coste revisado a la luz del diagnóstico local:
introspección F3-2 + lock distribuido son las piezas grandes; **bastante más
que las "1-2 sesiones" estimadas** antes de medir. La salida no se cierra
hasta SharedSuite 211/211 + Oracle en la matriz de CI.

---

### ~~Item 2 — F6-7 (sharding) follow-ups~~ ✅ Cerrado (alcance mínimo)

> **Cerrado 2026-05-25 — alcance mínimo (recomendado).** Ejemplo runnable
> `examples/sharding/main.go` (dos shards **SQLite** self-contained — más
> runnable que PG/testcontainers y el sharding es engine-agnostic; el doc
> nota cómo cambiar a PG/MySQL) + `website/docs/advanced/sharding.mdx`
> ampliado con el puntero al ejemplo + `examples/README.md`. **Verificado:
> compila y corre** (4 cuentas repartidas 2/2 entre shards, rechazo sin
> shard key). **Diferidos a v1.1** (declarados en
> `RELEASE_NOTES_v1.0.0.md` §Known limitations): scatter-gather y
> `shard-key-from-entity`. El routing explícito por shard key ya cubre el
> caso principal.

**Por qué bloqueante:** `ShardRouter` está mergeado en `main`
(commit `039f7ef9`, PR #115) pero el roadmap reconoce follow-ups vivos:
*"Sharding `ShardRouter`, F6-7 — delivered: routes per query by shard
key. Follow-ups (scatter-gather, shard-key-from-entity, runnable PG
example) pending."* Si v1.0 incluye sharding, debe estar completo; si
los follow-ups son post-v1.0, el doc tiene que desdoblarlo en *"sharding
básico ✅, sharding avanzado v1.1"*.

**Estado hoy:**

- Routing por shard key explícito: ✅ entregado.
- **Ejemplo runnable en `examples/sharding/`:** ✅ entregado (SQLite
  self-contained; `go run ./examples/sharding/main.go`).
- **Scatter-gather (fan-out de reads cross-shard):** ⏳ diferido a v1.1.
- **`shard-key-from-entity` (derivar key del modelo):** ⏳ diferido a v1.1.

**Cómo cerrar:**

1. Decidir scope para v1.0:
   - **Mínimo (recomendado):** ejemplo PG runnable + doc actualizada;
     scatter-gather y shard-key-from-entity diferidos a v1.1 con
     mención explícita en `RELEASE_NOTES_v1.0.0.md`. Es defendible:
     el routing explícito ya cubre el 80% de los casos.
   - **Completo:** los tres follow-ups entregados antes de v1.0.
     Coste estimado: 2-3 sesiones.
2. Si "mínimo": escribir `examples/sharding/main.go` con dos shards
   PG en testcontainers; doc de límites en
   `website/docs/advanced/sharding.mdx` (página nueva).
3. Si "completo": abrir F6-7a (scatter-gather, requiere ADR para semántica
   de queries cross-shard) y F6-7b (entity → shard key).

**Comando de verificación:**

```bash
# Ejemplo runnable existe y compila
ls examples/sharding/main.go && cd examples/sharding && go build -o /dev/null ./...

# Página advanced/sharding enlazada
grep -q "advanced/sharding" website/sidebars.ts
```

**Decisión tomada (2026-05-25):** **alcance mínimo** (ver banner ✅ arriba).
Scatter-gather y `shard-key-from-entity` diferidos a v1.1.

---

### ~~Item 3 — `LISTEN/NOTIFY` listener side (PG)~~ ✅ Cerrado (Salida B)

> **Cerrado 2026-05-25 vía Salida B (documentar la asimetría).** La
> limitación inbound se hace visible en `website/docs/advanced/events.mdx`
> (`:::warning Inbound LISTEN is not implemented yet`) y en la fila "Event
> bus + audit log" de `intro.mdx` ("outbound only — inbound
> `LISTEN/NOTIFY` post-v1.0"). El caveat queda recogido en
> `docs/RELEASE_NOTES_v1.0.0.md` §Known limitations. La entrega real del
> inbound (Salida A) se difiere a post-v1.0.
>
> **Corrección factual:** el método devuelve `ErrDialectNotSupported` (no
> `ErrUnsupportedFeature` como decía la versión previa de este item y el
> roadmap) — verificado en `events.go:172`.

**Por qué bloqueante:** `Client.UseEventBus` se presenta en `intro.mdx`
(tabla "Why QUARK") como **"Event bus + audit log"** sin caveat. El
roadmap reconoce que *"`Notify` for outbound notifications is supported;
`ListenerFactory.CreateListener` returns `ErrUnsupportedFeature`"*. La
asimetría outbound-OK / inbound-NO es justa pero no aparece en la cara
pública. Para v1.0, **o se entrega el inbound, o se explicita el caveat
donde el usuario lo ve antes de adoptar la API**.

**Estado hoy:**

- `EventBus.Publish` (outbound, post-commit): ✅ entregado en v0.9.0.
- `Notify` (outbound `pg_notify`): ✅ entregado.
- **`ListenerFactory.CreateListener` (inbound `LISTEN`):** devuelve
  `ErrDialectNotSupported` (`events.go:172`). Requiere conexión dedicada
  fuera del pool de `database/sql`.

**Cómo cerrar (elige una salida):**

1. **Salida A — entregar inbound LISTEN.** Implementación con conexión
   dedicada `pgx.Conn` (no `pgxpool`) para que sobreviva al pool. Sería
   un PR sustancial (2-3 sesiones) con ADR-0018 para la semántica de
   reconnect y backpressure.
2. **Salida B — documentar la asimetría explícitamente.** En `intro.mdx`
   tabla "Why QUARK", desdoblar la fila a *"Event bus outbound (`Publish`,
   `pg_notify`) ✅ / Event bus inbound (`LISTEN`) v1.1"*. En
   `advanced/events.mdx`, añadir sección "Inbound notifications" que
   diga claramente que no está disponible y por qué.

**Comando de verificación:**

```bash
# Salida A
grep -n "ListenerFactory.CreateListener" events.go
# no debe devolver "ErrDialectNotSupported"

# Salida B
grep -E "outbound|inbound" website/docs/advanced/events.mdx website/docs/intro.mdx
# debe haber al menos una mención que aclare la asimetría
```

**Decisión tomada (2026-05-25):** **Salida B** — documentar la asimetría
(ver banner ✅ arriba). Salida A (entregar inbound) diferida a post-v1.0.

---

### ~~Item 4 — Cross-instance stampede protection~~ ✅ Cerrado (Salida B)

> **Cerrado 2026-05-25 vía Salida B (documentar la limitación).** La nota
> "in-process only" se promovió a un `:::warning In-process only —
> cross-instance is post-v1.0` al inicio de la sección "Stampede
> protection" de `website/docs/advanced/caching-observability.mdx`, y la
> fila "Production caché" de `intro.mdx` ahora dice "in-process —
> cross-instance coordination planned post-v1.0". El caveat queda en
> `docs/RELEASE_NOTES_v1.0.0.md` §Known limitations. El hook
> `DistributedLock` (Salida A) se difiere a post-v1.0.

**Por qué bloqueante:** v1.0 con caché L2 a Redis debe tratar el caso
multi-réplica. ADR-0011 admite que el singleflight es **in-process
only**: *"a multi-replica deployment still allows N processes to each
compute the same hot key — much less severe than the in-process
stampede, but real"*. Para v1.0, **o entregamos el distributed lock
hook, o explicitamos la limitación en sitio que el usuario lea antes
de desplegar a multi-réplica**.

**Estado hoy:**

- Singleflight in-process: ✅ entregado (F4-5, ADR-0011).
- TTL jitter + XFetch: ✅ entregado.
- **Distributed lock hook para coordinación cross-instance:** ❌
  documentado en ADR-0011 §"Cuándo reabrir" como diferido hasta que
  haya demanda real.

**Cómo cerrar (elige una salida):**

1. **Salida A — entregar el hook `DistributedLock`.** Interface mínima
   `DistributedLock` (`Acquire(ctx, key, ttl) (bool, error)` + `Release`)
   con implementación de referencia para Redis (SET NX EX). El
   `stampedeStore` lo consume si está configurado; fallback al
   singleflight in-process si no. ADR-0018 (o el siguiente número
   libre) para la semántica. Coste: 1-2 sesiones.
2. **Salida B — explicitar la limitación.** En
   `advanced/caching-observability.mdx` §"Stampede protection", la
   nota "in-process only" existe pero está al final; promoverla a
   warning visible al inicio de la sección. Y añadir entrada en la
   tabla "Why QUARK" del intro que indique "L2 cache stampede
   protection (in-process; cross-instance v1.1)".

**Comando de verificación:**

```bash
# Salida A
grep -rn "DistributedLock" cache_stampede.go cache/
# debe devolver al menos la interface

# Salida B
grep -E "in-process only|cross-instance" website/docs/advanced/caching-observability.mdx
# debe haber un :::warning admonition en la sección Stampede
```

**Decisión tomada (2026-05-25):** **Salida B** — promover la nota
"in-process only" a warning visible + caveat en intro (ver banner ✅
arriba). Salida A (hook `DistributedLock`) diferida a post-v1.0.

---

### Item 5 — `RELEASE_NOTES_v1.0.0.md` con "Known limitations" explícitas

**Por qué bloqueante:** ADR-0017 §3 lo dice literal: el nuevo gate
incluye *"gaps estructurales aceptados conscientemente"*. Si los items
1-4 cierran vía la Salida B (documentar en lugar de entregar), esos
caveats DEBEN aparecer en `RELEASE_NOTES_v1.0.0.md` como sección
"Known limitations", no perdidos en el roadmap.

**Cómo cerrar:**

1. Una vez decididos los items 1-4 (entregados o diferidos), escribir
   `docs/RELEASE_NOTES_v1.0.0.md` con secciones:
   - **What v1.0 means**: contrato de SemVer (`v1.x` mantiene
     compatibilidad de API; breaking changes van a `v2.x`). Mencionar
     LTS si se compromete.
   - **Phases delivered**: Fases 0-6 con un párrafo cada una.
   - **Known limitations**: por cada Salida B de los items 1-4,
     un bullet explicando el caveat y la versión donde se cerrará.
   - **Migration from v0.x**: cualquier breaking acumulado.
2. Validar que no usa lenguaje de marketing (regla CLAUDE.md).
3. Validar que la tabla `comparison.mdx` y el `intro.mdx` "Why QUARK"
   son coherentes con los Known limitations declarados.

**Comando de verificación:**

```bash
test -f docs/RELEASE_NOTES_v1.0.0.md && \
  grep -A20 "Known limitations" docs/RELEASE_NOTES_v1.0.0.md
```

---

## §B · Items recomendados (no bloqueantes, pero altamente sugeridos)

### Item 6 — Bug-bash externo (release candidate v0.x-rc1)

**Por qué recomendado:** un v1.0 sin tracción externa es *"v1.0 según
el mantenedor"*. La superficie pública de Quark es grande (6 dialectos,
4 estrategias multi-tenant, codegen, caché, audit, eventos). Una
ronda de feedback externo levanta issues que el mantenedor ya no ve.

**Cómo:**

1. Taggear `v0.14.0-rc1` con todo lo de v1.0 ya cerrado.
2. Abrir issue template *"v1.0 RC feedback — known limitations
   acceptable?"* y postear en r/golang, Hacker News (si aplica), y
   los canales de Nucleus.
3. Ciclo de 2-4 semanas; cerrar issues bloqueantes; taggear v1.0
   cuando el ritmo de issues nuevos baje a ~0.

**Si se salta:** taggear v1.0 directo sigue siendo defendible, pero
documenta en `RELEASE_NOTES_v1.0.0.md` §Known limitations que la
release no pasó por feedback externo formal.

### Item 7 — F6-3b (UPDATE/partial/batch binder codegen)

**Por qué recomendado pero diferible:** el roadmap lo declara *"deferred;
measured payoff ~1%"*. Con ADR-0017, codegen se reencuadra como
type-safety. F6-4 (typed column accessors) ya da type-safety en columnas
y F6-3a en INSERT; el UPDATE/partial no añade type-safety **incremental**
porque las columnas a actualizar ya pasan por los accesores tipados.
**Defendible diferir a v1.1**, siempre que se diga.

**Salida sugerida:** dejar como Known limitation en
`RELEASE_NOTES_v1.0.0.md`: *"codegen cubre read path (F6-2) e INSERT
single-int-PK (F6-3a); UPDATE/partial/batch siguen reflect path. Cubre
el 90% de los casos donde codegen importa. Cierre formal de F6-3b
diferido a v1.1 según ADR-0017."*

### Item 8 — Versioned migration registry per-Client

**Por qué recomendado pero diferible:** el roadmap lo lista como deuda
documentada (registro global en `migrate/migrate.go`). Para v1.0 con
multi-tenancy "Why QUARK"-grade, **dos clientes en el mismo proceso
compartiendo registry global** es antipático. Si lo difieres,
documéntalo.

**Salida sugerida:** Known limitation en `RELEASE_NOTES_v1.0.0.md`.

---

## §C · Orden de ataque sugerido

Asumiendo que el mantenedor elige Salida B en los items que admiten
diferir (camino más rápido a un v1.0 honesto):

1. **Sesión 1** — Item 1 Salida C: workflow `oracle-nightly.yml`. Es la
   decisión más cara competitivamente; resolverla primero quita peso.
   Si no funciona, fallback a Salida B (degradar posicionamiento).
2. ~~**Sesión 2** — Item 2 mínimo: `examples/sharding/main.go` + doc
   `advanced/sharding.mdx`~~ ✅ **Hecho (2026-05-25)**: ejemplo runnable
   (SQLite) + doc ampliada; scatter-gather y shard-key-from-entity a v1.1.
3. ~~**Sesión 3** — Items 3 y 4 Salida B~~ ✅ **Hecho (2026-05-25)**: las
   asimetrías de EventBus (inbound) y stampede (cross-instance) están
   documentadas en sitio visible (ver §A Items 3 y 4, cerrados).
4. **Sesión 4** — Item 5: redactar `RELEASE_NOTES_v1.0.0.md` con todos
   los Known limitations decididos en sesiones 1-3, más items 7-8.
5. **Sesión 5 (opcional)** — Item 6: taggear `v0.14.0-rc1`, abrir
   ventana de feedback. Cerrar issues. Taggear v1.0.0.

**Tiempo total estimado:** 4-5 sesiones efectivas. Significativamente
menos que las "2-4 semanas" del veredicto de la auditoría del 25, **si
se eligen las salidas B donde corresponde**. Si los items 3 y 4 se
entregan completos (Salida A), añade 3-5 sesiones.

---

## §D · Cómo se cierra este documento

Cuando un item del §A cierre:

1. Editar este documento marcando el item con `~~Item N · …~~` +
   bloque *"**Cerrado** — descripción + PR/commit + archivo:línea"*
   siguiendo el patrón de `TASKS.md`.
2. Si el cierre es vía Salida B, copiar el caveat literal a
   `RELEASE_NOTES_v1.0.0.md` §Known limitations en el mismo PR.
3. El subagente `docs-auditor` debe verificar coherencia entre este
   documento y `RELEASE_NOTES_v1.0.0.md` antes del PR de v1.0.

Cuando los **5 items del §A** estén cerrados, este documento queda como
referencia histórica del gate honesto. **No taggear v1.0.0 antes.**

## §E · Lo que NO entra en este gate

Por claridad — estos no son bloqueantes para v1.0 según ADR-0017 y el
posicionamiento defendido en `ANALISIS_MADUREZ.md` §5.1:

- **Rendimiento ≥3× vs `database/sql`**: explícitamente retirado por
  ADR-0017. El codegen es type-safety, no velocidad.
- **Read replicas health-checking activo (vs pasivo)**: lo entregado
  cubre el caso de uso documentado; active health checks son v1.x.
- **Schema-first DSL al estilo Atlas/Prisma**: long-term goal, fuera
  de v1.0 por diseño (ADR-0009).
- **NoSQL support**: explícitamente fuera del scope (ADR-0005).
- **GraphQL / admin auto-generado**: explícitamente fuera (ADR-0006).
- **Pluggable ID strategies (UUID v7, ULID, Snowflake) built-in**:
  long-term goal; el usuario las registra hoy vía `RegisterTypeMapper`.

Si surge presión para meter alguno de estos en v1.0, **rechazar y
referenciar este documento**. El scope de v1.0 está cerrado a los
5 items del §A.
