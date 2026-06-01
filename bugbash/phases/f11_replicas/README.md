# F11 — Replicas

> Spec: [`docs/BUGBASH_PLAN.md`](../../../docs/BUGBASH_PLAN.md) §F11.
> Superficie: `replicas.go` (`pickReplica`, estrategias, `Sticky`,
> `markReplicaDown` + cooldown), `query_exec.go` (`readExec` + retry-on-primary
> en error transitorio), `option.go` (`WithReplicas`, `WithReplicaStrategy`,
> `WithReplicaDownCooldown`). ADR-0015.

## Qué prueba

Read/write split + sticky reads + lecturas en tx + reparto por estrategia +
failover/cooldown de réplicas. **Sólo PostgreSQL** (motor del spec).

## Topología

La fase **levanta su propia topología** con Docker: 1 primary + 3 replicas, todas
instancias PG independientes (**sin replicación real**, puertos 55440-55443). El
harness de bug-bash (un contenedor por motor) no modela una flota, así que esta
fase orquesta la suya y la tira al final.

El routing se prueba por **presencia de dato**, no scrapeando la etiqueta OTel
`db.host` — es señal más fuerte: una fila escrita sólo en el primary no existe en
ninguna réplica, así que:

- una lectura **no-sticky** de esa fila devuelve 0 → la sirvió una réplica;
- una lectura **`Sticky`/en-tx** devuelve 1 → la sirvió el primary;
- con **todas las réplicas paradas**, una lectura no-sticky devuelve 1 → hizo
  **failover transparente al primary** (`markReplicaDown` + retry-on-primary).

## Grupos cubiertos (PostgreSQL)

- **ReadWriteSplit** — escribe `wX` al primary; una lectura no-sticky de `wX`
  devuelve 0 (la sirvió una réplica vacía). El write fue al primary, el read no.
- **StickyReadsPrimary** — `Sticky(ctx)` ancla la lectura al primary → ve `wX` (1).
- **TxReadsPrimary** — una lectura dentro de `client.Tx` usa la conexión de la tx
  (primary) → ve `wX` (1).
- **RoundRobinSpread** — siembra cada réplica con un conteo distinto (1/2/3 filas
  `rr`) y comprueba que las lecturas no-sticky observan **>1** conteo distinto →
  round-robin reparte (estrategia por defecto).
- **FailoverOneReplicaDown** — `docker stop` de una réplica; 20 lecturas
  no-sticky **siguen teniendo éxito** (la que toca a la caída hace failover al
  primary; el cooldown la saca de rotación).
- **FailoverAllReplicasDown** — `docker stop` de todas; una lectura no-sticky
  devuelve 1 → failover al primary, sin error.
- **PrimaryDownWritesFail** — `docker stop` del primary; un write **falla** (no
  hay failover primary→réplica por diseño, ADR-0015). Corre el último.

## Requisitos / fuera de scope

- **Requiere Docker.** Sin Docker, `TestReplicas` hace skip logueado — el run por
  defecto (`-engines=sqlite`) es un no-op limpio.
- **Sin replicación real ni replica-lag**: el modelo de presencia-de-dato no los
  necesita.
- **Aserción explícita de la métrica OTel `db.host`**: cubierta por proxy (la
  presencia de dato ya prueba el routing real).
- **Cooldown como invariante de tiempo** (no martillear una réplica caída): el
  failover verifica que las lecturas siguen teniendo éxito con réplicas caídas;
  el invariante temporal del cooldown lo cubren los unit tests de la raíz
  (`replicas_test.go`), no esta fase de integración.
- **Puertos fijos (55440-55443)**: no corras instancias **en paralelo** de esta
  fase — `runPG` hace `docker rm -f` por nombre antes de arrancar, así que dos
  runs concurrentes colisionarían. Los runs secuenciales (`-count=N`) van bien
  (teardown antes del siguiente boot).

## Cómo correr

```bash
cd bugbash
go test -tags=bugbash -run TestReplicas -v ./phases/f11_replicas/ -timeout 15m
```

## Hallazgos (en `TASKS.md` § "Bug-bash hallazgos")

**Sin hallazgos.** Pasada 2026-06-01 (Docker, topología 1 primary + 3 replicas
PG): 7/7 grupos verdes, flake-check 3× limpio, teardown sin contenedores
residuales. Read/write split, `Sticky`/tx→primary, reparto round-robin, failover
transparente (1 y todas las réplicas caídas) y primary-caído→writes-fallan,
todos sólidos. Fase test-only (no destapó bug → sin cambio de código).

## Criterio done

- [x] Read/write split: write→primary, read no-sticky→réplica.
- [x] `Sticky` y lecturas en tx → primary.
- [x] Reparto round-robin observable entre réplicas.
- [x] Failover transparente a primary con réplica(s) caída(s); cooldown.
- [x] Primary caído → writes fallan (sin failover primary→réplica).
