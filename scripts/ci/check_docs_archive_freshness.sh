#!/usr/bin/env bash
# check_docs_archive_freshness.sh — el archivo de documentación no se queda atrás.
#
# El sitio publica la doc actual y, bajo su ruta de versión, los snapshots de
# las minors ya publicadas. Ese archivo se congeló sin que nadie lo notara: se
# fueron publicando minors sin cortar snapshot, así que el desplegable ofrecía
# versiones antiguas y saltaba directamente a la actual, con un hueco en medio
# del que la página no decía nada.
#
# La regla es mínima y verificable: el snapshot más reciente no puede quedar
# por debajo de la MINOR publicada. Un patch no exige snapshot nuevo (la doc de
# la minor sigue valiendo); una minor sí.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

VERSIONS_FILE="website/versions.json"
if [ ! -f "$VERSIONS_FILE" ]; then
  echo "OK: este sitio no versiona documentación (sin $VERSIONS_FILE)."
  exit 0
fi

python3 - <<'PY'
import json, sys

released = json.load(open(".release-please-manifest.json"))["."]
versions = json.load(open("website/versions.json"))

def minor(v):
    parts = v.split(".")
    return (int(parts[0]), int(parts[1]))

stable = [v for v in versions if v.count(".") == 2 and int(v.split(".")[0]) >= 1]
if not stable:
    print(f"FAIL: el archivo no contiene ningún snapshot de la línea estable, y la versión publicada es {released}.")
    print("Corta uno: bash scripts/release/cut_docs_snapshot.sh")
    sys.exit(1)

newest = max(stable, key=minor)
if minor(newest) < minor(released):
    print(f"FAIL: el archivo de documentación va por detrás de lo publicado.")
    print(f"  versión publicada:      {released}")
    print(f"  snapshot más reciente:  {newest}")
    print("")
    print("Una minor publicada sin snapshot deja al lector clavado en ella sin su")
    print("documentación. Corta el snapshot en el PR que precede al corte de la")
    print("release (el sitio sirve la doc del tag pinado, así que después ya no llega):")
    print("  bash scripts/release/cut_docs_snapshot.sh")
    sys.exit(1)

print(f"OK: el archivo cubre la línea publicada (publicado {released}, snapshot más reciente {newest})")
PY
