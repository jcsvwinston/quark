#!/usr/bin/env bash
# Copyright 2026 jcsvwinston
# SPDX-License-Identifier: Apache-2.0
#
# run.sh — the engine bench, measured the way its page says it is.
#
#   bash benchmarks/engines/run.sh [OUT_DIR [TEST_FLAGS...]]
#
# With no TEST_FLAGS it runs TestEngineBench, the bench. Anything after OUT_DIR
# replaces that and goes to the test binary as it is, which is how one arm is
# profiled under the same conditions the bench measures it in:
#
#   bash benchmarks/engines/run.sh out -test.run '^$' \
#     -test.bench 'Postgres/List100/quark$' -test.cpuprofile /out/cpu.out
#   go tool pprof -top out/cpu.out
#
# (OUT_DIR is /out inside the container.)
#
# Starts PostgreSQL 16 and MySQL 8.4 in ONE network namespace, builds the
# bench's test binary for Linux, and runs it INSIDE that namespace, so every
# connection the bench opens is loopback. That is deliberate. On macOS a
# published port crosses Docker Desktop's VM and its userland proxy: when this
# was measured, a FindByPK took 120–380 µs through the port and 48–60 µs from
# inside the namespace, so the proxy was most of the time and most of the
# scatter, and quark's overhead was noise under it.
# Loopback leaves the CPU in charge: it is the strictest place to measure an
# ORM, because nothing hides its cost. The same script runs in CI, where the
# runner is Linux and the namespace trick costs nothing.
#
# MySQL runs with innodb_autoinc_lock_mode=1 instead of MySQL 8's default 2:
# it is the setting under which MySQL documents that the keys of a multi-row
# INSERT are consecutive, so CreateBatch sends one INSERT per chunk and MY-03
# measures that form (under 2 it sends one INSERT per row, which MY-03's
# informational arm measures). The setting touches inserts into an
# AUTO_INCREMENT column only; MY-01 and MY-02 read.
#
# PostgreSQL runs with synchronous_commit=off and both engines keep their data
# on tmpfs. With durable commits, InsertOne measured between 150 and 600 µs on
# the same machine from one sample to the next — the disk's flush, not the
# code — and no ratio survives that.
#
# Writes OUT_DIR/bench-table.md (the table the CI lane publishes) and
# OUT_DIR/run.log, and removes both containers on exit, whatever happened.
# QUARK_BENCH_ROUNDS, QUARK_BENCH_SAMPLE and QUARK_BENCH_BLOCK pass through
# to the bench, and so does QUARK_BENCH_REFERENCE, which the CI workflow sets
# to have the time checks asserted; PG_IMAGE and MYSQL_IMAGE override the
# engines.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
out="${1:-$(mktemp -d)}"
shift || true
if [ "$#" -gt 0 ]; then
  test_flags=("$@")
else
  test_flags=(-test.run '^TestEngineBench$' -test.v)
fi
mkdir -p "$out"
out="$(cd "$out" && pwd)"

pg_image="${PG_IMAGE:-postgres:16-alpine}"
my_image="${MYSQL_IMAGE:-mysql:8.4}"
name="quark-engine-bench-$$"

# shellcheck disable=SC2329 # invoked by the trap below
cleanup() {
  docker rm -f "$name-my" "$name-pg" >/dev/null 2>&1 || true
}
trap cleanup EXIT

arch="$(docker version --format '{{.Server.Arch}}')"
echo "== building the bench for linux/$arch"
# GOWORK=off: this module resolves quark and the drivers through its own
# replace directives, and `make workspace` leaves a go.work at the repository
# root that does not list it.
(cd "$here" && GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go test -c -o "$out/engines.test" .)

echo "== starting $pg_image and $my_image in one network namespace"
docker run -d --name "$name-pg" \
  --tmpfs /var/lib/postgresql/data:rw \
  -e POSTGRES_PASSWORD=bench -e POSTGRES_DB=bench \
  "$pg_image" -c synchronous_commit=off >/dev/null
docker run -d --name "$name-my" --network "container:$name-pg" \
  --tmpfs /var/lib/mysql:rw \
  -e MYSQL_ROOT_PASSWORD=bench -e MYSQL_DATABASE=bench \
  "$my_image" --innodb-autoinc-lock-mode=1 >/dev/null

# Both images run a temporary server while they initialise, listening on the
# socket only. Waiting on TCP waits for the real one.
wait_for() {
  local what="$1"; shift
  for _ in $(seq 1 120); do
    if "$@" >/dev/null 2>&1; then
      echo "   $what is up"
      return 0
    fi
    sleep 1
  done
  echo "$what did not come up within two minutes" >&2
  docker logs "$name-${what:0:2}" 2>&1 | tail -30 >&2 || true
  return 1
}
wait_for pg docker exec "$name-pg" pg_isready -h 127.0.0.1 -U postgres -d bench
wait_for my docker exec "$name-my" mysqladmin ping -h 127.0.0.1 -uroot -pbench --silent

echo "== running the bench"
set +e
docker run --rm --network "container:$name-pg" \
  -v "$out:/out" \
  -e QUARK_BENCH_POSTGRES_DSN="postgres://postgres:bench@127.0.0.1:5432/bench?sslmode=disable" \
  -e QUARK_BENCH_MYSQL_DSN="root:bench@tcp(127.0.0.1:3306)/bench?parseTime=true" \
  -e QUARK_BENCH_REQUIRE=postgres,mysql \
  -e QUARK_BENCH_OUT=/out \
  -e QUARK_BENCH_ROUNDS -e QUARK_BENCH_SAMPLE -e QUARK_BENCH_BLOCK -e QUARK_BENCH_REFERENCE \
  --entrypoint /out/engines.test \
  "$pg_image" "${test_flags[@]}" -test.timeout 20m 2>&1 | tee "$out/run.log"
status="${PIPESTATUS[0]}"
set -e
# The binary stays when a profile was asked for: pprof needs it for symbols.
if [ "$#" -eq 0 ]; then
  rm -f "$out/engines.test"
fi

if [ -f "$out/bench-table.md" ]; then
  echo "== table: $out/bench-table.md"
fi
exit "$status"
