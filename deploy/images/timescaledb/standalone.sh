#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Run the operand image as a plain, throwaway Postgres server.
#
# WHY THIS EXISTS AT ALL. A CloudNativePG operand image is not a drop-in for
# `postgres` or `timescale/timescaledb`: it has no ENTRYPOINT, no
# `docker-entrypoint.sh`, no `/docker-entrypoint-initdb.d`, and its CMD is
# literally `bash`. In production that is correct — CNPG's instance manager runs
# initdb, writes postgresql.conf and starts the postmaster. But it means
# `docker run -e POSTGRES_PASSWORD=... <operand>` starts a shell that exits
# immediately, which is how a test harness ends up "passing" against a database
# that never came up.
#
# So the initdb dance lives here, in ONE place, and both consumers use it:
# `smoke.sh` (the functional gate) and `hack/migration-diff.sh` (the PG-17
# migration check). Duplicating it would let the two drift into testing
# different servers.
#
# 🔴 This is a TEST harness and must never be mistaken for how DeviceChain runs
# Postgres. It uses trust authentication and a data directory under /tmp, which
# is fine for a container that is destroyed at the end of the script and
# unacceptable anywhere else. Nothing in this file ships inside the image.
#
# Usage:
#   source standalone.sh
#   dc_operand_start <image> <container> <publish-spec>   # echoes the host port
#   dc_operand_psql  <container> [psql args...]

set -euo pipefail

# The command handed to the container. Kept as a single string so it can be
# passed straight to `bash -lc` inside the image.
dc_operand_bootstrap_cmd() {
  cat <<'BOOTSTRAP'
set -e
bindir="$(pg_config --bindir)"
export PGDATA=/tmp/pgdata
# 🔴 --encoding=UTF8 IS LOAD-BEARING, and its absence does not look like an
# encoding problem. The CNPG base image sets no LANG at all, so initdb infers
# SQL_ASCII -- and DeviceChain's data path then fails on every query with
# "simple protocol queries must be run with client_encoding=UTF8", because pgx
# refuses the simple protocol against a non-UTF8 server. The migration chain
# dies on its first statement.
#
# The same hazard applies to the real cluster: the A2.4 Cluster spec must set
# spec.bootstrap.initdb.encoding explicitly rather than letting it be inferred
# from an environment that, in this image, does not define one.
"$bindir/initdb" -D "$PGDATA" \
  --encoding=UTF8 --locale=C.utf8 \
  --auth-local=trust --auth-host=trust >/dev/null
{
  echo "listen_addresses = '*'"
  # TimescaleDB MUST be preloaded at postmaster start. It registers a custom WAL
  # resource manager, so a server that comes up without it cannot decode its own
  # WAL records -- which is the same mechanism that makes cnpg#10840 fatal for
  # TimescaleDB specifically rather than merely cosmetic.
  echo "shared_preload_libraries = 'timescaledb'"
  # Do not phone home from a throwaway CI container.
  echo "timescaledb.telemetry_level = off"
} >> "$PGDATA/postgresql.conf"
echo "host all all all trust" >> "$PGDATA/pg_hba.conf"

# Put timescaledb into template1 before opening the door.
#
# 🔴 This is not harness convenience — it reproduces a dependency DeviceChain
# has and does not declare. Services self-provision their own databases at
# startup (decision D2), so they always land on a database created AFTER the
# cluster was bootstrapped. `timescale/timescaledb` ships the extension in
# template1, so those databases inherit it and everything works. Without it,
# event-management's migrations fail on their first hypertable with
# `function create_hypertable(unknown, unknown) does not exist` -- which reads
# like a broken migration, not a missing bootstrap step.
#
# Under CNPG the equivalent is `spec.bootstrap.initdb.postInitTemplateSQL`, and
# this is the evidence for why A2.4 must set it.
"$bindir/pg_ctl" -D "$PGDATA" -w -o "-c listen_addresses='' -c unix_socket_directories=/tmp -c shared_preload_libraries=timescaledb -c timescaledb.telemetry_level=off" start >/dev/null
"$bindir/psql" -h /tmp -U postgres -d template1 -X -v ON_ERROR_STOP=1 \
  -c "CREATE EXTENSION IF NOT EXISTS timescaledb;" >/dev/null
"$bindir/pg_ctl" -D "$PGDATA" -w -m fast stop >/dev/null

exec "$bindir/postgres" -D "$PGDATA"
BOOTSTRAP
}

# dc_operand_start <image> <container> <publish-spec>
# Starts the container and blocks until Postgres accepts connections. Echoes the
# host port that Docker bound.
dc_operand_start() {
  local image="$1" container="$2" publish="$3"

  docker run -d --name "$container" -p "$publish" \
    --entrypoint bash "$image" -lc "$(dc_operand_bootstrap_cmd)" >/dev/null

  local host_port
  host_port="$(docker port "$container" 5432/tcp | head -n1 | sed 's/.*://')"
  if [ -z "$host_port" ]; then
    echo "could not determine the container's published port" >&2
    docker logs "$container" 2>&1 | tail -20 >&2
    return 1
  fi

  local ok=
  for _ in $(seq 1 60); do
    if docker exec "$container" pg_isready -q 2>/dev/null; then ok=1; break; fi
    sleep 1
  done
  if [ "$ok" != 1 ]; then
    # Print the logs. A silent "did not become ready" is the single most
    # expensive failure mode in a container harness, because the reason is
    # always in the postmaster output and always thrown away.
    echo "Postgres did not become ready in the operand container" >&2
    docker logs "$container" 2>&1 | tail -40 >&2
    return 1
  fi

  echo "$host_port"
}

# dc_operand_psql <container> [args...]
dc_operand_psql() {
  local container="$1"; shift
  docker exec -i "$container" psql -U postgres -d postgres -X -v ON_ERROR_STOP=1 "$@"
}
