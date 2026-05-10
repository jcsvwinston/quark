---
id: 0005
title: Quark es relacional. No soporte NoSQL
status: accepted
date: 2026-05-10
deciders: jcsvwinston
related: []
supersedes: null
tags: [scope, architecture]
---

# 0005 — Quark es relacional. No soporte NoSQL

## Contexto

Existe demanda recurrente de "ORMs unificados" que abstraigan también MongoDB, DynamoDB, Cassandra. Algunos ORMs (TypeORM, Mongoose-style en JS) ofrecen un sabor de esto.

La realidad es que las primitivas de NoSQL (key-value, document, wide-column, graph) son lo bastante distintas de SQL como para que cualquier abstracción "unificada" o (a) limita el ORM al mínimo común denominador (CRUD plano, sin joins, sin transactions cross-document fiables), o (b) bifurca el API con un sabor por motor, dejando el "ORM" como mero packaging.

Quark fue diseñado para SQL, sus primitivas (joins, transactions ACID, schema rígido, FKs, CTEs futuras) son SQL-céntricas, y su capa de migraciones depende de DDL.

## Decisión

**Quark soporta sólo bases de datos relacionales que hablen SQL estándar (con dialecto): SQLite, PostgreSQL, MySQL, MariaDB, MSSQL, Oracle.**

Sin soporte de:
- MongoDB, CouchDB, RavenDB (document stores).
- DynamoDB, Cassandra, ScyllaDB (wide-column).
- Redis-as-DB, Memcached (key-value primario).
- Neo4j, Dgraph, JanusGraph (graph DBs).
- TimescaleDB y similares **se aceptan vía dialecto Postgres** (son extensiones, no motores distintos).

Si un usuario necesita persistencia mixta (SQL + Mongo), Quark cubre el lado SQL; el lado NoSQL es responsabilidad de otra librería.

## Consecuencias

**Positivas:**
- Foco. Cada feature del ORM puede asumir SQL: joins, transactions, schema.
- Performance: no hay capas de abstracción para ajustar a primitivas no-SQL.
- Test surface acotado a 6 motores SQL.
- Documentación clara: los ejemplos no necesitan caveat por tipo de DB.

**Negativas:**
- Equipos con stacks heterogéneos no pueden usar Quark como ORM único.
- Cierra la puerta a algunos casos de uso (event sourcing puro, time-series sin Postgres, graph queries nativas).

## Alternativas consideradas

1. **ORM unificado SQL + NoSQL al estilo TypeORM.** Rechazado: TypeORM es famosamente flojo en ambos extremos por intentar abstraer demasiado.
2. **Soporte opcional vía adapters (`quark-mongo`, `quark-dynamo`).** Rechazado: una vez aceptas el primer no-SQL, el API se contamina con conceptos que no aplican a SQL (eventual consistency, schemas dinámicos), o se mantiene puro y los adapters son inútiles.
3. **"Repository pattern" agnóstico que delegue persistencia.** Rechazado: eso es Spring Data, no un ORM; Quark vive en una capa más abajo.

## Lo que esta decisión NO impide

- **Sí** permitir tipos JSON/JSONB ricos (Postgres `jsonb`, MySQL `JSON`). Los datos pueden ser semi-estructurados; el motor sigue siendo relacional.
- **Sí** soportar TimescaleDB, Citus, CockroachDB cuando hablen Postgres-wire compatible.
- **Sí** considerar DuckDB en el futuro (relacional, OLAP) si emerge demanda.

## Cuándo reabrir

Si una distribución relacional emerge con un dialecto SQL parcial (ya pasó con BigQuery, Snowflake), evaluarlo individualmente. Si la demanda fuera de "Mongo nativo" se hace dominante y los usuarios abandonan Quark por eso, reabrir — pero por ahora no es el caso.
