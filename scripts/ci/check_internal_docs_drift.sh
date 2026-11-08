#!/usr/bin/env bash
# check_internal_docs_drift.sh — la documentación INTERNA no cita ficheros que
# no existen.
#
# El sitio público tiene sus guards (lint-docs, voz de producto), pero `docs/**`
# no tenía ninguno. En el repo hermano esa ausencia dejó un manual describiendo
# durante catorce meses un subsistema ya extraído, con un enlace a un fichero
# que nunca existió: nadie lo vio porque nada lo miraba.
#
# Este guard comprueba lo verificable por máquina sin adivinar intenciones:
# cada ruta que la documentación interna cita como si existiera —enlaces
# markdown relativos, y rutas de fichero entre comillas invertidas— resuelve
# en el árbol. No juzga si el texto es cierto; solo que aquello a lo que
# apunta está ahí.
#
# Alcance: la documentación VIVA. Quedan fuera a propósito los registros
# históricos —`adr/` (decisiones fechadas), y por nombre las notas de versión
# (`RELEASE_NOTES_v*.md`), las guías de migración (`MIGRATION_v*.md`) y las
# auditorías/planes ya cerrados—: son actas de lo que era cierto entonces, y
# "corregirlas" sería falsificar el registro.
#
# Dos reglas más, nacidas de derivas que este guard no vio (QK-73, QK-77):
#
# - `docs/deprecations/**` es ESTRICTO: un aviso de deprecación cita ESTE repo
#   por contrato (el test que cubre el alias, el manifiesto que lo justifica),
#   así que ahí toda ruta entre comillas invertidas con extensión de fichero
#   —con directorio o sin él— debe existir desde la raíz. Dos avisos citaron
#   durante dos meses `tenant_router_test.go` y `examples/superapp/…`, que no
#   existían, y la regla general no los miró: uno no llevaba directorio y el
#   otro empezaba por un directorio que no es de este repo.
# - `acceptance/*.md` entra en el barrido por defecto (README, COVERAGE,
#   HANDOFF del arnés): hasta ahora nada los leía.
#
# Uso: bash scripts/ci/check_internal_docs_drift.sh [dir]   (default: docs acceptance)
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$ROOT" || exit 1

if [ $# -gt 0 ]; then
  DOCS_DIRS="$*"
else
  DOCS_DIRS="docs acceptance"
fi

for d in $DOCS_DIRS; do
  if [ ! -d "$d" ]; then
    echo "FAIL: no existe $d"
    exit 1
  fi
done

DOCS_DIRS="$DOCS_DIRS" python3 - <<'PY'
import os
import re
import sys

docs_dirs = os.environ["DOCS_DIRS"].split()

# 1) Enlaces markdown relativos: [texto](ruta.md) / (ruta.md#ancla).
#    Se buscan fuera de los bloques de código: un enlace no puede vivir en un
#    fence, y el código Go con genéricos —`gorm.G[User](db)`— tiene
#    exactamente la forma de un enlace a un fichero llamado `db`.
link_re = re.compile(r'\[[^\]]*\]\((?!https?://|mailto:|#)([^)\s]+)\)')
fence_re = re.compile(r'^```.*?^```[ \t]*$', re.MULTILINE | re.DOTALL)
# 2) Rutas de fichero en `código`, restringidas a los directorios de PRIMER
#    NIVEL de este repo. La restricción no es cosmética: buena parte de la
#    documentación describe el árbol del LECTOR (`handlers/`, `models/`,
#    `config/rbac_policy.csv` son rutas de su proyecto, no del nuestro), y sin
#    ella el guard perseguiría ficheros que nunca debieron existir aquí.
REPO_DIRS = ("internal/", "cmd/", "scripts/", "docs/", "website/",
             "acceptance/", "migrate/", "quarkmigrate/", "quarktenant/",
             "quarktest/", "cache/", "otel/", ".github/")
path_re = re.compile(r'`((?:[\w.-]+/)+[\w.-]+\.(?:go|md|yml|yaml|json|sql|sh|ts|tsx|csv|txt))`')
# 3) Regla estricta (docs/deprecations/**): cualquier fichero citado, con
#    directorio o sin él, debe existir desde la raíz del repo.
strict_path_re = re.compile(r'`((?:[\w.-]+/)*[\w.-]+\.(?:go|md|mdx|yml|yaml|json|sql|sh|ts|tsx|csv|txt))`')
STRICT_DIRS = {"deprecations"}
# Una ruta calificada con el nombre de otro repo de la suite (el contrato del
# 2.0 vive en `quantum/docs/planes/quantum-2.0.md`) no es de este árbol: la
# regla estricta la deja pasar en vez de exigir un fichero que nunca estará aquí.
SIBLING_REPOS = ("quantum/", "nucleus/", "orbit/")

# Escape por página, para las que documentan de arriba abajo el proyecto del
# lector (el layout generado, el tutorial): ahí toda ruta es ajena.
ESCAPE = "<!-- docs-drift-allow-external-paths -->"

broken = []
scanned = 0

# Directorios de registro histórico: ver la cabecera del script.
HISTORICAL = {"adr", "assets", "benchmarks", "playbooks"}
HISTORICAL_FILES = re.compile(r'^(RELEASE_NOTES_v|MIGRATION_v|AUDITORIA_|BUGBASH_|ANALISIS_)')

def walk_all():
    for docs_dir in docs_dirs:
        for dirpath, dirnames, filenames in os.walk(docs_dir):
            if os.path.dirname(dirpath) == docs_dir or dirpath == docs_dir:
                dirnames[:] = [d for d in dirnames if d not in HISTORICAL]
            yield dirpath, filenames

for dirpath, filenames in walk_all():
    for filename in filenames:
        if not filename.endswith(".md"):
            continue
        if HISTORICAL_FILES.match(filename):
            continue
        scanned += 1
        page = os.path.join(dirpath, filename)
        text = open(page, encoding="utf-8", errors="ignore").read()

        for target in link_re.findall(fence_re.sub("", text)):
            target = target.split("#", 1)[0]
            if not target:
                continue  # enlace a un ancla de la propia página
            resolved = os.path.normpath(os.path.join(dirpath, target))
            if not os.path.exists(resolved):
                broken.append((page, target, "enlace"))

        if ESCAPE in text:
            continue
        if os.path.basename(dirpath) in STRICT_DIRS:
            for target in strict_path_re.findall(text):
                if target.startswith(SIBLING_REPOS):
                    continue
                if not os.path.exists(os.path.normpath(target)):
                    broken.append((page, target, "ruta citada (regla estricta de deprecations)"))
            continue
        for target in path_re.findall(text):
            # Las rutas en código se citan desde la raíz del repo.
            if not target.startswith(REPO_DIRS):
                continue
            if not os.path.exists(os.path.normpath(target)):
                broken.append((page, target, "ruta citada"))

if broken:
    print(f"FAIL: {len(broken)} referencia(s) rotas en la documentación interna ({scanned} páginas escaneadas):")
    for page, target, kind in sorted(set(broken)):
        print(f"  {page}: {kind} {target!r} no existe")
    print("")
    print("Corrige la ruta, o borra el párrafo si describe algo que ya no existe:")
    print("una referencia a un fichero ausente suele señalar texto que quedó atrás.")
    sys.exit(1)

print(f"OK: la documentación interna no cita ficheros ausentes ({scanned} páginas escaneadas)")
PY
