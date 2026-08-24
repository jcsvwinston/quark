#!/usr/bin/env bash
# gen_release_notes_skeleton.sh — escribe los esqueletos de release notes que
# el checklist de release exige, a partir de lo que release-please ya generó.
#
# Idempotente: si la sección/fichero de la versión del manifest ya existen, no
# toca nada. Pensado para correr EN la rama del release PR (donde el manifest
# ya apunta a la versión nueva y el CHANGELOG ya tiene su entrada):
#
#   bash scripts/release/gen_release_notes_skeleton.sh
#
# Escribe:
#   - la sección '## vX.Y.Z' en website/docs/reference/release-notes.mdx
#     (esqueleto con los bullets del CHANGELOG; la prosa se edita a mano —
#     la regla anti-hype no se delega en un script)
#   - docs/RELEASE_NOTES_vX.Y.Z.md (mismo esqueleto)
#
# Las MENCIONES de versión (README/SECURITY/CLAUDE/cabecera del sitio) las
# bumpa release-please solo, vía extra-files + x-release-please-version.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

version=$(python3 -c "import json;print(json.load(open('.release-please-manifest.json'))['.'])")
tag="v${version}"
today=$(date +%Y-%m-%d)
site="website/docs/reference/release-notes.mdx"
notes="docs/RELEASE_NOTES_${tag}.md"

# Extrae la entrada del CHANGELOG para esta versión (entre su cabecera y la
# siguiente '## [').
changelog_body=$(awk -v ver="$version" '
  $0 ~ "^## \\[" ver "\\]" {inside=1; next}
  inside && /^## \[/ {exit}
  inside {print}
' CHANGELOG.md)

if [ -z "$changelog_body" ]; then
  echo "ERROR: CHANGELOG.md no tiene entrada para ${version} — corre esto en la rama del release PR." >&2
  exit 1
fi

if ! grep -q "^## ${tag}\$" "$site"; then
  python3 - "$site" "$tag" "$today" <<'PY'
import sys, re
site, tag, today = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(site).read()
m = re.search(r'^## v\d', s, re.M)
skeleton = f"""## {tag}

<!-- TODO: una frase honesta de qué es este release (patch/minor y de qué). -->

### Changed

<!-- TODO: redacta desde el CHANGELOG (bullets abajo son el material crudo,
     no la prosa final): -->

"""
s = s[:m.start()] + skeleton + s[m.start():]
open(site, 'w').write(s)
PY
  # Añade los bullets del CHANGELOG como material crudo comentado
  python3 - "$site" "$tag" <<PY
import sys
site, tag = sys.argv[1], sys.argv[2]
raw = '''${changelog_body}'''
s = open(site).read()
s = s.replace('no la prosa final): -->\n', 'no la prosa final): -->\n\n<!--\n' + raw.strip() + '\n-->\n', 1)
open(site, 'w').write(s)
PY
  echo "escrito: sección ${tag} (esqueleto) en ${site}"
else
  echo "ok: ${site} ya tiene la sección ${tag}"
fi

if [ ! -f "$notes" ]; then
  {
    echo "# Release notes — ${tag}"
    echo
    echo "<!-- TODO: una frase honesta de qué es este release. -->"
    echo
    echo "Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>"
    echo
    echo "## Changed"
    echo
    echo "<!-- Material crudo del CHANGELOG; redacta la prosa final: -->"
    echo "<!--"
    echo "$changelog_body" | sed 's/^/  /'
    echo "-->"
  } > "$notes"
  echo "escrito: ${notes} (esqueleto)"
else
  echo "ok: ${notes} ya existe"
fi

# CLAUDE.md queda FUERA de los extra-files de release-please a propósito: su
# updater genérico reemplaza todas las apariciones de la versión anterior en
# el fichero, y este cita versiones pasadas en su línea de historial — la
# reescribía, falseando el registro. Aquí se bumpa solo la línea marcada.
if grep -q 'x-release-please-version' CLAUDE.md 2>/dev/null; then
  python3 - "$version" <<'PY_BUMP'
import re, sys
version = sys.argv[1]
lines = open("CLAUDE.md").read().split("\n")
for i, line in enumerate(lines):
    if "x-release-please-version" in line:
        lines[i] = re.sub(r"v\d+\.\d+\.\d+", f"v{version}", line, count=1)
        break
open("CLAUDE.md", "w").write("\n".join(lines))
print(f"bumpeada la línea marcada de CLAUDE.md a v{version}")
PY_BUMP
fi

echo "Recuerda: la prosa final es tuya. El guard de coherencia valida menciones y sección."
