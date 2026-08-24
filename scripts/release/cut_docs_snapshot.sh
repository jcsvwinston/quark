#!/usr/bin/env bash
# cut_docs_snapshot.sh — congela la documentación actual como snapshot de una
# versión, para que el archivo publicado siga el ritmo de las releases.
#
# El sitio sirve SIEMPRE la doc actual, y bajo su ruta de versión los
# snapshots de las minors ya publicadas: es lo que permite a quien está
# clavado en una minor anterior leer la doc que le corresponde. Ese archivo
# se congeló en su día —se publicaron minors sin cortar snapshot— y el
# desfase no lo veía nadie; check_docs_archive_freshness.sh lo vigila ahora.
#
# Uso:
#   bash scripts/release/cut_docs_snapshot.sh            # versión del manifiesto
#   bash scripts/release/cut_docs_snapshot.sh 1.6.0      # explícita
#
# CUÁNDO: en el PR que precede al corte de una MINOR, para que el tag la
# contenga — el paraguas sirve la doc del TAG pinado, así que un snapshot
# añadido después no llega al sitio hasta la release siguiente.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

VERSION="${1:-$(python3 -c "import json;print(json.load(open('.release-please-manifest.json'))['.'])")}"

if ! printf '%s' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "FAIL: versión inválida '$VERSION' (se espera X.Y.Z)" >&2
  exit 1
fi

if [ -d "website/versioned_docs/version-$VERSION" ]; then
  echo "OK: el snapshot $VERSION ya existe; nada que hacer."
  exit 0
fi

echo "Cortando snapshot de documentación $VERSION…"
(cd website && npm run docusaurus -- docs:version "$VERSION")

echo
echo "Hecho. Revisa y commitea:"
echo "  website/versions.json"
echo "  website/versioned_docs/version-$VERSION/"
echo "  website/versioned_sidebars/version-$VERSION-sidebars.json"
