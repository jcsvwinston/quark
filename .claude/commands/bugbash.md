---
description: Ejecuta una pasada del bug-bash post-v1.0 (validación externa que V1_GATE.md §B Item 6 dejó pendiente). Corre fase(s) sobre los motores seleccionados, recolecta fallos en bugbash/REPORTS/, y delega al subagente bugbash-reporter para clasificar y abrir tareas. Por diseño, NO es parte del CI de PRs — es validación periódica intencional.
argument-hint: [fase | all] [--engines=<list>] [--seed=<n>] [--soak] [--report-only]
---

# /bugbash $ARGUMENTS

Ejecutas una pasada del bug-bash diseñado en
[`docs/BUGBASH_PLAN.md`](../../docs/BUGBASH_PLAN.md) sobre el dominio
descrito en [`bugbash/DOMAIN.md`](../../bugbash/DOMAIN.md). No es trabajo
de feature: es la pasada de validación externa que el §B Item 6 del
V1_GATE.md dejó pendiente y que post-v1.0 debe ejecutarse periódicamente
para detectar regresiones cross-engine y dialect-specific gaps que la
suite unitaria no atrapa.

> **Antes de invocar:** lee `docs/BUGBASH_PLAN.md` (filosofía y fases) y
> verifica que `bugbash/` está implementado al menos hasta la fase que
> vas a correr. Si la fase no existe, **párate y avisa al usuario** —
> no inventes una fase ad-hoc.

## Parsing del argumento

`$ARGUMENTS` puede contener:

| Token | Significado |
| --- | --- |
| `f00_install` ... `f14_soak` | Fase concreta (ver `bugbash/phases/`) |
| `all` | F0-F13 (sin soak) |
| `all-with-soak` | F0-F14 incluyendo soak overnight |
| `--engines=sqlite,postgres,…` | Lista de motores; default `all` (6) |
| `--seed=N` | Semilla para los generadores. Default 42 |
| `--soak` | Modifica F14 para correr 12h (default es 1h smoke) |
| `--report-only` | Sólo regenera el informe de la última pasada |

Si no se pasa fase, asumir `all` y avisar al usuario que va a ser
2-3 horas de wall-clock.

## Paso 0 — Pre-flight

\`\`\`bash
cd /Users/jcsv/GolandProjects/quark

# Estado limpio
git status --porcelain | grep -v "REPORTS/" && {
  echo "ERROR: working tree sucio (fuera de REPORTS/). Aborta."
  exit 1
}

# Verifica que el bug-bash está implementado
test -f bugbash/go.mod || {
  echo "ERROR: bugbash/go.mod no existe. La fase F0 de la implementación"
  echo "       del bug-bash sigue pendiente. Pídele al usuario que arranque"
  echo "       /next-session bugbash-impl o equivalente."
  exit 1
}

# Verifica que la fase pedida existe
test -d "bugbash/phases/$PHASE" || {
  echo "ERROR: fase $PHASE no implementada en bugbash/phases/."
  echo "       Fases disponibles: \$(ls bugbash/phases/)"
  exit 1
}

# Verifica que Docker está disponible (para PG/MySQL/MariaDB/MSSQL/Oracle)
docker info >/dev/null 2>&1 || {
  echo "ERROR: Docker no disponible. Sin Docker sólo puedes correr"
  echo "       --engines=sqlite."
  [ "$ENGINES" != "sqlite" ] && exit 1
}
\`\`\`

## Paso 1 — Boot de contenedores

\`bugbash/tools/docker.go\` expone \`Up(engines []string)\` y \`Down()\`.
Sigue el mismo patrón que el job de CI de Oracle (\`docker run\` directo,
no testcontainers — ADR-0018 / PR #127). Para cada motor en
\`--engines\`:

\`\`\`bash
# Postgres
docker run -d --name bugbash-postgres -p 5432:5432 \
  -e POSTGRES_PASSWORD=quark postgres:16-alpine

# MySQL
docker run -d --name bugbash-mysql -p 3306:3306 \
  -e MYSQL_ROOT_PASSWORD=quark mysql:8

# MariaDB
docker run -d --name bugbash-mariadb -p 3307:3306 \
  -e MARIADB_ROOT_PASSWORD=quark mariadb:11

# MSSQL
docker run -d --name bugbash-mssql -p 1433:1433 \
  -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD=Quark!2026 \
  mcr.microsoft.com/mssql/server:2022-latest

# Oracle (mismo patrón que CI ADR-0018)
docker run -d --name bugbash-oracle -p 1521:1521 \
  gvenzl/oracle-free:23-slim
# luego el GRANT EXECUTE ON DBMS_LOCK vía docker exec -i sysdba
\`\`\`

DSNs se exportan a env vars que las fases leen
(\`BUGBASH_DSN_POSTGRES\`, etc.). El script captura el caso de
contenedores ya arrancados (reusa si están con el mismo nombre).

**Si la pasada falla en este paso**: stop. No es bug de Quark; es bug
de entorno. El user lo verá.

## Paso 2 — Seed (sólo si la fase lo requiere)

F1+ requieren dominio sembrado. Si los datos no existen en el motor
(consulta a \`organizations\` table):

\`\`\`bash
go run ./bugbash/seed/ \
  --engines=\$ENGINES \
  --seed=\$SEED \
  --scale=\$([ "\$PHASE" = "f04_volume" ] && echo "full" || echo "small")
\`\`\`

\`scale=small\` siembra cardinalidades de F1-F3 (~10k filas totales).
\`scale=full\` siembra las cardinalidades de F4 (~25M filas; tarda 30-45
min en motor lento).

## Paso 3 — Correr la fase

\`\`\`bash
mkdir -p bugbash/REPORTS/run-\$(date +%Y%m%d-%H%M)
export BUGBASH_REPORT_DIR=bugbash/REPORTS/run-\$(date +%Y%m%d-%H%M)

cd bugbash
go test -tags=bugbash \
  -timeout 60m \
  -count=1 \
  -engines=\$ENGINES \
  -seed=\$SEED \
  -v \
  ./phases/\$PHASE/... \
  2>&1 | tee \$BUGBASH_REPORT_DIR/raw.log
\`\`\`

Si la fase tarda más de 60 min (típico de F4 volume + F14 soak),
ajustar \`-timeout\` acordemente. F14 con \`--soak\`: \`-timeout=14h\`.

Cada test individual escribe su \`failure.json\` en
\`\$BUGBASH_REPORT_DIR/per-engine/<engine>/\`. La fase entera escribe
métricas OTel snapshot en \`\$BUGBASH_REPORT_DIR/metrics/\`.

## Paso 4 — Delegar al subagente bugbash-reporter

Tras la fase, **siempre** delegar al subagente
[\`bugbash-reporter\`](../agents/bugbash-reporter.md) con la ruta del
report:

\`\`\`
Invoca al subagente bugbash-reporter con
BUGBASH_REPORT_DIR=\$BUGBASH_REPORT_DIR. Pídele:
1. Clasificar los fallos por categoría y severidad.
2. Generar summary.md humano.
3. Para cada fallo P0/P1, decidir si va a TASKS.md o a issue GH
   (preguntar al usuario si gh está disponible).
4. Verificar que ningún fallo es flaky (re-correr 3× los marcados como
   ambiguos).
\`\`\`

El subagente devuelve un resumen y la lista de tareas creadas.

## Paso 5 — Decisión de bloqueo

Tras el report:

- **P0 abierto**: bloquea el próximo release patch. Insertar en
  \`TASKS.md\` § "Bugs P0" en lugar de § "Bug-bash hallazgos".
- **P1 abierto sin issue GH**: añadir entrada a \`TASKS.md\` § "Bug-bash
  hallazgos" para que aparezca en \`/next-session auto\`.
- **Sólo P2/P3**: registrar y seguir; no bloquea nada.

## Paso 6 — Cleanup

\`\`\`bash
# Tirar contenedores (opcional si vas a seguir)
docker rm -f bugbash-postgres bugbash-mysql bugbash-mariadb \
            bugbash-mssql bugbash-oracle

# Pero NO borres bugbash/REPORTS/ — son trazabilidad.
\`\`\`

## Plantilla de cierre

Al final, comunica al usuario:

\`\`\`
Bug-bash <fase|all> completado — run-<timestamp>

Motores corridos: <list>
Tests totales: <N>
Pass: <N>  Fail: <N>

Fallos por severidad:
- P0: <N> (BLOQUEANTE)
- P1: <N>
- P2: <N>
- P3: <N>

Por categoría:
- regression: <N>
- dialect-specific: <N>
- gap: <N>
- doc-drift: <N>
- test-only: <N>

Tareas abiertas:
- TASKS.md: <N> entradas nuevas en § "Bug-bash hallazgos"
- Issues GH: <N>

Report completo: bugbash/REPORTS/run-<timestamp>/summary.md

Próximo /next-session sugerido:
- Si P0: "bugbash-p0-<id>" (foco en el P0 más grave)
- Si sólo P1+: "v1.2" o lo que toque por roadmap; el bug-bash
  no bloquea pero conviene atajar P1 en el patch release siguiente.
\`\`\`

## Modos especiales

### --report-only

Salta los pasos 0-3. Sólo re-genera summary.md sobre el último
\`bugbash/REPORTS/run-*\` existente. Útil si quieres re-clasificar tras
mejorar la lógica del reporter.

### --soak

Sólo aplicable a \`f14_soak\`. Cambia el timeout default (1h) a 12h.
Implica que el comando va a estar corriendo overnight; el script
escribe un PID file y sobrevive a \`nohup\`.

## Anti-patterns que evitar

- **No abortar la pasada al primer fallo.** Una fase debe correr hasta
  el final aunque el primer test falle. El valor del bug-bash es la
  **agregación** — un solo fallo aislado dice poco; ver el patrón
  cross-engine sí.
- **No correr el bug-bash en PRs.** Es opt-in periódico, no parte del
  CI. Su valor está en la pasada larga + agregación; convertirlo en
  gate de PR rompe el flujo de desarrollo sin mejorar la calidad.
- **No "arreglar" un fallo del bug-bash quitándolo del bug-bash.**
  Si un test del bug-bash es incorrecto, se cambia el test con razón
  documentada en el PR — pero ese tipo de cambio lo aprueba \`code-reviewer\`
  con mirada extra.

---

**Razón de existir de este comando:** post-v1.0 el riesgo no son los
bugs P0 (esos están cerrados y la suite los previene), es la
**acumulación silenciosa** de regresiones cross-engine y dialect-specific
gaps a medida que la API crece. El bug-bash es la red de seguridad
periódica que captura esos antes de que un usuario externo los reporte
como issue. Ejecutado cada release minor + nightly de F1+F2, mantiene
v1.0.x sólido.
