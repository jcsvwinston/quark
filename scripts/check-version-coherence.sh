#!/usr/bin/env bash
# Version-coherence guard (H-Q6). At v1.2.0 the release went out while
# README/SECURITY/CLAUDE.md/release-notes still said v1.1.5/v1.1.2/v1.1.0 —
# nothing forced the docs to move with the tag. This check makes the
# release-please PR (which bumps .release-please-manifest.json) fail until
# the user-facing version mentions are bumped in the same PR, and it keeps
# them from drifting afterwards.
set -euo pipefail

cd "$(dirname "$0")/.."

version=$(sed -nE 's/.*"\.": *"([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' .release-please-manifest.json)
if [ -z "$version" ]; then
  echo "could not read the version from .release-please-manifest.json" >&2
  exit 1
fi

fail=0

require_mention() {
  local file=$1
  if ! grep -q "v${version}" "$file"; then
    echo "ERROR: ${file} does not mention v${version} (the version in .release-please-manifest.json)" >&2
    fail=1
  fi
}

# The user-facing files that state "Quark is vX.Y.Z".
require_mention README.md
require_mention SECURITY.md
require_mention CLAUDE.md
require_mention website/docs/reference/release-notes.mdx

# Guard de heading en release-notes (NU7-1 aplicado a quark). La página usa
# el patrón «versión actual arriba + una sección '## vX.Y.Z' por release»;
# el require_mention de arriba solo exige que la cadena v${version} aparezca
# EN ALGUNA PARTE, así que el estado «release-please subió el manifest pero
# nadie escribió la sección» pasaba el guard con la mención del índice.
# Exigimos el heading de la versión del manifest (con o sin título detrás).
release_notes="website/docs/reference/release-notes.mdx"
if ! grep -qE "^## v${version//./\\.}( |$)" "$release_notes"; then
  echo "ERROR: ${release_notes} no tiene una sección '## v${version}' (la versión del manifest necesita sus release notes)" >&2
  fail=1
fi

# Narrative release notes must exist for the current minor.
minor_notes="docs/RELEASE_NOTES_v${version%.*}.0.md"
if [ ! -f "$minor_notes" ]; then
  echo "ERROR: ${minor_notes} does not exist (narrative notes for the current minor)" >&2
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo >&2
  echo "Release checklist: bump the version mentions above and add the minor's RELEASE_NOTES file." >&2
  exit 1
fi

echo "version coherence OK: v${version} mentioned in README/SECURITY/CLAUDE/release-notes, '## v${version}' section present, ${minor_notes} present"

# ---------------------------------------------------------------------------
# Roadmap sin versiones (QK6-5). QK5-2 quitó la versión hardcodeada del
# roadmap pero no dejó guard de reincidencia: si alguien vuelve a escribir
# «Quark is vX.Y.Z» ahí, nada lo cazaría hasta la siguiente auditoría.
# ---------------------------------------------------------------------------
if grep -nE 'v[0-9]+\.[0-9]+\.[0-9]+' website/docs/reference/roadmap.mdx; then
  echo "ERROR: website/docs/reference/roadmap.mdx contiene una versión hardcodeada (las versiones viven en release-notes, que sí tiene guard)" >&2
  fail=1
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "roadmap OK: sin versiones hardcodeadas"

# ---------------------------------------------------------------------------
# SECURITY.md sin versiones fósiles como soportadas (DI-2). Con v1.7.1
# publicada, la tabla de «Supported Versions» seguía diciendo v1.2.x/v1.1.x:
# la política («latest two tagged minors») era correcta, pero los números
# llevaban cinco minors congelados y el require_mention de arriba no los ve
# (solo exige que la versión ACTUAL aparezca, no que las viejas desaparezcan).
# Falla si alguna versión anterior a (minor actual − 1) figura en una fila
# marcada como soportada (✅).
# ---------------------------------------------------------------------------
cur_major=${version%%.*}
minor_rest=${version#*.}
cur_minor=${minor_rest%%.*}
stale=0
while IFS= read -r line; do
  case "$line" in
    *"✅"*) ;;
    *) continue ;;
  esac
  for v in $(printf '%s\n' "$line" | grep -oE 'v[0-9]+\.[0-9]+' || true); do
    v_major=${v#v}; v_major=${v_major%%.*}
    v_minor=${v##*.}
    if [ "$v_major" -lt "$cur_major" ] || { [ "$v_major" -eq "$cur_major" ] && [ "$v_minor" -lt $((cur_minor - 1)) ]; }; then
      echo "ERROR: SECURITY.md marca ${v}.x como soportada, pero la versión actual es v${version} — la política cubre solo los dos últimos minors taggeados" >&2
      stale=1
    fi
  done
done < SECURITY.md
if [ "$stale" -ne 0 ]; then
  exit 1
fi
echo "SECURITY.md OK: ninguna versión anterior a v${cur_major}.$((cur_minor - 1)) figura como soportada"
