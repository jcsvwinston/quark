#!/usr/bin/env bash
# oracle-up.sh — arranca el Oracle de la matriz (gvenzl/oracle-free) con el
# MISMO bootstrap que CI: espera de readiness + GRANT EXECUTE ON DBMS_LOCK
# (sin él, MigrationLock falla con PLS-00201). Antes estas ~25 líneas vivían
# DUPLICADAS en dos jobs de ci.yml y reproducirlas en local era copiar YAML
# a mano; ahora CI y local comparten este script.
#
# Uso local:
#   bash scripts/ci/oracle-up.sh
#   export QUARK_TEST_ORACLE_DSN=oracle://quark:quark@localhost:1521/FREEPDB1
#
# En CI, el DSN se exporta a $GITHUB_ENV automáticamente; el nombre de la
# variable es el primer argumento (default QUARK_TEST_ORACLE_DSN — el job
# del superapp pasa SUPERAPP_DSN_ORACLE).
set -euo pipefail

DSN_VAR="${1:-QUARK_TEST_ORACLE_DSN}"

docker run -d --name quark-oracle -p 1521:1521 \
  -e ORACLE_PASSWORD=quark \
  -e APP_USER=quark \
  -e APP_USER_PASSWORD=quark \
  gvenzl/oracle-free:23-slim

echo "Waiting for Oracle to report ready..."
# 6 min is ample (observed ~30-90s) and keeps the worst case inside the job cap.
deadline=$((SECONDS + 360))
until docker logs quark-oracle 2>&1 | grep -q "DATABASE IS READY TO USE!"; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "Oracle did not become ready within 6 minutes; logs:"
    docker logs quark-oracle
    exit 1
  fi
  sleep 5
done

echo "Granting EXECUTE ON DBMS_LOCK to quark..."
# -i is required: without it `docker exec` does not forward the heredoc to
# sqlplus's stdin, so the grant silently no-ops (exit 0, no SQL run) and the
# MigrationLock subtest later fails with PLS-00201 on DBMS_LOCK. With -i the
# WHENEVER SQLERROR guard below also actually fires on a failed grant.
docker exec -i quark-oracle sqlplus -S -L sys/quark@localhost:1521/FREEPDB1 as sysdba <<'SQL'
WHENEVER SQLERROR EXIT SQL.SQLCODE
GRANT EXECUTE ON DBMS_LOCK TO quark;
EXIT
SQL

DSN="oracle://quark:quark@localhost:1521/FREEPDB1"
if [ -n "${GITHUB_ENV:-}" ]; then
  echo "$DSN_VAR=$DSN" >> "$GITHUB_ENV"
else
  echo "Oracle listo. Exporta: $DSN_VAR=$DSN"
fi
