#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Build (or take) the operand image and run the functional gate against it.
#
#   ./smoke.sh                      # build from versions.conf, then smoke it
#   DC_OPERAND_IMAGE=<ref> ./smoke.sh   # smoke an image that already exists
#
# The build itself asserts the image's *contents* (see the Dockerfile's final
# RUN). This script asserts its *behaviour* — that a server actually starts and
# that the TSL features ADR-026 is built on are really there. Both halves are
# needed: the contents check would pass on an image whose libraries are present
# and unloadable.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
set -a; . "$HERE/versions.conf"; set +a
# shellcheck source=./standalone.sh
. "$HERE/standalone.sh"

IMAGE="${DC_OPERAND_IMAGE:-}"
CONTAINER="${DC_OPERAND_CONTAINER:-dc-operand-smoke}"

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

if [ -z "$IMAGE" ]; then
  IMAGE="dc-tsdb-operand:smoke"
  "$HERE/build.sh" "$IMAGE"
else
  echo "==> Smoking pre-built image $IMAGE"
fi

echo "==> Starting a throwaway server from the operand image"
host_port="$(dc_operand_start "$IMAGE" "$CONTAINER" "127.0.0.1::5432")"
echo "==> Postgres up on 127.0.0.1:$host_port"

echo "==> Running smoke.sql"
# ON_ERROR_STOP is set inside smoke.sql too. It is passed here as well because
# the consequence of it being unset is not a failure — it is psql exiting 0 after
# an error, i.e. a green run that proved nothing.
dc_operand_psql "$CONTAINER" \
  -v ON_ERROR_STOP=1 \
  -v want_pg_major="$PG_MAJOR" \
  -v want_ts_version="$TIMESCALEDB_VERSION" \
  < "$HERE/smoke.sql"

# Compatibility libraries: prove they LOAD, not merely that they exist.
#
# The build asserts the .so files are present, and smoke.sql exercises only the
# default version — so until this loop, the libraries that exist solely for the
# rolling-update scenario were the one thing no gate ever ran. "Present" and
# "loadable" are different claims, and the whole point of carrying them is that
# a replica will load one for real.
#
# Each version gets its own database from template0: template1 already carries
# the extension at the DEFAULT version, so an inherited database cannot be asked
# to install an older one.
if [ -z "${TIMESCALEDB_COMPAT_VERSIONS// /}" ]; then
  echo "==> No compatibility versions configured — this check is inert until the first bump."
else
  for v in $TIMESCALEDB_COMPAT_VERSIONS; do
    echo "==> Loading compatibility version $v"
    db="compat_$(echo "$v" | tr . _)"
    dc_operand_psql "$CONTAINER" -v ON_ERROR_STOP=1 <<SQL
CREATE DATABASE $db TEMPLATE template0;
SQL
    docker exec -i "$CONTAINER" psql -U postgres -d "$db" -X -v ON_ERROR_STOP=1 <<SQL
CREATE EXTENSION timescaledb VERSION '$v';
DO \$\$
DECLARE got text;
BEGIN
  SELECT extversion INTO got FROM pg_extension WHERE extname = 'timescaledb';
  IF got IS DISTINCT FROM '$v' THEN
    RAISE EXCEPTION 'compatibility library % loaded as %', '$v', COALESCE(got, '<absent>');
  END IF;
END \$\$;
CREATE TABLE t (ts timestamptz NOT NULL, v double precision);
SELECT create_hypertable('t', 'ts');
INSERT INTO t SELECT now() - (i || ' min')::interval, i FROM generate_series(1, 50) i;
DO \$\$
DECLARE chunks int;
BEGIN
  SELECT count(*) INTO chunks FROM timescaledb_information.chunks WHERE hypertable_name = 't';
  IF chunks < 1 THEN
    RAISE EXCEPTION 'compatibility version % produced no chunks', '$v';
  END IF;
END \$\$;
SQL
    echo "==> Compatibility version $v loads and works"
  done
fi

echo "==> Operand image smoke PASSED ($IMAGE)"
