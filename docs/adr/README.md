# Architecture Decision Records

> Decisiones arquitectónicas tomadas en Quark, una por archivo, formato MADR.
> No las cuestiones sin abrir un nuevo ADR que las supersede.

## Índice

| ID | Título | Estado | Fase relacionada |
|---:|---|---|---|
| [0001](0001-active-record-no-data-mapper.md) | Persistencia: Active Record, no Data Mapper | Accepted | — |
| [0002](0002-reflect-default-codegen-fase-6.md) | Reflect por defecto, codegen opt-in en Fase 6 | Accepted (gate ≥3× superseded by [0017](0017-codegen-type-safety-not-perf-gate.md)) | Fase 6 |
| [0003](0003-rls-cliente-fase-5-motor.md) | RLS hoy es WHERE-injection cliente; motor real en Fase 5 | Superseded by [0012](0012-rls-real-postgres-set-local-plus-policies.md) | Fase 5 |
| [0004](0004-cache-l2-integrada.md) | Caché L2 integrada (memory/redis), no plugin externo | Accepted | Fase 4 |
| [0005](0005-no-nosql-solo-relacional.md) | Quark es relacional. No soporte NoSQL | Accepted | — |
| [0006](0006-no-graphql-admin-auto.md) | Sin GraphQL ni admin auto-generado | Accepted | — |
| [0007](0007-multi-tenancy-tres-estrategias.md) | Multi-tenancy: tres estrategias coexisten | Accepted | Fase 5 |
| [0008](0008-docs-en-mismo-pr-que-api.md) | Documentación se modifica en el mismo PR que la API | Accepted | — |
| [0009](0009-migrations-introspection-diff-not-versioned-files.md) | Migrations: introspection-based diff, not (only) versioned files | Accepted | Fase 3 |
| [0010](0010-per-column-timezone-override.md) | Timezones por columna — híbrido Client default + tag, wire UTC | Accepted | Fase 1 |
| [0011](0011-cache-stampede-protection-wrapper.md) | Cache stampede protection vía wrapper común sobre CacheStore | Accepted | Fase 4 |
| [0012](0012-rls-real-postgres-set-local-plus-policies.md) | RLS real Postgres vía `SET LOCAL app.tenant_id` + `CREATE POLICY` | Accepted | Fase 5 |
| [0013](0013-transactional-hooks-and-sync-eventbus.md) | Hooks transaccionales + EventBus síncrono en commit-phase | Accepted | Fase 5 |
| [0014](0014-codegen-coexistence-typed-registry.md) | Codegen coexiste vía registry de funciones tipadas por tipo con fallback a reflect | Accepted | Fase 6 |
| [0015](0015-read-replicas-routing.md) | Read replicas: routing en ejecución, opt-in, sticky read-your-writes | Accepted | Fase 6 |
| [0016](0016-sharding-shardrouter.md) | Sharding: ShardRouter enruta por shard key vía ClientProvider; sin cross-shard implícito | Accepted | Fase 6 |
| [0017](0017-codegen-type-safety-not-perf-gate.md) | Codegen es type-safety, no velocidad; se retira el gate ≥3× p99 de ADR-0002 | Accepted | Fase 6 |
| [0018](0018-oracle-migration-lock-dbms-lock.md) | Lock de migración Oracle vía `DBMS_LOCK` (session-scoped), no lock-table `FOR UPDATE` | Accepted | Fase 3 / v1.0-gate |
| [0019](0019-inbound-listen-notify-dedicated-conn.md) | Inbound LISTEN/NOTIFY (PostgreSQL) sobre `*sql.Conn` dedicada del pool, no un pool propio | Accepted | post-v1.0 / v1.1 |
| [0020](0020-cross-instance-cache-stampede-coordination.md) | Coordinación de cache-stampede cross-instancia vía capacidad opcional `CacheLocker` (opt-in, wait-and-reread) | Accepted | v1.2 |
| [0021](0021-shard-key-from-entity.md) | Shard key desde la entidad vía interfaz `ShardKeyer` (helper `WithShardKeyOf` caller-side, no un hook del router) | Accepted | v1.2 |
| [0022](0022-scatter-gather-cross-shard-reads.md) | Scatter-gather cross-shard reads vía funcs explícitas (`ScatterGather`/`ScatterCount`), merge caller-side, agregados no-COUNT diferidos | Accepted | v1.2 |
| [0023](0023-driver-modules.md) | Los drivers salen a módulos propios; el contrato vive en `quarkdriver` y los tres predicados de clasificación viajan juntos porque ninguno falla al no reconocer — contesta `false` | Accepted | v1.9 |
| [0024](0024-cli-en-modulo-propio.md) | El CLI a su propio módulo, junto al superapp y a las suites por motor: la build list del consumidor baja de 123 a 39 y el binario no cambia; a cambio, cuatro altas de registro y una serie de versiones propia para el `go install` | Accepted | v1.13 |
| [0025](0025-la-transaccion-fija-el-inquilino.md) | La transacción fija el inquilino: `ForTx` se confina al que abrió la transacción, un contexto sin inquilino lo hereda y uno que nombre a otro falla con `ErrTenantMismatch`; `client.Tx` sobre el `BaseClient` es la misma puerta que `router.Tx` (QK-26) | Accepted | v1.14 |
| [0026](0026-dialect-contract-in-quarkdriver.md) | Successor of 0023: the dialect contract (`Dialect`, `LockOptions`, the optional interfaces, the schema model, the registry) moves to `quarkdriver`, and package `quark` keeps every name as a type alias of the same type — additive, nothing deprecated; new contract is born in the leaf | Accepted | A11 (implemented in Q3) |
| [0027](0027-statement-cache-opt-in.md) | Prepared statements are reused only on request (`WithStatementCache`): one LRU per pool with a reference count, statements prepared once per transaction on its connection, never on PostgreSQL (pgx caches) nor on Oracle (go-ora v2.9.0 answered a re-executed statement from a stale result) (QK-37) | Accepted | A12 (implemented in Q3) |
| [0028](0028-createbatch-keys-without-returning.md) | `CreateBatch` without `RETURNING` leaves the per-row form only where the engine proves each row's key: an insert-only `MERGE` that returns keys with their row's position on SQL Server, a multi-row `INSERT` on MySQL only under `innodb_autoinc_lock_mode` 0/1; a failed chunk is undone and runs row by row (QK-36) | Accepted | A12 (implemented in Q3) |
| [0029](0029-extension-points-do-not-grow-in-v1.md) | Within v1 an extension point (a type a third party implements and Quark calls: dialects, drivers, hooks, stores, middleware, observers) gains no method and changes no member — new capability is a new optional interface checked by type assertion, with a default; a plumbing interface (one Quark implements and hands out) may gain a method in a minor. Owner's decision of 2026-10-06; `TestExtensionPointsFrozen` enforces it | Accepted | A11 follow-up (v1.17) |

## Cómo añadir un ADR nuevo

1. Copia la plantilla de uno reciente (frontmatter + secciones Context/Decision/Consequences/Alternatives).
2. Numera secuencialmente (`NNNN-titulo-corto-en-kebab.md`).
3. Estado inicial: `proposed`. Tras discusión: `accepted` o `rejected`.
4. Si supersede una decisión previa, márcalo en `supersedes:` y actualiza el ADR antiguo con `status: superseded`.
5. Añade fila a este índice.
6. Si la decisión afecta arquitectura visible al usuario, refléjalo en `website/docs/architecture/`.

## Para Code

Lee el ADR específico cuando justifiques o cuestiones un patrón. **No reabras decisiones aceptadas sin abrir un nuevo ADR**. Si encuentras código que viola un ADR, eso es un bug, no una alternativa de diseño.
