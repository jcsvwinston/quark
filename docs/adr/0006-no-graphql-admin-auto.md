---
id: 0006
title: Sin GraphQL ni admin auto-generado
status: accepted
date: 2026-05-10
deciders: jcsvwinston
related: []
supersedes: null
tags: [scope]
---

# 0006 — Sin GraphQL ni admin auto-generado

## Contexto

Algunos ORMs (notablemente **ent** de Meta, **Hasura** como capa, **Prisma** con add-ons) generan automáticamente:

- Una API GraphQL completa derivada del esquema.
- Un panel admin web para CRUD sobre los modelos.
- Resolvers, paginación cursor-based, filtros tipados, todo sin escribir código adicional.

Esto es muy poderoso — y ent lo hace mejor que nadie en Go. También es **un producto distinto del ORM**: implica routing HTTP, autenticación/autorización, generación de UI, conventions de paginación, etc. Compite con Hasura, PostgREST y sistemas similares.

Quark vive **dentro del ecosistema Nucleus**, que es el framework MVC/REST. Nucleus es quien debe ofrecer las capas de API y admin si las necesita; replicarlas en Quark sería:

- Duplicar trabajo con Nucleus.
- Forzar a Nucleus a usar las decisiones de UI/API de Quark.
- Romper la abstracción: el ORM no debe saber de HTTP.

## Decisión

**Quark no genera GraphQL, no genera admin UI, no genera resolvers REST.**

Quark expone un ORM type-safe sobre Go. Cualquier capa por encima (REST, GraphQL, gRPC, admin web) es responsabilidad del consumidor — habitualmente Nucleus, pero también cualquier desarrollador que use Quark standalone.

Nucleus puede (y debe) ofrecer scaffolding de admin/API basado en modelos Quark, pero ese scaffolding vive en Nucleus, no en Quark.

## Consecuencias

**Positivas:**
- Foco. Quark hace una cosa (ORM relacional) y la hace bien.
- No competimos con ent en su terreno fuerte; competimos donde ent es flojo (multi-dialecto serio, multi-tenancy nativa).
- Tamaño del módulo manejable; dependencias mínimas.
- Versionado independiente del frontend/admin.

**Negativas:**
- Devs que vienen de ent o Prisma esperando GraphQL + admin "gratis" lo echarán de menos.
- En diff feature-por-feature contra ent, perdemos esta columna.
- Si Nucleus tarda en ofrecer admin, los usuarios standalone se quedan sin esa pieza.

## Alternativas consideradas

1. **GraphQL opcional vía generator command.** Rechazado: añade dependencia a tooling GraphQL en el repo; la mantenibilidad sería pesada para un nicho.
2. **Admin UI opcional vía package separado (`quark-admin`).** Posible en el futuro pero no por ahora; mejor que viva en Nucleus o en un proyecto comunitario aparte.
3. **Plugin de tercer-party para GraphQL.** Permitido implícitamente: la API de Quark expone metadata suficiente (`GetModelMeta[T]`) para que alguien construya un plugin GraphQL externo. Bienvenido pero no oficial.

## Cuándo reabrir

Si Nucleus llega a v1.0 sin ofrecer admin/REST scaffolding sobre Quark, replantear ofrecer un módulo `quark-admin` minimalista (sin GraphQL — eso seguiría fuera) que cubra el caso de uso interno.
