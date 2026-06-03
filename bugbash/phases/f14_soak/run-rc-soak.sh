#!/usr/bin/env bash
# Copyright 2026 jcsvwinston
# SPDX-License-Identifier: Apache-2.0
#
# run-rc-soak.sh — launch the F14 release-candidate soak across ALL SIX engines.
#
# Why this script exists: the spec's F14 criterion is "12h × 6 motores"
# (SQLite + PostgreSQL + MySQL + MariaDB + SQL Server + Oracle). Launching the
# soak by hand is error-prone — it's easy to forget an engine (Oracle and
# SQLite were both missed in a manual launch). This script bakes in all six so
# none is ever left out. It also detaches each job with nohup so the run
# SURVIVES closing the terminal / the agent session (background `go test` jobs
# do NOT survive a session boundary — that is what killed the first attempt).
#
# Usage (run from anywhere; the script cd's to the bugbash module):
#   ./run-rc-soak.sh                 # launch the 12h × 6-engine soak, detached
#   ./run-rc-soak.sh watch           # one-shot progress check
#   ./run-rc-soak.sh collect         # print per-engine result + findings count
#   ./run-rc-soak.sh stop            # kill running soak jobs + remove containers
#
# Env overrides:
#   SOAK_SECONDS   per-engine duration (default 43200 = 12h; use e.g. 1800 for a 30m smoke)
#   SOAK_WORKERS   concurrent workers per engine (default 8)
#   BUGBASH_DSN_ORACLE   reuse an existing Oracle instead of booting bugbash-oracle
#                        (set this if your local Oracle already owns port 1521)
#
# After a clean run every engine's REPORTS/run-rc-soak-<engine>/out.log ends in
# `ok ... f14_soak` with `findings: 0`. Then: /release v1.1.0.

set -u
cd "$(dirname "$0")/../.." || exit 1   # -> bugbash module root

ENGINES=(sqlite postgres mysql mariadb mssql oracle)
SOAK_SECONDS="${SOAK_SECONDS:-43200}"
SOAK_WORKERS="${SOAK_WORKERS:-8}"
PG_ALT_DSN="postgres://postgres:quark@localhost:5433/postgres?sslmode=disable"

launch() {
  command -v docker >/dev/null && docker info >/dev/null 2>&1 || {
    echo "ERROR: Docker is not running — start Docker Desktop first." >&2; exit 1; }

  # Postgres needs its own container on 5433 (5432 collides with other projects).
  # MySQL/MariaDB/MSSQL/Oracle are auto-booted by the harness (tools.Up) on first
  # use; Oracle takes ~5 min and gets its DBMS_LOCK grant automatically.
  docker rm -f bugbash-postgres-alt >/dev/null 2>&1
  docker run -d --name bugbash-postgres-alt -e POSTGRES_PASSWORD=quark \
    -p 5433:5432 postgres:16-alpine >/dev/null || {
    echo "ERROR: could not start bugbash-postgres-alt (port 5433 in use?)" >&2; exit 1; }

  local timeout_h
  timeout_h=$(( SOAK_SECONDS / 3600 + 2 ))h   # test timeout = soak + 2h headroom

  echo "Launching F14 RC soak: ${SOAK_SECONDS}s/engine, ${SOAK_WORKERS} workers, timeout ${timeout_h}, engines: ${ENGINES[*]}"
  for E in "${ENGINES[@]}"; do
    local D="REPORTS/run-rc-soak-$E"; mkdir -p "$D"
    env BUGBASH_REPORT_DIR="$PWD/$D" \
        BUGBASH_DSN_POSTGRES="$PG_ALT_DSN" \
        ${BUGBASH_DSN_ORACLE:+BUGBASH_DSN_ORACLE="$BUGBASH_DSN_ORACLE"} \
        nohup go test -tags=bugbash -run TestSoak ./phases/f14_soak/ \
          -engines="$E" -soak-seconds="$SOAK_SECONDS" -soak-workers="$SOAK_WORKERS" \
          -timeout "$timeout_h" > "$D/out.log" 2>&1 &
    disown
    echo "  launched $E (pid $!)"
  done
  echo "All ${#ENGINES[@]} engines launched detached. Run './run-rc-soak.sh watch' to check progress."
}

watch() {
  echo "soak processes alive: $(pgrep -fc f14_soak.test 2>/dev/null || echo 0)/${#ENGINES[@]}"
  # Liveness signal = soak_txns growing (t.Logf output is buffered until the end).
  docker exec bugbash-postgres-alt psql -U postgres -tc \
    "select 'pg soak_txns='||count(*) from soak_txns" 2>/dev/null || echo "(pg not queryable yet)"
}

collect() {
  for E in "${ENGINES[@]}"; do
    local D="REPORTS/run-rc-soak-$E"
    echo "=== $E ==="
    tail -4 "$D/out.log" 2>/dev/null || echo "(no log)"
    echo "findings: $(wc -l < "$D/failures.jsonl" 2>/dev/null || echo 0)"
  done
}

stop() {
  pkill -f f14_soak.test 2>/dev/null && echo "killed soak jobs" || echo "no soak jobs running"
  docker rm -f bugbash-postgres-alt bugbash-mysql bugbash-mariadb bugbash-mssql bugbash-oracle >/dev/null 2>&1
  echo "removed soak containers (a user-managed Oracle via BUGBASH_DSN_ORACLE is left alone)"
}

case "${1:-launch}" in
  launch) launch ;;
  watch)  watch ;;
  collect) collect ;;
  stop)   stop ;;
  *) echo "usage: $0 [launch|watch|collect|stop]" >&2; exit 2 ;;
esac
