# Architecture Decision Records

> Decisiones arquitectónicas tomadas en Quark, una por archivo, formato MADR.
> No las cuestiones sin abrir un nuevo ADR que las supersede.

## Índice

| ID | Título | Estado | Fase relacionada |
|---:|---|---|---|
| [0001](0001-active-record-no-data-mapper.md) | Persistencia: Active Record, no Data Mapper | Accepted | — |
| [0002](0002-reflect-default-codegen-fase-6.md) | Reflect por defecto, codegen opt-in en Fase 6 | Accepted | Fase 6 |
| [0003](0003-rls-cliente-fase-5-motor.md) | RLS hoy es WHERE-injection cliente; motor real en Fase 5 | Accepted | Fase 5 |
| [0004](0004-cache-l2-integrada.md) | Caché L2 integrada (memory/redis), no plugin externo | Accepted | Fase 4 |
| [0005](0005-no-nosql-solo-relacional.md) | Quark es relacional. No soporte NoSQL | Accepted | — |
| [0006](0006-no-graphql-admin-auto.md) | Sin GraphQL ni admin auto-generado | Accepted | — |
| [0007](0007-multi-tenancy-tres-estrategias.md) | Multi-tenancy: tres estrategias coexisten | Accepted | Fase 5 |
| [0008](0008-docs-en-mismo-pr-que-api.md) | Documentación se modifica en el mismo PR que la API | Accepted | — |

## Cómo añadir un ADR nuevo

1. Copia la plantilla de uno reciente (frontmatter + secciones Context/Decision/Consequences/Alternatives).
2. Numera secuencialmente (`NNNN-titulo-corto-en-kebab.md`).
3. Estado inicial: `proposed`. Tras discusión: `accepted` o `rejected`.
4. Si supersede una decisión previa, márcalo en `supersedes:` y actualiza el ADR antiguo con `status: superseded`.
5. Añade fila a este índice.
6. Si la decisión afecta arquitectura visible al usuario, refléjalo en `website/docs/architecture/`.

## Para Code

Lee el ADR específico cuando justifiques o cuestiones un patrón. **No reabras decisiones aceptadas sin abrir un nuevo ADR**. Si encuentras código que viola un ADR, eso es un bug, no una alternativa de diseño.
