#!/usr/bin/env bash
# Copyright 2026 jcsvwinston
# SPDX-License-Identifier: Apache-2.0
#
# check_playground_weight.sh — the query playground's WebAssembly module
# stays within its download budget, and the page says how big it is.
#
# The playground was approved on the condition that its weight is measured
# and bounded. This script is that condition in CI:
#
#   1. It builds the module (scripts/ci/build_playground.sh) and prints the
#      raw and gzip sizes. gzip -9 is the measure, because that is what the
#      budget was set against (3 092 752 B on 2026-11-08, with one model
#      type instantiated; every further generic instantiation of
#      quark.Query costs about 130 KB compressed).
#   2. It FAILS when the gzip size exceeds BUDGET_GZIP_BYTES — about 13 %
#      over that first measurement, so a dependency that bloats the graph
#      trips it while a normal release does not.
#   3. It FAILS when the size written in website/docs/playground.mdx
#      (WASM_GZIP_BYTES, shown on the button that loads the module) differs
#      from the measured one by more than 10 %, so the page never states a
#      number that is no longer true.
#
# Usage: bash scripts/ci/check_playground_weight.sh
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

BUDGET_GZIP_BYTES=3500000
PAGE=website/docs/playground.mdx

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

bash scripts/ci/build_playground.sh "$TMP" >/dev/null

RAW=$(wc -c < "$TMP/quark.wasm" | tr -d ' ')
GZ=$(gzip -9 -c "$TMP/quark.wasm" | wc -c | tr -d ' ')
echo "playground wasm: raw=${RAW} B gzip=${GZ} B (budget ${BUDGET_GZIP_BYTES} B gzip)"

status=0
if [ "$GZ" -gt "$BUDGET_GZIP_BYTES" ]; then
  echo "FAIL: the compressed module (${GZ} B) exceeds the budget (${BUDGET_GZIP_BYTES} B)." >&2
  echo "      Find what grew (go tool nm -size, or bisect the imports) before raising the number." >&2
  status=1
fi

WRITTEN=$(grep -oE 'WASM_GZIP_BYTES = [0-9]+' "$PAGE" | grep -oE '[0-9]+' | head -1 || true)
if [ -z "$WRITTEN" ]; then
  echo "FAIL: $PAGE does not declare 'export const WASM_GZIP_BYTES = <bytes>;'" >&2
  status=1
else
  DIFF=$(( GZ > WRITTEN ? GZ - WRITTEN : WRITTEN - GZ ))
  # within 10 %: |measured - written| * 10 <= measured
  if [ $(( DIFF * 10 )) -gt "$GZ" ]; then
    echo "FAIL: $PAGE says the module weighs ${WRITTEN} B gzip; it weighs ${GZ} B (more than 10 % apart)." >&2
    echo "      Update WASM_GZIP_BYTES in the page to the measured value." >&2
    status=1
  else
    echo "page states ${WRITTEN} B gzip: within 10 % of the measurement"
  fi
fi

exit $status
