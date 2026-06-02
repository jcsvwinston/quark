# bugbash/ — operativo para Code

> Este directorio aloja el bug-bash post-v1.0 diseñado en
> [`docs/BUGBASH_PLAN.md`](../docs/BUGBASH_PLAN.md) sobre el dominio descrito en
> [`DOMAIN.md`](DOMAIN.md). Es **el "consumidor externo" del que carecía
> v1.0** — la pasada que detecta lo que la suite unitaria no atrapa.
>
> No usar para desarrollo normal. Lee el plan antes de tocar nada.

## Estado de implementación

| Pieza | Estado |
| --- | --- |
| Plan maestro | ✅ [`docs/BUGBASH_PLAN.md`](../docs/BUGBASH_PLAN.md) |
| Dominio | ✅ [`DOMAIN.md`](DOMAIN.md) (spec) + [`domain/`](domain) (structs Go) |
| `bugbash/go.mod` con `replace ../` | ✅ hecho |
| `bugbash/domain/*.go` | ✅ hecho (20 modelos + mappers uuid/decimal) |
| `bugbash/tools/` | ✅ [`docker.go`](tools/docker.go) (boot/teardown por motor) |
| `bugbash/seed/seed.go` | ⏳ pendiente (siguiente sesión) |
| Fases F0-F14 | 🚧 Hechas: **F0** (`f00_install`), **F1** (`f01_smoke`), **F2** (`f02_api_surface`), **F3** (`f03_relaciones`), **F4** (`f04_volume`, cap/paginación/streaming/chunking — halló y cerró BB-10), **F5** (`f05_tenancy`), **F6** (`f06_migrations`, plan/diff / apply / backfill+resume / versioned Up·Down / lock — halló y cerró BB-11, BB-12), **F7** (`f07_cache`), **F8** (`f08_hooks`), **F9** (`f09_codegen`, paridad generated-vs-reflect / WhereP SQL / gates de versión y drift — sin hallazgos), **F10** (`f10_sharding`, routing por shard key / cero leaks / tx por-shard — sin hallazgos), **F11** (`f11_replicas`, read/write split / sticky / failover — sin hallazgos), **F12** (`f12_resilience`, deadlock retry / pool / pánico-rollback / concurrencia — sin hallazgos), **F13** (`f13_security`). Pendientes: F14 (soak; una por sesión). Leg de codegen de F0 (`quark gen`) pendiente. Nota: F13 está implementada fuera de orden por ser el gate obligatorio antes de cualquier patch v1.0.x. |
| Subagente `bugbash-reporter` | ✅ [`.claude/agents/bugbash-reporter.md`](../.claude/agents/bugbash-reporter.md) |
| Slash command `/bugbash` | ✅ [`.claude/commands/bugbash.md`](../.claude/commands/bugbash.md) |

## Arranque rápido (cuando esté implementado)

```bash
# Una sola fase contra un solo motor
/bugbash f01_smoke --engines=sqlite

# Una fase contra todos los motores
/bugbash f02_api_surface --engines=all

# Todas las fases sin soak (F0-F13)
/bugbash all --engines=all

# Soak overnight (F14, 12h x 6 motores)
/bugbash f14_soak --engines=all --soak
```

El comando arranca los contenedores necesarios (PG/MySQL/MariaDB/MSSQL/
Oracle vía `docker run`, mismo patrón que CI bloqueante), corre la fase
con un seed determinista (`--seed=42` por defecto), recolecta los
fallos en `REPORTS/run-<timestamp>/`, y delega al subagente
`bugbash-reporter` para clasificar e informar.

## Estructura física esperada

```
bugbash/
├── README.md                       # este archivo
├── DOMAIN.md                       # spec del dominio
├── go.mod                          # módulo independiente; replace ../
├── go.sum
├── domain/                         # structs del dominio (uno por entidad)
│   ├── organization.go
│   ├── user.go
│   ├── user_profile.go
│   ├── role.go
│   ├── user_role.go
│   ├── category.go
│   ├── product.go
│   ├── warehouse.go
│   ├── inventory.go
│   ├── customer.go
│   ├── order.go
│   ├── order_line.go
│   ├── payment.go
│   ├── refund.go
│   ├── invoice.go
│   ├── invoice_line.go
│   ├── tax_rule.go
│   ├── audit_event.go
│   ├── attachment.go
│   ├── note.go
│   ├── quark_gen.go                # opt-in, generado por `quark gen ./domain/`
│   └── doc.go                      # package doc
├── seed/                           # generadores deterministas
│   ├── seed.go                     # entrada principal; toma --seed --scale
│   ├── distributions.go            # Pareto, log-normal, etc.
│   ├── faker.go                    # nombres, emails, direcciones realistas
│   └── seed_test.go                # verifica reproducibilidad del seed
├── tools/                          # helpers reutilizados por fases
│   ├── docker.go                   # boot/down de contenedores por motor
│   ├── metrics.go                  # scraper de quark.queries.* OTel
│   ├── pool_check.go               # verifica leaks de conexión post-test
│   └── coverage.go                 # mide % de API ejercitada vs export list
├── phases/
│   ├── f00_install/
│   │   ├── README.md               # objetivo y criterio de done
│   │   └── install_test.go         # go test -tags=bugbash
│   ├── f01_smoke/
│   ├── f02_api_surface/
│   ├── f03_relations/
│   ├── f04_volume/
│   ├── f05_multitenancy/
│   ├── f06_migrations/
│   ├── f07_cache/
│   ├── f08_hooks_events_audit/
│   ├── f09_codegen/
│   ├── f10_sharding/
│   ├── f11_replicas/
│   ├── f12_resilience/
│   ├── f13_security/
│   └── f14_soak/
└── REPORTS/                        # generado por runs; gitignore esperable
    └── run-2026-MM-DD-HHMM/
        ├── summary.md              # narrativa para humano
        ├── failures.json           # estructurado para reporter
        ├── coverage.json           # % de API ejercitada
        ├── metrics/                # snapshots OTel por fase
        │   └── f02_api_surface_oracle.json
        ├── datasets/               # SQL para reproducer
        │   └── f04_volume_seed42.sql
        └── per-engine/
            ├── postgres/
            ├── mysql/
            ├── mariadb/
            ├── mssql/
            ├── oracle/
            └── sqlite/
```

## Convenciones de Code para implementar

### Tests con build-tag

```go
//go:build bugbash
// +build bugbash

package f01_smoke
```

El tag `bugbash` aísla los tests del `go test ./...` normal — el bug-bash
es opt-in y no contamina la suite estándar. La suite normal sigue
pasando en los 6 motores como hasta hoy.

### Helpers compartidos

`bugbash/tools/` expone funciones reusadas. Reglas:

- **Cero lógica de negocio** ahí; sólo plumbing (docker boot, métricas
  scrape, pool check, coverage scan).
- Cada función tiene su test unitario en `bugbash/tools/*_test.go`.
- No importan `domain/` (los tools son agnósticos del dominio).

### Estado entre fases

Las fases son **acumulativas en datos**: F1 siembra el dominio mínimo y
todas las fases posteriores lo asumen. F4 añade el volumen de carga; el
resto se ejecuta sobre el dataset volumétrico. **No re-sembrar entre
fases consecutivas salvo que la fase lo declare**.

Mecanismo: el contenedor por motor levanta una vez al inicio de la
pasada (`/bugbash all`), persiste durante toda la pasada, se tira al
final. Una fase corrida sola (`/bugbash f05_multitenancy`) levanta el
contenedor, opcionalmente reusa un snapshot pre-sembrado de
`REPORTS/<runs anteriores>/datasets/` si existe, y ejecuta.

### Output de cada test

Cada test debe:

1. Capturar fallos con `t.Errorf` (no `t.Fatal` salvo bloqueante).
2. Adjuntar al fallo un payload JSON estructurado:
   ```go
   reporter.Fail(t, reporter.Failure{
       Phase:    "f02_api_surface",
       Test:     t.Name(),
       Engine:   engineName,
       Category: reporter.CategoryDialectSpecific,
       Severity: reporter.SeverityP1,
       Error:    err.Error(),
       Reproducer: reporter.Reproducer{
           Seed:     seed,
           Command:  "go test -tags=bugbash -run "+t.Name()+" ./phases/f02_api_surface/...",
           Files:    []string{"window.go:142", "cte.go:88"},
       },
   })
   ```
3. NO abortar la fase entera por un fallo aislado — la fase termina,
   reporta TODO, y deja que el reporter decida bloqueante vs no.

### Reproducibilidad

- Seed por defecto: `42`. Override con `--seed=N`.
- Cualquier `rand.*` debe usar `bugbash/seed.New(seed)` que devuelve
  una `*rand.Rand` determinista.
- Cualquier `time.Now()` que el test compare debe pasar por
  `bugbash/tools.Clock` (mockeable).
- Datasets de reproducer (en `REPORTS/<run>/datasets/`) son SQL `INSERT`
  textuales — un humano puede correrlos en el motor para reproducir el
  fallo a mano.

## Cómo Code añade una fase nueva

Si en algún momento surge una clase de bugs no cubierta por F0-F14, se
añade una fase nueva (`F15-name`):

1. Crear `bugbash/phases/f15_name/` con su `README.md` (objetivo,
   ejercita, verifica, criterio done) siguiendo el patrón de F0-F14.
2. Añadir entrada en [`docs/BUGBASH_PLAN.md`](../docs/BUGBASH_PLAN.md)
   §Fases.
3. Actualizar el slash command [`/bugbash`](../.claude/commands/bugbash.md)
   para que reconozca la nueva fase.
4. PR con `code-reviewer` + entrada en CHANGELOG bajo `### Tests`.

## Cómo Code mantiene el bug-bash vivo

- **Tras cada release minor** (`v1.x.0`): correr pasada completa. PRs
  derivados van a v1.x.1.
- **Tras cada release patch** (`v1.0.x`): F0+F1+F13 obligatorios.
- **Cuando docs-auditor detecte drift** entre código y `BUGBASH_PLAN.md`
  / `DOMAIN.md`: alinear en el mismo PR que la feature que lo causó.
- **Si el bug-bash empieza a tener flaky**: bug del bug-bash, no del
  producto — arreglarlo es prioridad. Cero tolerancia a flaky.
