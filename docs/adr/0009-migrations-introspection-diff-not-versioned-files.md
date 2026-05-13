---
id: 0009
title: Migrations strategy — introspection-based diff, not (only) versioned files
status: accepted
date: 2026-05-13
deciders: jcsvwinston
related: []
supersedes: null
tags: [migrations, schema, architecture]
---

# 0009 — Migrations strategy: introspection-based diff, not (only) versioned files

## Contexto

Quark hoy tiene dos rutas de migración:

1. **`client.Migrate(ctx, &Model{}, …)`** — auto-migración. Crea tablas si no existen; añade columnas que faltan. Comparación naive (existencia de columna), no detecta cambios de tipo, NOT NULL, defaults, índices, FKs ni checks.
2. **`internal/migrate.Migrator`** — versionado por archivos. Maneja Up/Down explícitos con sequence number. Sin diff: el desarrollador escribe las migraciones a mano.

Ambos son razonables como punto de partida, pero el gap respecto a Alembic / EF Migrations / Atlas / golang-migrate / sqlc es real. El análisis de Fase 3 en `ANALISIS_MADUREZ.md` §4 lo identifica como **el bloqueante para "un equipo serio aceptaría Quark"**: sin schema-diff confiable un equipo grande no migra a producción.

Los caminos posibles para cerrar la brecha:

- **A. Schema-first (Atlas-style)**: el SQL/HCL es la fuente de verdad; el código Go se genera. Estado del arte para proyectos green-field; doloroso para retrofit y para equipos que quieren el modelo en Go.
- **B. Code-first + versioned files (Alembic / golang-migrate)**: el modelo Go es la verdad; cada cambio se traduce a un archivo de migración. Robusto pero verboso. La migración a producción es disciplinada.
- **C. Code-first + diff bidireccional (EF Migrations / Prisma)**: el modelo Go es la verdad; un `quark schema diff` introspecciona el DB en vivo, lo compara con el modelo, y emite la migración candidata Up + Down. El desarrollador la revisa, la versiona, la aplica. Sin pasos manuales para los cambios obvios; sigue habiendo revisión humana para los matices.

## Decisión

**Phase 3 adopta la estrategia C: code-first + diff bidireccional.** Schema-first queda descartada explícitamente (Atlas lo cubre bien; no aporta diferenciación a Quark). Versioned files explícitos se mantienen como soporte secundario para los casos donde el desarrollador prefiere control total (`internal/migrate.Migrator` no se retira; gana en cambio un comando `quark schema diff` que emite archivos de migración candidatos).

La superficie a entregar en Phase 3 se descompone en siete items (`F3-1`..`F3-7` en `TASKS.md`):

1. **`F3-1` — Lock distribuido** (PG `pg_advisory_xact_lock`, MySQL `GET_LOCK`, MSSQL `sp_getapplock`, Oracle `DBMS_LOCK`). Mutex de migración a nivel de cluster: el primer proceso que arranca lo adquiere; los demás esperan (con timeout). Infraestructura para todo lo demás.
2. **`F3-2` — Schema introspection**. Por dialect, devolver una representación neutral del schema actual (tablas, columnas con tipos / NOT NULL / defaults, índices, FKs, checks). Equivalente a `pg_dump --schema-only` pero estructurado.
3. **`F3-3` — Schema diff core**. Comparador que toma el schema de Go y el schema del DB y emite operaciones bidireccionales (`AddColumn`, `DropColumn`, `AlterColumnType`, `AddIndex`, …). Marca cada operación con un `RiskLevel` (`safe` / `lossy` / `breaking`).
4. **`F3-4` — Migración transaccional + resumable**. Wrapper sobre `BEGIN ... COMMIT` con savepoints; en MySQL (donde DDL no es transaccional) state-checkpoint + resume token. Cualquier migración aborta limpio si una operación falla.
5. **`F3-5` — Dry-run plan**. `quark schema diff --plan` que muestra el DDL up/down propuesto + warnings de RiskLevel, sin ejecutar. Estilo `terraform plan`.
6. **`F3-6` — Backfill orquestado**. `Migration.Backfill(fn, batchSize, resumeToken)` para data migrations grandes que no caben en una transacción. Resume token persistido en `quark_migration_state`.
7. **`F3-7` — Per-client model registry**. Refactor del registro global a un registro por `*Client`. Permite tests independientes y multi-tenant strict.

Cada item llega como un PR auto-contenido con su test cross-engine en la matriz integration. La fase se cierra cuando los siete están tachados — momento en el que se taggea **v0.6.0** (per ROADMAP).

## Consecuencias

**Positivas:**
- Equipos pueden usar `quark schema diff` como `terraform plan` para auditar cambios antes de mergearlos.
- El developer no escribe DDL a mano para los cambios obvios, sólo revisa.
- El RiskLevel del diff hace explícitos los cambios destructivos (drop column, narrow type) que requieren review humana.
- Lock distribuido previene que dos pods migren simultáneamente — incidente real en producción de proyectos sin esto.

**Negativas:**
- La introspección por dialect es trabajo significativo (cada motor expone metadata diferente). Cinco implementaciones de F3-2.
- El diff bidireccional tiene casos donde la inferencia es ambigua (rename vs drop+add). Necesitará heurísticas + opt-in para casos dudosos.
- Versioned migrations no se retira — coexiste con el diff. Si Phase 3 entrega bien el diff y nadie usa versioned, lo retiraremos en Phase 4 con una nota de deprecation. Hasta entonces, dos rutas.

## Alternativas consideradas

- **Atlas como dependencia** (no como replacement). Considerado: Atlas es excelente pero (a) añade una dependencia binaria pesada, (b) cubre el caso schema-first que rechazamos, (c) el coste integracional es comparable a implementar nuestro diff. Descartado por ahora; un wrapper opcional sobre Atlas para usuarios que ya lo usan queda como ticket abierto.
- **Auto-migración silenciosa al arrancar el `Client`**. Considerado y descartado: este patrón (Hibernate `hbm2ddl.auto=update`) es notoriamente peligroso en producción. Los cambios de schema deben ser intencionales y auditables. `client.Migrate(ctx, …)` se queda como opt-in explícito; nunca se ejecuta automáticamente desde `New`.

## Cuándo reabrir

Si tras dos releases con Phase 3 entregado el equipo descubre que el diff bidireccional no escala (proyecto con > 200 tablas, migraciones que se vuelven impredecibles), reabrir para considerar:
- Schema-first como ruta secundaria opt-in (Atlas wrapper).
- Codegen para que el modelo Go se derive del schema en vez de al revés.

Hasta entonces, Phase 3 es la inversión.
