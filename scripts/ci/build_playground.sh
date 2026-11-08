#!/usr/bin/env bash
# Copyright 2026 jcsvwinston
# SPDX-License-Identifier: Apache-2.0
#
# build_playground.sh — builds the documentation site's query playground:
# internal/playground compiled to WebAssembly, plus the wasm_exec.js shim
# from the Go toolchain that the page loads before it.
#
# Usage: bash scripts/ci/build_playground.sh <out-dir>
#
# Writes <out-dir>/quark.wasm and <out-dir>/wasm_exec.js. The page
# (website/docs/playground.mdx) fetches both from /quark-playground/ under
# the site's base URL, so a site build passes its static/quark-playground
# directory here. In this repository that directory is build output and is
# gitignored; the published site runs this script from its own build.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

OUT=${1:?usage: build_playground.sh <out-dir>}
mkdir -p "$OUT"

GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w' -o "$OUT/quark.wasm" ./internal/playground

EXEC_JS="$(go env GOROOT)/lib/wasm/wasm_exec.js"
if [ ! -f "$EXEC_JS" ]; then
  echo "build_playground: $EXEC_JS not found; this toolchain ships wasm_exec.js elsewhere" >&2
  exit 1
fi
cp "$EXEC_JS" "$OUT/wasm_exec.js"

echo "build_playground: wrote $OUT/quark.wasm ($(wc -c < "$OUT/quark.wasm" | tr -d ' ') B) and $OUT/wasm_exec.js"
