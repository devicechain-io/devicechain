#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Tenant-deletion duration for a LARGE tenant, per store (the number a data-processing
# agreement quotes). Drives the real purge stores over a seeded tenant on throwaway servers.
#
#   hack/purge-measure.sh                                  # 1M events, 10k devices
#   PM_EVENTS=10000000 hack/purge-measure.sh               # the 10M-event tenant
#   PM_COMPRESS=0 PM_EVENTS=1000000 hack/purge-measure.sh  # worst case: nothing compressed
#
# Scale knobs (all optional): PM_DEVICES (10000), PM_EVENTS (1000000), PM_SPAN_DAYS (30),
# PM_BROKER_MSGS (= PM_EVENTS), PM_KV_KEYS_PER_DEVICE (1), PM_BLOBS (20000),
# PM_COMPRESS (1: chunks older than 7 days compressed, as the lifecycle policy leaves them).
# PM_REPORT is where the JSON report is written (default: ./purge-measure-report.json).
#
# It starts two containers, NAMED UNIQUELY, and removes exactly those two on exit. It never
# lists, stops or prunes anything else on the Docker host.
#
# 🔴 THE REPORT'S NUMBERS ARE ONLY AS GOOD AS THE VERIFICATION AT THE END OF THE TEST: the
# victim's rows must be gone from every store and the bystanders' still present, or the test
# fails and no report is written. A timing of a delete that deleted nothing is the confident
# wrong number this harness exists to avoid.
#
# Everything is single-node on loopback: network round trips are near zero and JetStream is
# unreplicated. Read the broker and key-value figures as a lower bound for a clustered
# instance; backend/tools/migrationdiff/purge_measure_test.go says what else it leaves out.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUFFIX="$$-$(date +%s)"
PG="dc-purge-measure-pg-$SUFFIX"
NATS="dc-purge-measure-nats-$SUFFIX"
PG_IMAGE="${PM_PG_IMAGE:-timescale/timescaledb:latest-pg16@sha256:61f891691050da6032023c01ea885730eeeba06b7c17b403e7d0b9c49c37dfe9}"
NATS_IMAGE="${PM_NATS_IMAGE:-nats:2.12-alpine@sha256:b270f5e2428354c0335612694d7dd2fb588148e567a5757fdff325ef9c9332e6}"

cleanup() { docker rm -f "$PG" "$NATS" >/dev/null 2>&1 || true; }
trap cleanup EXIT

if [[ "$PG_IMAGE" == *@sha256:* ]]; then
  PG_IMAGE="$("$ROOT/hack/docker-pull.sh" "$PG_IMAGE")" || exit 1
fi
if [[ "$NATS_IMAGE" == *@sha256:* ]]; then
  NATS_IMAGE="$("$ROOT/hack/docker-pull.sh" "$NATS_IMAGE")" || exit 1
fi

echo "==> starting $PG ($PG_IMAGE) and $NATS ($NATS_IMAGE)"
docker run -d --name "$PG" -e POSTGRES_PASSWORD=postgres -p 127.0.0.1::5432 "$PG_IMAGE" \
  -c max_wal_size=8GB -c checkpoint_timeout=15min >/dev/null
docker run -d --name "$NATS" -p 127.0.0.1::4222 "$NATS_IMAGE" -js -sd /data >/dev/null

PG_PORT="$(docker port "$PG" 5432/tcp | sed -n '1s/.*://p')"
NATS_PORT="$(docker port "$NATS" 4222/tcp | sed -n '1s/.*://p')"
[ -n "$PG_PORT" ] && [ -n "$NATS_PORT" ] || { echo "could not resolve published ports" >&2; exit 1; }

# The image runs a temporary server to initialise, stops it, then starts the real one, and
# pg_isready answers during the first. Wait for the second "ready" line in the log.
for _ in $(seq 1 120); do
  if [ "$(docker logs "$PG" 2>&1 | grep -c 'ready to accept connections')" -ge 2 ] &&
    docker exec "$PG" pg_isready -U postgres >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 1
done
[ "${ok:-}" = 1 ] || { echo "Postgres did not become ready" >&2; docker logs "$PG" | tail -20 >&2; exit 1; }

REPORT="${PM_REPORT:-$PWD/purge-measure-report.json}"
echo "==> running the measurement (pg :$PG_PORT, nats :$NATS_PORT, report $REPORT)"
cd "$ROOT/backend/tools/migrationdiff"
DC_IT_PGHOST=127.0.0.1 DC_IT_PGPORT="$PG_PORT" PM_NATS_URL="nats://127.0.0.1:$NATS_PORT" PM_REPORT="$REPORT" \
  go test -tags purgemeasure -count=1 -timeout 4h -run '^TestPurgeMeasurement$' -v .
echo "==> report: $REPORT"
