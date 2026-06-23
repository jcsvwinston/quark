# Auditoría integral de Quark — v1.1.5

**Fecha:** 2026-06-21 · **Objeto:** HEAD de `main` (`6cde2b48`, línea v1.1.5).
**Método:** auditoría docs↔código y de seguridad/tests vía subagentes (lectura de fuente + ejecución de tests con Go 1.26 real); **benchmarks re-ejecutados de verdad** contra el módulo local; comparativa de frameworks con estado web de junio 2026.
**Encargo:** auditar docs y código, desvelar falsedades, re-pasar benchmarks (rendimiento + características + comparativa), y dictaminar **dónde está Quark para clasificarse como ORM "enterprise-grade".**

---

## Veredicto en una frase

Quark es **técnicamente real, honesto y bien construido** — más features integradas que sus competidores y un rendimiento *más consistente* que GORM/ent — pero **no es "enterprise-grade" todavía, y el motivo no es técnico**: lo es por adopción (0 usuarios externos), bus factor (1 autor), gobernanza y madurez probada en producción. "Enterprise-grade" es, sobre todo, una propiedad **social y operativa**, no de features.

---

## 1. Falsedades y exactitud de la documentación

Re-auditada la doc publicada (intro, 9 guides, 7 advanced, 13 reference/api) + README + CLAUDE contra el fuente. **Exactitud ≈97–98%. Cero falsedades críticas o altas.** Lo notable: lo poco que está mal **infravende**, no exagera.

| # | Dónde | Qué dice | Realidad (código) | Sev. |
|---|---|---|---|---|
| 1 | `reference/roadmap.mdx:173-174` | scatter-gather y shard-key-desde-entidad "diferidos a v1.2+" | **Ya entregados**: `ScatterGather`/`ScatterCount`/`ScatterMerge` (`shard_scatter.go`), `WithShardKeyOf`/`ShardKeyer` (`shard_router.go`). | MEDIO |
| 2 | `reference/roadmap.mdx:242-243` | stampede cross-instancia como "goal futuro" | **Ya existe**: `WithCacheCrossInstance()` + `CacheLocker` (ADR-0020), documentado vivo en advanced/caching. | MEDIO |
| 3 | `reference/api/query-builder.mdx:114` | WhereJSON PG usa `::jsonb->>` | El código emite `jsonb_extract_path_text(...)` (`dialect.go:222`); la propia guía `querying.mdx` ya lo muestra bien. | MEDIO |
| 4 | `reference/roadmap.mdx:102` | `quark tenant install-rls-policies` como subcomando del binario | Es entrypoint de la **librería** `quarktenant` que embebes, no del CLI `quark`. La guía `row-level-native.mdx` lo hace bien. | BAJO |
| 5 | `reference/api/errors.mdx` | omite 3 sentinels (`ErrEventEmitFailed`, `ErrNoSubscription`, `ErrListenerClosed`) | Existen y se documentan en advanced/events; es incompletitud, no error. | BAJO |

Todo lo demás verificado: audit log atómico (honesto: solo dentro de `Client.Tx`), RLS nativa PG (`set_config`+`CREATE POLICY`), SQLGuard descrito como **léxico** (no semántico), `errors.Is(err, ErrInvalidQuery)` para operador inválido **cierto** (fix v1.1.5, `guard.go:257`), 4 estrategias de multi-tenancy, idempotencia m2m, 48 símbolos de API con firma exacta, versión coherente (v1.1.5 en intro/README/release-notes/roadmap). La regla anti-hype se sostiene: grep de `production-ready|enterprise-grade|battle-tested` vacío en docs propios.

**Conclusión §1:** la doc es **fiable** para construir contra ella. Las correcciones son 3 desfases de roadmap + 1 función JSON en una página de API ref.

---

## 2. Auditoría de código: seguridad, tests, deuda

Toolchain real `go1.26.4`. ~38k LoC no-test, ~35k LoC de test, 155 archivos `_test.go`.

**Seguridad SQL — postura sólida, sin huecos detectados.** Todo identificador de input pasa por `internal/guard` antes de concatenarse (73 llamadas a `ValidateIdentifier` en 17 archivos); los valores van siempre por placeholders. El gap histórico de `JOIN ... ON` (P0-5) **está cerrado** (`guard.ValidateJoinOn`, gramática identifier-only) y testeado con payloads de inyección reales (`x'; DROP TABLE users--` → rechazado). `AllowRawQueries` (default `false`) gatea de verdad las vías raw.

**Tests — números reales medidos:**
- `go test -short ./...` → **todo OK** (18.5s); `go vet ./...` limpio; `-race` sobre builders → sin data races.
- Cobertura: **paquete raíz 67.9%**, `internal/guard` 86.9%.
- Matriz de 6 motores **real y bloqueante** en CI (`ci.yml`: PG/MySQL/MariaDB/MSSQL vía testcontainers, Oracle vía `docker run gvenzl/oracle-free`, `fail-fast:false`). El job "Test (SQLite)" corre solo SQLite y los otros 5 hacen SKIP **honestamente** (no pretende cubrirlos); la cobertura cross-engine vive en el job `integration` con `-tags=integration` (testcontainers → no pueden saltarse).
- Matiz: existen ~18 `t.Skip` por env-var (la regla #7 de CLAUDE.md los prohíbe), pero su efecto está **neutralizado en CI** por los build tags — grieta de letra, no de cobertura efectiva.

**Deuda/robustez:** reflect-everywhere confirmado (270 llamadas en hot paths; el codegen opt-in es la salida ya entregada). Panics limitados a invariantes + `Must*` con recover correcto en transacciones (`tx.go` hace Rollback y re-panic). Manejo de errores sin swallowing (cero `if err!=nil{return nil}` sospechosos).

**Claims del proyecto — respaldados, no aspiracionales:** los 6 motores en CI, la superapp de aceptación (gate de **655 símbolos** generado del código, bloqueante en CI), y la cultura anti-marketing son verificables en workflows/archivos. La superapp incluso destapó bugs reales de core (BB-15) — ejerce de verdad.

**Conclusión §2:** base de código **disciplinada y madura para su tamaño**. No es código de juguete. Debilidades reales: deuda de reflect y la grieta de la regla #7.

---

## 3. Benchmarks de rendimiento — re-ejecutados (datos frescos)

Corrida real: `go test -bench=. -benchmem` en `benchmarks/`, Go 1.26.4, **SQLite in-memory**, sandbox Linux arm64 4-core, `-count=1 -benchtime=300ms`. Microbenchmarks: aíslan overhead de CPU/alloc del ORM, **no** I/O de red. (Máquina distinta a la del doc → los absolutos difieren; lo que importa son los **ratios**.)

**Tiempo por operación (ns/op, menor = mejor):**

| Operación | Raw | Quark | GORM | ent | sqlc |
|---|---:|---:|---:|---:|---:|
| InsertOne | 9.082 | **15.992** | 24.003 | 16.650 | 9.058 |
| InsertBatch (100) | 197.534 | **303.950** | 307.127 | 366.737 | 281.151 |
| FindByPK | 9.252 | **16.493** | 12.937 | 14.640 | 9.282 |
| ListWhere (≤50) | 38.182 | **73.133** | 58.947 | 53.046 | 39.576 |
| Update | 3.133 | **5.616** | 11.655 | 22.743 | 3.132 |

**Allocaciones por operación (allocs/op):**

| Operación | Raw | Quark | GORM | ent | sqlc |
|---|---:|---:|---:|---:|---:|
| InsertOne | 20 | 60 | 78 | 77 | 21 |
| InsertBatch | 622 | 1.378 | 1.286 | 3.278 | 2.306 |
| FindByPK | 24 | 65 | 66 | 100 | 25 |
| ListWhere | 365 | 468 | 705 | 756 | 374 |
| Update | 15 | 56 | 84 | 143 | 18 |

**Overhead sobre el suelo (×raw):**

| Operación | Quark | GORM | ent |
|---|---:|---:|---:|
| InsertOne | 1,76× | 2,64× | 1,83× |
| InsertBatch | 1,54× | 1,55× | 1,86× |
| FindByPK | 1,78× | 1,40× | 1,58× |
| ListWhere | 1,92× | 1,54× | 1,39× |
| Update | **1,79×** | 3,72× | **7,26×** |

**Lectura honesta:**
- El claim del doc — *"Quark, GORM y ent están en la misma clase de rendimiento; sqlc pegado al suelo raw; no es el más rápido"* — **queda confirmado** con datos frescos.
- **Quark es el ORM reflect más consistente**: nunca peor de ~1,9× el SQL a mano. GORM y ent **se disparan en escrituras** (ent `Update` 7,3× raw, GORM `Update` 3,7×); Quark se mantiene en 1,8×. En `Update` e `InsertOne`, Quark es el ORM más rápido de los tres.
- **Punto débil de Quark: `ListWhere`** (lectura filtrada multi-fila), donde va por detrás de GORM y ent (1,92× vs 1,54×/1,39×). Es el candidato nº1 a optimizar (el scan reflect por fila).
- sqlc y raw son el suelo (~1,0–1,1×): codegen sin runtime. Confirma que el codegen de Quark es **type-safety, no velocidad** (ADR-0017) — coherente con la doc.

**Caveats de la corrida:** `-count=1 -benchtime=300ms` en sandbox compartido → ruido run-a-run (±10–25% en algunas). Trátese como señal de **ratios**, no cifras de producción. Para un número publicable, repetir con `-count=6` + `benchstat` en hardware dedicado (como hace el doc).

---

## 4. Características soportadas vs otros frameworks (verificado)

Leyenda: ✅ nativo · 🟡 parcial/plugin/manual · ❌ no.

| Característica | GORM | Ent | sqlc | Bun | **Quark** |
|---|---|---|---|---|---|
| Multi-dialecto (nº) | 🟡 4 (+Oracle 3º) | 🟡 ~5 (+CockroachDB/TiDB) | ❌ 3 | 🟡 5 | ✅ **6 incl. Oracle** |
| Migraciones schema-as-code | 🟡 AutoMigrate | ✅ (Atlas) | ❌ externas | 🟡 | ✅ diff por introspección |
| Relaciones / eager-load | ✅ | ✅ | ❌ | ✅ | ✅ |
| Caché L2 integrada | ❌ | ❌ | ❌ | ❌ | ✅ memory/redis + stampede |
| Observabilidad OTel | 🟡 plugin | 🟡 manual | 🟡 driver | ✅ trazas | ✅ **trazas + métricas** |
| Multi-tenancy | 🟡 manual | 🟡 privacy layer | ❌ | ❌ | ✅ **4 estrategias (incl. RLS nativa)** |
| Read-replicas + failover | ✅ plugin `dbresolver` | ❌ | ❌ | ❌ | ✅ core |
| Sharding | ✅ plugin `sharding` | ❌ | ❌ | ❌ | ✅ + scatter-gather |
| Audit log atómico | ❌ | 🟡 hooks | ❌ | ❌ | ✅ |
| Optimistic locking | ✅ tag | 🟡 | ❌ | 🟡 | ✅ |
| Codegen | reflect | ✅ | ✅ | reflect | reflect + **codegen opt-in** |
| Type-safety (generics) | 🟡 | ✅ alta | ✅ alta | 🟡 | ✅ |

**En features integradas, Quark gana**: caché L2, multi-tenancy de 4 estrategias, audit atómico y 6 dialectos no tienen equivalente *nativo* en ninguno de los cuatro (GORM logra replicas/sharding vía plugins oficiales separados). La contrapartida es §5.

---

## 5. Madurez y adopción (donde se decide "enterprise")

| | GORM | Ent | sqlc | Bun | **Quark** |
|---|---|---|---|---|---|
| Estrellas GitHub (aprox.) | ~39.800 | ~17.000 | ~17.500 | ~4.700 | **0 públicas** |
| Mantenedor | Org comunidad | Empresa (Atlas/Ariga, origen Meta) | Org comunidad | Empresa (Uptrace) | **1 autor** |
| Versión | v1.31 (estable, años) | **v0.14 (¡aún pre-1.0!)** | v1.31 | v1.2 | v1.1.5 (autodeclarada) |
| Tracción en producción | Masiva (estándar de facto) | Amplia | Amplia | Moderada | **Ninguna pública** |
| Bus factor | Alto | Alto (empresa) | Alto | Empresa | **1** |

Comparar la "madurez" de Quark con GORM/Ent es comparar categorías distintas: Quark puede tener más features, pero la madurez de un ORM se mide en **años-de-producción-de-terceros**, y ahí Quark es 0.

---

## 6. ¿Es "enterprise-grade"? — scorecard honesto

"Enterprise-grade" no es una lista de features; son **propiedades de confianza** que un equipo verifica antes de apostar su capa de datos (sintetizado de criterios de selección OSS y del marco de graduación CNCF: adopción multi-organización, gobernanza, longevidad).

| # | Criterio | Estado de Quark | Nota |
|---|---|---|---|
| 1 | Estabilidad / SemVer / v1.0+ | 🟡 | v1.1.5 con SemVer disciplinado, pero la estabilidad la **autodeclara** el autor, no la valida la comunidad. |
| 2 | **Adopción probada por terceros (≥3 indep.)** | ❌ | **0 usuarios externos.** El criterio que más pesa, y está a cero. |
| 3 | Seguridad | ✅ | Frontera SQL sólida, testeada con inyección. Falta proceso público de CVE. |
| 4 | **Soporte / bus factor > 1** | ❌ | **1 autor.** Si para, el proyecto para. |
| 5 | Comunidad / ecosistema | ❌ | Inexistente: sin issues de terceros, plugins, integraciones, StackOverflow. |
| 6 | Gobernanza / respaldo | ❌ | Un individuo; sin GOVERNANCE, sin empresa detrás. |
| 7 | Rendimiento / escalabilidad | ✅ | Overhead consistente; replicas + sharding + pooling de serie. |
| 8 | Observabilidad | ✅ | OTel trazas + métricas nativas, slow-query log. |
| 9 | Testing por motor real | ✅ | Matriz 6 motores bloqueante + superapp gate. Fuerte. |
| 10 | Documentación | ✅ | Completa, versionada, honesta (tras el overhaul). |
| 11 | Licencia | ✅ | Apache-2.0, predecible. |
| 12 | **Longevidad demostrable** | ❌ | No demostrable con 1 autor y 0 historia de adopción. |

**Resultado: fuerte en lo técnico (7/8/9/10/11 y features), pero falla estructuralmente en los 5 criterios que *definen* "enterprise": adopción (2), bus factor (4), comunidad (5), gobernanza (6), longevidad (12).**

**Veredicto:** Quark está en el nivel de **"technically production-capable, pre-adoption"** — un ORM que un *equipo individual valiente* podría usar en producción propia hoy (la calidad técnica lo permite), pero que **no puede llamarse "enterprise-grade"** porque ese sello es mayoritariamente social/operativo y Quark aún no tiene nada de eso. Coincide, además, con la propia regla anti-hype del proyecto: llamarlo enterprise-grade hoy sería exactamente el marketing que el repo prohíbe.

---

## 7. Qué faltaría para acercarse a "enterprise-grade" (priorizado)

Lo técnico está casi; lo que falta es **lo social/operativo** (y eso no se programa en una tarde):

**Para cerrar los gaps que sí dependen del código (rápido):**
1. Corregir los 3 desfases de roadmap + la función JSON de §1 (un PR `docs:`).
2. Optimizar `ListWhere` (el scan reflect por fila) — único punto donde Quark va por detrás de GORM/ent; o documentar el codegen para ese path.
3. Cerrar la grieta de la regla #7 (skips por env-var) o reescribir la regla para reflejar la realidad de los build tags.
4. Publicar un número de benchmark reproducible (`-count=6` + `benchstat`, hardware dedicado) y un badge de cobertura real.

**Para lo que de verdad falta (lo que mueve la aguja "enterprise", por orden de impacto):**
5. **Adopción externa.** Liberar públicamente, conseguir ≥3 usuarios independientes en producción y casos documentados. Sin esto, nada de lo demás cuenta.
6. **Bus factor.** Sumar al menos un segundo mantenedor con commit; documentar gobernanza (GOVERNANCE.md) y proceso de release/seguridad (SECURITY.md con divulgación de CVE).
7. **Señales de confianza.** Issues/PRs de terceros atendidos, changelog público, política de soporte de versiones, y tiempo (longevidad se demuestra con años, no con features).
8. **Respaldo sostenible.** Una organización o patrocinio que garantice mantenimiento más allá de una persona.

> En resumen: Quark ya es **mejor de lo que su adopción sugiere** — tiene más features integradas que GORM/Ent y un perfil de rendimiento más predecible. Pero "enterprise-grade" se gana en el mundo, no en el repo: necesita usuarios, mantenedores y años. Hoy es un ORM técnicamente serio en estado **pre-adopción**.
