#!/usr/bin/env bash
# check_versioned_docs_markers.sh — un snapshot anuncia SU propia versión.
#
# El marcador `x-release-please-version` lo sube release-please en la rama del
# release. Un snapshot cortado ANTES de ese bump congela la versión anterior y
# la publica como si fuera la suya: el archivo de 1.6.0 decía «Quark is
# v1.5.2», y en la página que el sitio sirve bajo esa versión. Sólo se ve desde
# fuera, porque la copia es coherente CONSIGO MISMA — ningún otro guard la mira.
#
# Aquí el snapshot se corta EN la rama del release, donde el marcador ya está
# subido, así que sale bien por construcción; esto vigila que siga siendo
# verdad, incluida la doc ya archivada.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

[ -d website/versioned_docs ] || { echo "OK: este sitio no versiona documentación."; exit 0; }

status=0
for dir in website/versioned_docs/version-*; do
  version="${dir##*/version-}"
  while IFS= read -r file; do
    while IFS= read -r line; do
      claimed=$(printf '%s\n' "$line" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)
      if [ -z "$claimed" ]; then
        echo "FAIL: $file: la línea del marcador no lleva versión: $line" >&2
        status=1
      elif [ "$claimed" != "v$version" ]; then
        echo "FAIL: $file afirma $claimed, pero es el snapshot de v$version — la doc archivada de una versión no puede anunciar otra" >&2
        status=1
      fi
    done < <(grep "x-release-please-version" "$file")
  done < <(grep -rl "x-release-please-version" "$dir" 2>/dev/null || true)
done

[ $status -eq 0 ] && echo "OK: cada snapshot de documentación anuncia su propia versión ($(ls -d website/versioned_docs/version-* | wc -l | tr -d ' ') snapshots)"
exit $status
