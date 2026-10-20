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
#
# 🔴 "RAN SOMETHING" IS NOT "RAN EVERYTHING". Per module, after the run, the script derives every
# top-level Test function declared in an integration-tagged file and requires each to have STARTED
# (and not skipped) in the transcript, so a tagged test that never executes fails the job by name.
# The file set is cross-checked against the package loader's own view (`go list` with and without
# the tag), so a build-constraint spelling the text match does not know is a failure rather than
# an untested file. Tagged Benchmarks are measurement rigs and are listed, not run.
set -u

# ran_count reads a `go test -v` transcript on stdin and reports how many tests actually STARTED.
# It is a function so --self-test can exercise the same expression the run below trusts.
ran_count() { grep -c '^=== RUN' || true; }

# A file is integration-tagged when its constraint NAMES the tag positively: `integration`,
# `integration && unix`, `unix && integration`, or the legacy `// +build` form. `!integration`
# does not match. ONE definition, used by module discovery and by the per-test guard below, so the
# two cannot disagree about which files count.
TAG_RE='^(//go:build (.* )?integration( .*)?|// \+build .*integration.*)$'

# tagged_files lists (relative to the current directory, ./-prefixed) the integration-tagged
# *_test.go files. Run it from a module root.
tagged_files() { grep -rlE "$TAG_RE" . --include='*_test.go' 2>/dev/null | sort; }

# gated_files prints the *_test.go files (./-relative, like tagged_files) that exist ONLY with the
# integration tag, as the compiler's package loader sees it: the test files `go list` reports with
# the tag minus those it reports without. It shares no code with tagged_files, so a build-constraint
# spelling the grep does not know (a parenthesised or `||` expression, say) shows up as a file this
# lists and that does not. Run it from a module root.
gated_files() {
  local fmt='{{range .TestGoFiles}}{{$.Module.Path}} {{$.ImportPath}} {{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$.Module.Path}} {{$.ImportPath}} {{.}}{{"\n"}}{{end}}'
  comm -23 <(go list -tags integration -f "$fmt" ./... 2>/dev/null | tr -d '\r' | sort -u) \
           <(go list -f "$fmt" ./... 2>/dev/null | tr -d '\r' | sort -u) |
    awk '{ d=$2; sub("^" $1, "", d); print "." d "/" $3 }' | sort -u
}

# names_in prints the top-level Test functions declared in the files given as arguments.
names_in() { [ $# -eq 0 ] || sed -n 's/^func \(Test[A-Za-z0-9_]*\)(.*\*testing\.T).*/\1/p' "$@" | sort -u; }

# never_started reads a `go test -v` transcript on stdin and the expected test names as arguments,
# and prints each name that has no `=== RUN` line (never started) or that started and was SKIPPED.
# A tagged test that skips itself is as unexecuted as one that never started: every integration
# test here fails, rather than skips, when its server is missing.
never_started() {
  local transcript n
  transcript=$(cat)
  for n in "$@"; do
    if ! grep -qE "^=== RUN +$n\$" <<<"$transcript"; then
      echo "$n (never started)"
    elif grep -qE "^--- SKIP: $n " <<<"$transcript"; then
      echo "$n (skipped)"
    fi
  done
}

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
  # The per-test guard gets the same pair of controls: it must name a test that never started and
  # one that skipped, and must stay silent when every expected test ran.
  gone=$(printf '=== RUN   TestOne\n--- PASS: TestOne (0.00s)\n=== RUN   TestSkipped\n--- SKIP: TestSkipped (0.00s)\n' \
    | never_started TestOne TestMissing TestSkipped | tr '\n' ';')
  if [ "$gone" != "TestMissing (never started);TestSkipped (skipped);" ]; then
    echo "self-test FAILED: the per-test guard reported [$gone] for a missing and a skipped test"
    exit 1
  fi
  quiet=$(printf '=== RUN   TestOne\n--- PASS: TestOne (0.00s)\n=== RUN   TestOneMore\n' | never_started TestOne)
  if [ -n "$quiet" ]; then
    echo "self-test FAILED: the per-test guard named [$quiet] although every expected test ran"
    exit 1
  fi
  # And the tag matcher, because a pattern that misses a syntax silently drops a file from every
  # check built on it.
  for ok in '//go:build integration' '//go:build integration && unix' '//go:build unix && integration' '// +build integration'; do
    grep -qE "$TAG_RE" <<<"$ok" || { echo "self-test FAILED: tag matcher missed [$ok]"; exit 1; }
  done
  for no in '//go:build !integration' '//go:build interop' '//go:build integrationx' '// integration notes'; do
    if grep -qE "$TAG_RE" <<<"$no"; then echo "self-test FAILED: tag matcher accepted [$no]"; exit 1; fi
  done
  echo "self-test passed: the no-tests-ran and per-test guards fire on a bad run and stay silent on a real one"
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
  # Pulled explicitly, retried, and mirrored: this reaches a third-party registry on every pull
  # request, and a Docker Hub outage would otherwise red-line a run that has nothing to do with
  # it. hack/docker-pull.sh falls back to a mirror BY DIGEST (so the bytes are the pinned ones)
  # and prints the reference to run, which is a local tag when a mirror served it. An override
  # without a digest has no mirror and keeps the plain retried pull.
  if [[ "$IMAGE" == *@sha256:* ]]; then
    IMAGE="$("$(dirname "${BASH_SOURCE[0]}")/docker-pull.sh" "$IMAGE")" || exit 1
  else
    pulled=0
    for attempt in 1 2 3; do
      if docker pull --quiet "$IMAGE" >/dev/null 2>&1; then pulled=1; break; fi
      echo "docker pull attempt $attempt failed; retrying"
      sleep $((attempt * 10))
    done
    [ "$pulled" = "1" ] || { echo "could not pull $IMAGE after 3 attempts"; exit 1; }
  fi
  docker run -d --name "$NAME" -e POSTGRES_PASSWORD=postgres -P "$IMAGE" >/dev/null || {
    echo "failed to start $IMAGE"; exit 1; }
  trap 'docker rm -f "$NAME" >/dev/null 2>&1' EXIT
  DC_IT_PGPORT=$(docker port "$NAME" 5432/tcp | sed -n '1s/.*://p')
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
  ( cd "$m" && [ -n "$(tagged_files)" ] ) || continue
  modules+=("$m")
done
# Cross-check the count against a second, independent parse, the way CI's go-module discovery does:
# a loose matcher above would otherwise shrink the set with no one the wiser. This one asks the
# package loader instead of the text: a module is integration-gated iff `go list` sees more test
# files with the tag than without it.
want=0
for m in $(go list -m -f '{{.Dir}}'); do
  tw=$(cd "$m" && go list -tags integration -f '{{len .TestGoFiles}} {{len .XTestGoFiles}}' ./... 2>/dev/null | awk '{s+=$1+$2} END{print s+0}')
  to=$(cd "$m" && go list -f '{{len .TestGoFiles}} {{len .XTestGoFiles}}' ./... 2>/dev/null | awk '{s+=$1+$2} END{print s+0}')
  if [ "$tw" -gt "$to" ]; then want=$((want + 1)); fi
done
if [ "$want" -ne ${#modules[@]} ]; then
  echo "module discovery disagrees: grep found ${#modules[@]} integration-tagged modules, go list found $want"
  exit 1
fi
if [ ${#modules[@]} -eq 0 ]; then
  echo "no module carries an integration-tagged test; this script would be a no-op that reports success"
  exit 1
fi

# DC_IT_MODULES narrows the run to the named modules (repository-relative paths, space
# separated), AFTER discovery and its cross-check, so the discovery guard above still runs in
# full. It exists for the "TimescaleDB next" CI job, which runs one module's suite against the
# next image. Every name must be a discovered integration module: a misspelt or moved path
# fails here rather than narrowing the run to nothing.
if [ -n "${DC_IT_MODULES:-}" ]; then
  selected=()
  for want in $DC_IT_MODULES; do
    hit=""
    for m in "${modules[@]}"; do
      [ "${m#"$PWD"/}" = "$want" ] && hit=$m
    done
    [ -n "$hit" ] || { echo "DC_IT_MODULES names $want, which is not an integration-tagged module of the workspace"; exit 1; }
    selected+=("$hit")
  done
  modules=("${selected[@]}")
  echo "DC_IT_MODULES: running ${#modules[@]} module(s): $DC_IT_MODULES"
fi

rc=0
for m in "${modules[@]}"; do
  name=${m#"$PWD"/}
  echo "=== $name ==="
  # -timeout: go test's default of 10 minutes is per PACKAGE, and event-management's model
  # package builds tables of hundreds of chunks to test the chunk ceiling of its startup
  # migrations, which takes several minutes on its own. A package that hits the default dies
  # with a panic naming whichever test happened to be running, which reads as that test's fault.
  out=$(cd "$m" && go test -tags integration -count=1 -p 2 -timeout 30m -v ./... 2>&1)
  status=$?
  ran=$(printf '%s\n' "$out" | ran_count)
  # Summaries always; PASS lines are not summaries — core alone emits 1131 of them and they
  # bury everything worth reading.
  printf '%s\n' "$out" | grep -E '^(ok|FAIL|panic)' | sed -n '1,40p'
  echo "  rc=$status tests_run=$ran"
  # 🔴 ON FAILURE, PRINT THE EVIDENCE. The first version showed a filtered head -40 and nothing
  # else, so a module with 643 tests failed with its `--- FAIL` past the cut and a lone
  # `panic(...)` header for a stack that had been discarded. A gate whose red result does not
  # say WHY sends its reader to reproduce the run by hand, which is most of what it was for.
  if [ $status -ne 0 ]; then
    echo "  ---- failures in $name ----"
    # 🔴 THE `--- FAIL` LINES COME FIRST, ON THEIR OWN. The evidence below is capped, and a
    # PASSING package that logs a recovered panic (graphql-go prints the stack of every
    # resolver panic it catches, 20 frame lines each) spends the whole cap before the failing
    # package's output begins — which is how a red integration job once named no test at all.
    printf '%s\n' "$out" | grep -E '^\s*--- FAIL' | sed -n '1,40p'
    # Then each failure's OWN message: the indented lines go test prints under a `--- FAIL`
    # (the t.Error/testify block), without stack frames. The capped dump below still begins with
    # whatever a passing package printed first, so without this a red job named its failing
    # test but not why it failed (#1803: TestCalculatedMetricsOnPostgres, message nowhere).
    printf '%s\n' "$out" | awk '/^[[:space:]]*--- FAIL/ { on = 1; print; next }
      on && /^[[:space:]]/ && !/^[[:space:]]+\/.*\.go:[0-9]+/ { print; next }
      { on = 0 }' | sed -n '1,80p'
    printf '%s\n' "$out" | grep -E '^--- FAIL|_test\.go:[0-9]+:|^panic|^\s+/.*\.go:[0-9]+' | sed -n '1,60p'
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
  # 🔴 THE PER-TEST GUARD. `tests_run > 0` says the module ran SOMETHING; it cannot say that THIS
  # file's tests ran, and a suite that is only partly executed is how a gate stays green over a
  # test that has never once run. So: derive the tagged Test functions from the files, require each
  # to have started in the transcript, and fail a module where one did not, or where it skipped.
  # Benchmarks do not run without -bench; they are listed, not run.
  files=()
  while IFS= read -r f; do [ -n "$f" ] && files+=("$f"); done < <(cd "$m" && tagged_files)
  missed=$(comm -23 <(cd "$m" && gated_files) <(printf '%s\n' "${files[@]}" | sort -u))
  if [ -n "$missed" ]; then
    echo "  FAILED: $name has test files that exist only under the integration tag but whose constraint"
    echo "          this script does not recognise, so their tests would never be checked:"
    printf '%s\n' "$missed" | sed 's/^/            /'
    rc=1
  fi
  expected=$(cd "$m" && names_in "${files[@]}")
  if [ -n "$expected" ]; then
    names=()
    while IFS= read -r n; do names+=("$n"); done <<<"$expected"
    gone=$(printf '%s\n' "$out" | never_started "${names[@]}")
    echo "  tagged tests expected=${#names[@]} not_run=$(printf '%s' "$gone" | grep -c . || true)"
    if [ -n "$gone" ]; then
      echo "  FAILED: $name carries integration-tagged tests that did not execute:"
      printf '%s\n' "$gone" | sed 's/^/            /'
      rc=1
    fi
  fi
  benches=$(cd "$m" && grep -hoE '^func Benchmark[A-Za-z0-9_]*' "${files[@]}" 2>/dev/null | sed 's/^func //' | sort -u | tr '\n' ' ')
  if [ -n "${benches// /}" ]; then echo "  note: tagged benchmarks, measurement rigs this script does not run: $benches"; fi
done
echo "INTEGRATION_OVERALL_RC=$rc"
exit "$rc"
