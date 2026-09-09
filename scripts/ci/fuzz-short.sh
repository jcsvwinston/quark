#!/usr/bin/env bash
# fuzz-short.sh — a short, seeded run of every native Go fuzz target, sized to
# stay inside the CI test job without turning it into a fuzzing lane.
#
# OpenSSF Scorecard scores Fuzzing 0/10 across the suite, and the surfaces this
# covers — the SQL guard, the struct-tag parser, the dialect escaping — all
# take untrusted input, so the targets are worth having for their own sake.
# What this script buys is regression pressure on EVERY pull request: each
# target's seed corpus (f.Add plus the files under testdata/fuzz/) runs on the
# normal `go test ./...` step, and this adds a few seconds of actual mutation
# per target on top.
#
# FUZZTIME is a wall-clock budget PER TARGET, so the lane's duration is bounded
# whatever the runner's throughput turns out to be. The default is what fits:
# five targets have to share one minute with their instrumented builds, so the
# suggested 20s per target does not, and 6s does with room to spare (about 45
# seconds all in). Six seconds is still millions of executions on the cheap
# targets. For a real hunt, raise it: FUZZTIME=2m make fuzz.
#
# The root package's target (FuzzDialectEscaping) is deliberately NOT in the
# timed list: instrumenting the root test binary recompiles the package under
# coverage, and the root package is the largest in the tree. That used to cost
# minutes because the test binary also linked every driver and the whole
# testcontainers tree; since ADR-0024 it does not, but the budget is still
# better spent on the four guard targets. Its seed corpus runs below instead —
# the mutation runs happen locally or on demand:
#
#   go test -run '^$' -fuzz='^FuzzDialectEscaping$' -fuzztime=2m .
#
# Usage: bash scripts/ci/fuzz-short.sh [fuzztime]
set -euo pipefail

FUZZTIME="${1:-${FUZZTIME:-6s}}"

# package:target — one `go test -fuzz` invocation each, since -fuzz takes a
# single target.
TIMED=(
  "./internal/guard:FuzzValidateIdentifier"
  "./internal/guard:FuzzValidateJSONPath"
  "./internal/guard:FuzzValidateJoinOn"
  "./internal/guard:FuzzValidateRawQuery"
  "./internal/schema:FuzzColumnNaming"
)

for entry in "${TIMED[@]}"; do
  pkg="${entry%%:*}"
  target="${entry##*:}"
  echo "--- fuzzing $target in $pkg for $FUZZTIME"
  go test "$pkg" -run '^$' -fuzz="^${target}\$" -fuzztime="$FUZZTIME" -count=1
done

echo "--- seed corpus only: FuzzDialectEscaping (see the note at the top)"
go test . -run '^FuzzDialectEscaping$' -count=1
