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
#   hack/migration-diff.sh coverage   # assert the tenant purge accounts for every table
#   hack/migration-diff.sh replay     # assert every migration is individually re-runnable
#
# `verify` is the guard: it fails if a migration changes a schema without the golden
# being refreshed, and — at the GA migration-squash — it proves a single baseline
# migration reproduces the exact schema the incremental chain built.
#
# `verify` ALSO runs `coverage` against the same freshly migrated database, because the
# two guards answer different questions about the same migration and only one of them
# is about the schema's text. Coverage asserts that every table an instance ends up with
# can be accounted for by the tenant purge — a tenant column, a foreign key into
# tenant-bearing data, or a registered exemption stating why it holds none. A migration
# that adds a table holding tenant data with no tenant column passes `verify` cleanly
# (the golden simply gains the new table) and would leave that data un-erasable forever.
# Running it here rather than as its own CI step is deliberate: it needs a database with
# EVERY area migrated into it, which is what this harness already builds, on both
# supported Postgres majors.
#
# 🔴 The default image here is NOT the one deploy/opentofu ships, and that is deliberate.
# The goldens were captured on PostgreSQL 16 against the community image pinned below, and
# they are version-sensitive (continuous-aggregate dumps vary by Timescale version), so
# snapshot + verify must keep using the image the goldens came from. The DEPLOYED event
# store runs our own PostgreSQL 17 operand image, which CI verifies against these same
# goldens in a second pass (MDIFF_LAUNCH=operand) — that second pass is what pins the
# claim that both majors produce identical schema. Override with MDIFF_IMAGE to check
# another one.
#
# 🔴 A GOLDEN AND THE DIFFER THAT CAPTURED IT ARE ONE ARTIFACT. RE-SNAPSHOT AGAINST THE
# TREE YOU WILL COMMIT, NOT THE ONE YOU STARTED FROM.
#
# The pinned image above is only half of what a golden depends on: the other half is
# backend/tools/migrationdiff itself — its pg_dump flags and its normalizeDump. A
# sibling PR that changes either produces a differently-shaped dump from the same
# database, so a golden captured before you rebased onto it can be wrong under the
# differ that CI will actually run, and the failure arrives as a schema diff that
# looks like your migration's fault.
#
# This is not hypothetical: #893 changed the differ's dump flags and normalization
# while #894 was re-snapshotting for a new table. The golden had to be captured
# twice — once, then again after the rebase — and only the second capture was
# checked by the instrument CI uses.
#
# So: rebase FIRST, snapshot SECOND, and if main moves under you again, re-run
# `verify` before pushing. Verify is cheap and it is the only thing that can tell
# you the artifact and the instrument still agree.
set -euo pipefail

MODE="${1:-verify}"
case "$MODE" in
  snapshot | verify | coverage | replay) ;;
  *) echo "usage: $0 <snapshot|verify|coverage|replay>" >&2; exit 2 ;;
esac

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOL_DIR="$ROOT/backend/tools/migrationdiff"
GOLDEN_DIR="${MDIFF_GOLDEN_DIR:-$TOOL_DIR/golden}"

CONTAINER="${MDIFF_CONTAINER:-dc-mdiff}"
# Pinned by DIGEST, not by the `latest-pg16` tag it resolves to. Goldens are
# version-sensitive, and `latest-pg16` is a moving tag: the day Timescale
# republishes it, `verify` starts failing on every PR for a reason that has
# nothing to do with the PR. The digest below is the image the goldens were
# captured against (TimescaleDB 2.28.3 / PostgreSQL 16).
IMAGE="${MDIFF_IMAGE:-timescale/timescaledb:latest-pg16@sha256:61f891691050da6032023c01ea885730eeeba06b7c17b403e7d0b9c49c37dfe9}"
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

# How to start the image. Two image families are in play during the ADR-020 A2
# migration to CloudNativePG, and they boot in incompatible ways:
#
#   entrypoint (default) — `timescale/timescaledb`, which inherits the official
#     postgres image's docker-entrypoint: it reads POSTGRES_PASSWORD, runs initdb
#     itself and starts the server. This is what deploy/opentofu ships today.
#
#   operand — a CloudNativePG operand image (deploy/images/timescaledb, ADR-020
#     A2.6). It has NO entrypoint at all and its CMD is `bash`, because in
#     production CNPG's instance manager does the initdb. Started the default way
#     it would run a shell, exit, and leave this harness waiting on a server that
#     was never going to arrive.
LAUNCH="${MDIFF_LAUNCH:-entrypoint}"

# Guard: `snapshot` against a non-default server would overwrite the committed
# goldens with dumps from a DIFFERENT PostgreSQL/TimescaleDB, and the result
# looks exactly like a legitimate refresh — a diff full of plausible schema
# changes, committed by someone who ran the documented command. Require an
# explicit destination instead.
if [ "$MODE" = "snapshot" ] && [ "$LAUNCH" != "entrypoint" ] && [ -z "${MDIFF_GOLDEN_DIR:-}" ]; then
  echo "refusing to snapshot into the committed goldens from launch=$LAUNCH." >&2
  echo "The goldens belong to the default image; a snapshot from another server" >&2
  echo "would silently replace them. Set MDIFF_GOLDEN_DIR to a scratch directory," >&2
  echo "or run with the default launch mode." >&2
  exit 2
fi

echo "==> Starting throwaway TimescaleDB ($IMAGE, launch=$LAUNCH) as $CONTAINER"
case "$LAUNCH" in
  entrypoint)
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
    ;;
  operand)
    # shellcheck source=../deploy/images/timescaledb/standalone.sh
    . "$ROOT/deploy/images/timescaledb/standalone.sh"
    HOST_PORT="$(dc_operand_start "$IMAGE" "$CONTAINER" "$PUBLISH")"
    echo "==> Postgres published on 127.0.0.1:$HOST_PORT"
    ;;
  *)
    echo "unknown MDIFF_LAUNCH='$LAUNCH' (expected 'entrypoint' or 'operand')" >&2
    exit 2
    ;;
esac

echo "==> Running migrationdiff (mode=$MODE)"
cd "$TOOL_DIR"
go run . \
  -mode "$MODE" \
  -container "$CONTAINER" \
  -host localhost -port "$HOST_PORT" \
  -user postgres -password "$PASSWORD" \
  -db "$DB" \
  -golden-dir "$GOLDEN_DIR"

# The tenant-purge coverage gate, over the database `verify` just migrated. It re-runs
# the chains first, which is a no-op on an already-migrated database, so the only cost
# here is the classification itself.
if [ "$MODE" = "verify" ]; then
  echo "==> Running migrationdiff (mode=coverage)"
  go run . \
    -mode coverage \
    -host localhost -port "$HOST_PORT" \
    -user postgres -password "$PASSWORD" \
    -db "$DB"

  # The REPLAY gate. `verify` compares pg_dump output, which is blind to this property by
  # construction: gormigrate skips an ID it has already recorded, so running a chain twice
  # is a no-op that produces an identical dump. Every migrations.go tells the next
  # maintainer their migration must be individually re-runnable — this is the first thing
  # that checks.
  #
  # It works in its own database (<db>_replay) because it repeatedly DROPs and rebuilds
  # schemas, which would destroy the artefact `verify` above just finished asserting
  # against the goldens. Nothing after this point reads that database today — the drill
  # builds its own — so the separation is defensive: it is what keeps a check ADDED after
  # this one from silently running against a rebuilt schema.
  echo "==> Running migrationdiff (mode=replay)"
  go run . \
    -mode replay \
    -container "$CONTAINER" \
    -host localhost -port "$HOST_PORT" \
    -user postgres -password "$PASSWORD" \
    -db "$DB"

  # The purge DRILL, which is a different claim from the coverage gate above and the only
  # one that touches a row. Coverage proves every table is accounted for; this proves a
  # sweep of the real foreign-key graph erases one tenant and leaves another intact — and
  # that a continuous aggregate's materialized copy goes with it, which is invisible to a
  # schema-only check because no ROW appears in a pg_dump.
  #
  # These run here rather than in the `go` CI job because they need exactly what this
  # script already has: a real PostgreSQL with the TimescaleDB extension, and (for the
  # drill) every area's chain applied. The `go` job has no database at all, which is why
  # both suites were previously only VETTED and never executed.
  #
  # -count=1 because they read a database, and a cached PASS would replay over a broken
  # tree. No -run filter, deliberately: naming the tests here is the same
  # remember-to-add-it coverage a list always turns out to be, and an integration test
  # added tomorrow should run tomorrow.
  echo "==> Running the tenant-purge drill"
  DC_IT_PGPORT="$HOST_PORT" go test -tags integration -count=1 -timeout 20m ./...

  # core/rdb's guest-connection suite. It is the only thing that proves a real "database
  # does not exist" error survives pgx, database/sql, gorm and our own wrapping as
  # something errors.As can still recognise — the single assumption the telemetry store's
  # "absent database ⇒ nothing to erase" conclusion rests on, and one a hand-built
  # pgconn.PgError structurally cannot check. Different module, so a separate invocation.
  echo "==> Running the guest-connection integration suite"
  ( cd "$ROOT/backend/core" && DC_IT_PGPORT="$HOST_PORT" \
      go test -tags integration -count=1 ./rdb/... )
fi

echo "==> Done."
