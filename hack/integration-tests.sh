#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Runs every `integration`-tagged Go test in the workspace against a throwaway PostgreSQL.
#
# These suites exist because sqlite cannot answer the questions they ask — row locking and real
# concurrency (device-management's geofence TOCTOU), hypertables, chunks and columnar compression
# (event-management), a real catalog and a real second database (core/rdb, migrationdiff). Until
# this script existed CI only ever VETTED them: `go vet -tags "integration interop"` compiles a
# test without running it, so every one of them could have been failing for months in silence.
#
#   hack/integration-tests.sh              # start a container, run everything, tear it down
#   DC_IT_PGPORT=5432 hack/integration-tests.sh --no-container   # against a server you started
#   hack/integration-tests.sh --self-test  # prove the no-tests-ran guard can fire (needs no server)
#
# 🔴 IT RUNS EACH MODULE'S WHOLE TAGGED SUITE, WITH NO -run FILTER, AND THAT IS DELIBERATE.
# The first version of this filtered by name (`-run Integration`, `-run PurgeDrill`) and covered
# 8 of the 13 tagged tests — core/rdb's two are named TestAGuestConnects... and
# TestARealMissingDatabase..., neither of which contains "Integration", so BOTH were skipped while
# the module reported rc=0. A green result from a filter that matches nothing is the most
# expensive kind of pass, which is also why the guard below exists.
set -u

# ran_count reads a `go test -v` transcript on stdin and reports how many tests actually STARTED.
# It is a function so --self-test can exercise the same expression the run below trusts.
ran_count() { grep -c '^=== RUN' || true; }

# The negative control, in the shape the other hack/ gates use: a guard is worth nothing until it
# has been shown to fire. This one exists because a `-run` filter that matches nothing exits 0 with
# "no tests to run", which is indistinguishable from a pass — the exact way the first draft of this
# script reported success while skipping 5 of 13 tagged tests.
if [ "${1:-}" = "--self-test" ]; then
  empty=$(printf 'testing: warning: no tests to run\nPASS\nok  \tfoo\t0.001s\n' | ran_count)
  if [ "$empty" -ne 0 ]; then
    echo "self-test FAILED: the guard counted $empty started tests in a transcript that ran none"
    exit 1
  fi
  # The counterweight. A guard that always reports zero would pass the check above and then fail
  # every real module, which is a different broken instrument with the same green summary.
  real=$(printf '=== RUN   TestOne\n--- PASS: TestOne (0.00s)\n=== RUN   TestTwo\n--- PASS: TestTwo (0.00s)\nPASS\n' | ran_count)
  if [ "$real" -ne 2 ]; then
    echo "self-test FAILED: the guard counted $real started tests in a transcript that ran 2"
    exit 1
  fi
  echo "self-test passed: the no-tests-ran guard fires on an empty run and stays silent on a real one"
  exit 0
fi

CONTAINER=1
[ "${1:-}" = "--no-container" ] && CONTAINER=0

NAME=dc-integration-tests
# The SAME digest-pinned image hack/migration-diff.sh uses. Pinned rather than tracking
# `latest-pg16` because a moving tag can change what this gate means without a commit, and
# identical to migration-diff's so the two gates never disagree about which server they mean.
IMAGE=${DC_IT_IMAGE:-timescale/timescaledb:latest-pg16@sha256:61f891691050da6032023c01ea885730eeeba06b7c17b403e7d0b9c49c37dfe9}

if [ "$CONTAINER" = "1" ]; then
  docker rm -f "$NAME" >/dev/null 2>&1
  # The password is "postgres" everywhere. hack/migration-diff.sh starts its server with it and
  # core/rdb's and migrationdiff's suites hardcode it; event-management's alone DEFAULTS to
  # "devicechain" but honours DC_IT_PGPASSWORD, which is why that is exported below rather than
  # left to each suite's default.
  # Pulled explicitly and retried: this reaches a third-party registry on every pull request,
  # and a transient 5xx there would otherwise red-line a run that has nothing to do with it.
  pulled=0
  for attempt in 1 2 3; do
    if docker pull --quiet "$IMAGE" >/dev/null 2>&1; then pulled=1; break; fi
    echo "docker pull attempt $attempt failed; retrying"
    sleep $((attempt * 10))
  done
  [ "$pulled" = "1" ] || { echo "could not pull $IMAGE after 3 attempts"; exit 1; }
  docker run -d --name "$NAME" -e POSTGRES_PASSWORD=postgres -P "$IMAGE" >/dev/null || {
    echo "failed to start $IMAGE"; exit 1; }
  trap 'docker rm -f "$NAME" >/dev/null 2>&1' EXIT
  DC_IT_PGPORT=$(docker port "$NAME" 5432/tcp | head -n1 | sed 's/.*://')
  for _ in $(seq 1 90); do
    docker exec "$NAME" pg_isready -U postgres >/dev/null 2>&1 && break
    sleep 2
  done
  docker exec "$NAME" pg_isready -U postgres || { echo "postgres never became ready"; exit 1; }
fi

export DC_IT_PGPORT="${DC_IT_PGPORT:-5432}"
export DC_IT_PGHOST="${DC_IT_PGHOST:-localhost}"
export DC_IT_PGUSER="${DC_IT_PGUSER:-postgres}"
export DC_IT_PGPASSWORD="${DC_IT_PGPASSWORD:-postgres}"
echo "postgres on ${DC_IT_PGHOST}:${DC_IT_PGPORT}"

# The workspace enumerates its own modules, so a module that GAINS an integration test is covered
# the day it does — no list here to fall out of step with go.work.
modules=()
for m in $(go list -m -f '{{.Dir}}'); do
  grep -rlq '^//go:build integration' "$m" --include=*.go 2>/dev/null || continue
  modules+=("$m")
done
if [ ${#modules[@]} -eq 0 ]; then
  echo "no module carries an integration-tagged test; this script would be a no-op that reports success"
  exit 1
fi

rc=0
for m in "${modules[@]}"; do
  name=${m#"$PWD"/}
  echo "=== $name ==="
  out=$(cd "$m" && go test -tags integration -count=1 -p 2 -v ./... 2>&1)
  status=$?
  ran=$(printf '%s\n' "$out" | ran_count)
  # Summaries always; PASS lines are not summaries — core alone emits 1131 of them and they
  # bury everything worth reading.
  printf '%s\n' "$out" | grep -E '^(ok|FAIL|panic)' | head -40
  echo "  rc=$status tests_run=$ran"
  # 🔴 ON FAILURE, PRINT THE EVIDENCE. The first version showed a filtered head -40 and nothing
  # else, so a module with 643 tests failed with its `--- FAIL` past the cut and a lone
  # `panic(...)` header for a stack that had been discarded. A gate whose red result does not
  # say WHY sends its reader to reproduce the run by hand, which is most of what it was for.
  if [ $status -ne 0 ]; then
    echo "  ---- failures in $name ----"
    printf '%s\n' "$out" | grep -E '^--- FAIL|_test\.go:[0-9]+:|^panic|^\s+/.*\.go:[0-9]+' | head -60
  fi
  # 🔴 THE GUARD. A module whose tests all failed to MATCH, or whose build tag was mistyped,
  # exits 0 with "no tests to run" — indistinguishable from a pass in every summary. A module
  # in this list has an integration test by construction, so running none of them is a failure
  # of this script, not a quiet success.
  if [ "$ran" -eq 0 ]; then
    echo "  FAILED: $name ran NO tests despite carrying an integration-tagged file"
    rc=1
  fi
  [ $status -eq 0 ] || { echo "  FAILED: $name"; rc=1; }
done
echo "INTEGRATION_OVERALL_RC=$rc"
exit "$rc"
