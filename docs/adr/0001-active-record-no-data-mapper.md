---
id: 0001
title: Persistencia — Active Record, no Data Mapper
status: accepted
date: 2026-05-10
deciders: jcsvwinston
related: [0002, 0007]
supersedes: null
tags: [architecture, persistence, api]
---

# 0001 — Persistencia: Active Record, no Data Mapper

## Contexto

Los ORMs maduros se reparten entre dos patrones canónicos:

- **Active Record** (Rails ActiveRecord, GORM, Eloquent): el modelo es un struct/clase que conoce su persistencia. `user.Save()`, `user.Delete()`. La PK vive en el propio entity.
- **Data Mapper + Unit of Work** (Hibernate, Doctrine, SQLAlchemy ORM, EF Core): un repositorio/sesión gestiona persistencia; el entity es POGO/POJO. `session.Add(user); session.Commit()`. El ORM mantiene Identity Map y dirty tracking.

Data Mapper habilita capacidades muy potentes (Identity Map, lazy loading transparente, dirty tracking automático con `entity.field = X; commit()`, cascadas finas), a costa de complejidad arquitectónica fuerte: hay que mantener una sesión, hooks transaccionales no triviales, y una "magia" que sorprende a developers Go acostumbrados a explicitness.

## Decisión

Quark adopta **Active Record**. Modelos son structs Go con `db` tags + interfaces de hooks (`BeforeCreate`, `AfterUpdate`, etc.). La PK vive en el struct. Las operaciones se ejecutan vía `quark.For[T](ctx, client).Save(&entity)`.

**No habrá Unit of Work, no habrá Identity Map, no habrá lazy loading transparente.** Eager loading explícito vía `.Preload(...)`.

## Consecuencias

**Positivas:**
- API idiomática Go: explícita, sin "sesión" implícita ni magia de proxies.
- Curva de aprendizaje suave para usuarios que vienen de GORM.
- Implementación más pequeña y mantenible.
- Compatible con generics ligeros (`Query[T]`).

**Negativas:**
- **No se puede hacer `entity.Name = "x"; commit()` y que el ORM emita el UPDATE.** El usuario debe pasar el struct entero a `Update`, lo que cruza con la herida de `isZeroValue` (ver bug P0-4 en `TASKS.md`).
- Sin Identity Map → cada `Find(id)` puede devolver un struct distinto para el mismo id; el desarrollador debe gestionar coherencia.
- Cascadas son recursivas pero limitadas (no hay orphan removal automático estilo JPA).
- Techo arquitectónico más bajo que Hibernate/EF Core.

**Mitigación parcial (Fase 1):** introducir **dirty tracking ligero opt-in** vía `.Track()` que tome snapshot al cargar y permita `Save()` que sólo emita UPDATE de campos cambiados. Esto cierra parcialmente la herida sin pedir la complejidad completa de Unit of Work.

## Alternativas consideradas

1. **Data Mapper completo (Hibernate-style).** Rechazado: demasiada complejidad para un ORM Go en alpha; rompe expectativa del ecosistema.
2. **Híbrido (Active Record + sesión opcional).** Rechazado: bifurca la API; usuarios se confunden sobre cuándo usar cada modo.
3. **Sólo data builder sin ORM (sqlc/jet style).** Rechazado: deja fuera el caso de uso de Nucleus, donde se quieren modelos con hooks.

## Cuándo reabrir esta decisión

Si tras Fase 6 (codegen) hay demanda repetida de Identity Map o de cascadas declarativas equivalentes a JPA, abrir un ADR sucesor que evalúe introducir un modo `Session` opcional.
