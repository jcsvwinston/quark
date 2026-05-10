# Playbooks por módulo

> Cheat sheets operativos para Code. Cada playbook cubre un módulo crítico del ORM y lista: anti-patterns conocidos, bugs vivos, decisiones que afectan al módulo, y gotchas con archivo:línea.
>
> **Para Code: lee el playbook del módulo donde vas a tocar antes de tocar nada.** Si el playbook contradice algo del código, el código manda — pero abre un issue para reconciliar.

## Índice

| Módulo | Archivos clave | Bugs P0 vivos | ADRs relevantes |
|---|---|:-:|:-:|
| [query-builder](query-builder.md) | `query_builder.go`, `query_exec.go`, `query_crud.go` | P0-1, P0-4, P0-5 | 0001, 0002, 0007 |
| [dialects](dialects.md) | `dialect.go` | P0-2 | 0005 |
| [migrations](migrations.md) | `migrator.go`, `sync.go`, `migrate/migrate.go` | — | 0001 |
| [tenant](tenant.md) | `tenant_router.go`, `client.go` | P0-1 | 0003, 0007 |
| [cache](cache.md) | `cache.go`, `cache/memory/`, `cache/redis/` | — | 0004 |
| [security](security.md) | `internal/guard/`, `security.go` | P0-2, P0-5 | — |

## Mantenimiento

- Actualizar `last_review` en frontmatter cuando se revise el playbook completo (al menos en cada release).
- Si un bug P0 se cierra, mover la entrada a una sección "Historial" del playbook (no borrar — el contexto histórico ayuda).
- Si se descubre un anti-pattern nuevo, añadirlo aquí ANTES o JUNTO con el commit que lo introduce/arregla.
