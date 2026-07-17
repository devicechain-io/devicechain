#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Migration-flatten equivalence harness (see backend/tools/migrationdiff).
#
# Stands up a throwaway TimescaleDB, runs every service's gormigrate chain through the
# real core/rdb path, and snapshots or verifies each functional area's schema:
#
#   hack/migration-diff.sh snapshot   # capture golden schemas from the current chains
#   hack/migration-diff.sh verify     # assert the chains still reproduce the goldens
#
# `verify` is the guard: it fails if a migration changes a schema without the golden
# being refreshed, and — at the GA migration-squash — it proves a single baseline
# migration reproduces the exact schema the incremental chain built.
#
# The Timescale image is pinned to what deploy/opentofu ships (a Postgres superset that
# preloads the timescaledb extension). Goldens are version-sensitive (continuous-
# aggregate dumps vary by Timescale version), so snapshot + verify must use the same
# image; override with MDIFF_IMAGE if you bump the deployed version.
set -euo pipefail

MODE="${1:-verify}"
case "$MODE" in
  snapshot | verify) ;;
  *) echo "usage: $0 <snapshot|verify>" >&2; exit 2 ;;
esac

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOL_DIR="$ROOT/backend/tools/migrationdiff"
GOLDEN_DIR="$TOOL_DIR/golden"

CONTAINER="${MDIFF_CONTAINER:-dc-mdiff}"
IMAGE="${MDIFF_IMAGE:-timescale/timescaledb:latest-pg16}"
# MDIFF_PORT pins the host port for local debugging; unset (the default, and CI) lets
# Docker assign a free ephemeral port on loopback. A hardcoded host port is fragile in
# CI — the throwaway container failed to start with "address already in use" on
# 0.0.0.0:55432 when something else held the port — and there is no reason to pin one:
# the tool connects to whatever port we discover below, and the port is bound to
# 127.0.0.1 rather than 0.0.0.0 so it is never exposed off-box.
PORT="${MDIFF_PORT:-}"
PASSWORD="postgres"
DB="dcmigrationdiff"

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

if [ -n "$PORT" ]; then
  PUBLISH="127.0.0.1:$PORT:5432"
else
  PUBLISH="127.0.0.1::5432" # empty middle field → Docker picks a free ephemeral port
fi

echo "==> Starting throwaway TimescaleDB ($IMAGE) as $CONTAINER"
docker run -d --name "$CONTAINER" \
  -e POSTGRES_PASSWORD="$PASSWORD" \
  -p "$PUBLISH" \
  "$IMAGE" >/dev/null

# Discover the host port Docker actually bound (the ephemeral one, or the pinned one).
# `docker port <c> 5432/tcp` prints e.g. "127.0.0.1:49153"; take the port after the colon.
HOST_PORT="$(docker port "$CONTAINER" 5432/tcp | head -n1 | sed 's/.*://')"
[ -n "$HOST_PORT" ] || { echo "could not determine the container's published port" >&2; docker logs "$CONTAINER" | tail -20 >&2; exit 1; }
echo "==> Postgres published on 127.0.0.1:$HOST_PORT"

echo -n "==> Waiting for Postgres to accept connections"
for _ in $(seq 1 60); do
  if docker exec "$CONTAINER" pg_isready -U postgres >/dev/null 2>&1; then ok=1; break; fi
  echo -n "."; sleep 1
done
echo
[ "${ok:-}" = 1 ] || { echo "Postgres did not become ready" >&2; docker logs "$CONTAINER" | tail -20 >&2; exit 1; }

echo "==> Running migrationdiff (mode=$MODE)"
cd "$TOOL_DIR"
go run . \
  -mode "$MODE" \
  -container "$CONTAINER" \
  -host localhost -port "$HOST_PORT" \
  -user postgres -password "$PASSWORD" \
  -db "$DB" \
  -golden-dir "$GOLDEN_DIR"

echo "==> Done."
