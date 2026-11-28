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
#   hack/integration-tests.sh --matrix     # print CI legs using cheap discovery only
#   hack/integration-tests.sh --modules "backend/core"  # run only these discovered modules
#   hack/integration-tests.sh --self-test  # prove the test and plan guards (no docker or go)
# --modules and --no-container can be combined in either order.
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

# These modules each take minutes, so each gets its own CI leg; everything else shares one
# "small" leg. This list decides GROUPING, never coverage: an unlisted module still runs there.
HEAVY=(backend/core backend/services/device-management backend/services/event-management backend/services/user-management backend/services/event-processing)

# Compare independently derived newline lists and name every coverage difference.
check_plan_covers() {
  local missing extra
  missing=$(comm -23 <(printf '%s\n' "$2" | sed '/^$/d' | sort -u) <(printf '%s\n' "$1" | sed '/^$/d' | sort -u))
  extra=$(comm -13 <(printf '%s\n' "$2" | sed '/^$/d' | sort -u) <(printf '%s\n' "$1" | sed '/^$/d' | sort -u))
  [ -z "$missing" ] || printf 'discovered but unplanned: %s\n' "$missing"
  [ -z "$extra" ] || printf 'planned but undiscovered: %s\n' "$extra"
  [ -z "$missing$extra" ]
}

# Pure grouping of discovered workspace-relative modules; coverage is checked before JSON leaves.
build_plan() {
  local h m found heavy name cache plan planned discovered
  local legs=() small=()
  [ $# -gt 0 ] || { echo "no discovered integration modules; refusing an empty plan" >&2; return 1; }
  discovered=$(printf '%s\n' "$@")
  for h in "${HEAVY[@]}"; do
    found=0
    for m in "$@"; do [ "$m" != "$h" ] || found=1; done
    [ "$found" = 1 ] || { echo "stale HEAVY entry: $h lost its tagged tests or moved; edit HEAVY" >&2; return 1; }
    cache=$(printf '%s/go.mod\n%s/go.sum' "$h" "$h")
    legs+=("$(jq -cn --arg name "${h##*/}" --arg modules "$h" --arg cache "$cache" '{name:$name,modules:$modules,cache:$cache}')") || return 1
  done
  for m in "$@"; do
    heavy=0
    for h in "${HEAVY[@]}"; do [ "$m" != "$h" ] || heavy=1; done
    [ "$heavy" = 1 ] || small+=("$m")
  done
  if [ ${#small[@]} -gt 0 ]; then
    cache=$(for m in "${small[@]}"; do printf '%s/go.mod\n%s/go.sum\n' "$m" "$m"; done)
    legs+=("$(jq -cn --arg modules "${small[*]}" --arg cache "$cache" '{name:"small",modules:$modules,cache:$cache}')") || return 1
  fi
  plan=$(printf '%s\n' "${legs[@]}" | jq -cs '.') || return 1
  planned=$(jq -r '.[].modules | split(" ")[]' <<<"$plan") || return 1
  check_plan_covers "$planned" "$discovered" >&2 || return 1
  printf '%s\n' "$plan"
}

CONTAINER=1
MODE=run
SELECTED=0
requested=()
while [ $# -gt 0 ]; do
  case "$1" in
    --no-container) CONTAINER=0 ;;
    --self-test) MODE=self-test ;;
    --matrix) MODE=matrix ;;
    --modules)
      [ $# -ge 2 ] || { echo "--modules needs a value" >&2; exit 1; }
      shift
      read -r -a requested <<<"$1"
      [ ${#requested[@]} -gt 0 ] || { echo "--modules must not be empty" >&2; exit 1; }
      SELECTED=1 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
  shift
done

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

# 🔴 ONLY THE PACKAGES THE TAG CHANGES, AND THAT IS NOT A CUT. The tag is a build constraint and
# nothing else: a package whose test binary links no file the tag adds or removes (in itself, its
# tests, or any package it links) compiles to the SAME binary with `-tags integration` as without
# it, and the `go` job already runs that binary for every package of every module, under -race as
# well. Running it here again only bought time: integration (core) spent ~19 of its 21 minutes
# re-running untagged packages (core/messaging alone ~17) and was the last job of the run to finish.
# The environment is no difference either: the only thing this script adds is the DC_IT_PG*
# server, and every reader of those variables is itself an integration-tagged file.
#
# tag_changed <on> <off> <roots> is pure, so --self-test can drive it: <on> and <off> are
# `ImportPath|files` lines of `go list -deps -test` with and without the tag (GoFiles of a test
# variant `p [p.test]` carry its test files), <roots> is `ImportPath|dep,dep` lines of
# `go list -test` with the tag. A listing that differs is a changed package, matched by its exact
# import path: a changed TEST variant `p [p.test]` changes only p's own binary, never that of a
# package linking plain p. It prints each package whose test main `p.test` is, or links, a changed one.
tag_changed() {
  awk -F'|' '
    FILENAME == ARGV[1] { on[$0] = 1; next }
    FILENAME == ARGV[2] { off[$0] = 1; next }
    !ready {
      for (k in on) if (!(k in off)) { split(k, a, "|"); changed[a[1]] = 1 }
      for (k in off) if (!(k in on)) { split(k, a, "|"); changed[a[1]] = 1 }
      ready = 1
    }
    $1 ~ /\.test$/ {
      hit = ($1 in changed)
      n = split($2, d, ",")
      for (i = 1; i <= n && !hit; i++) if (d[i] in changed) hit = 1
      if (hit) { p = $1; sub(/\.test$/, "", p); print p }
    }' "$1" "$2" "$3" | sort -u
}

# tag_changed_here runs tag_changed on the module in the current directory; it fails when the
# package loader does.
tag_changed_here() {
  local t rc=0 fmt='{{.ImportPath}}|{{if .Error}}error {{end}}{{.GoFiles}}{{.CgoFiles}}'
  t=$(mktemp -d) || return 1
  { go list -e -deps -test -tags integration -f "$fmt" ./... > "$t/on.raw" &&
    go list -e -deps -test -f "$fmt" ./... > "$t/off.raw" &&
    go list -e -test -tags integration -f '{{.ImportPath}}|{{join .Deps ","}}' ./... > "$t/roots.raw" &&
    tr -d '\r' < "$t/on.raw" > "$t/on" && tr -d '\r' < "$t/off.raw" > "$t/off" && tr -d '\r' < "$t/roots.raw" > "$t/roots" &&
    tag_changed "$t/on" "$t/off" "$t/roots"; } || rc=1
  rm -rf "$t"
  return "$rc"
}

# The negative control, in the shape the other hack/ gates use: a guard is worth nothing until it
# has been shown to fire. This one exists because a `-run` filter that matches nothing exits 0 with
# "no tests to run", which is indistinguishable from a pass — the exact way the first draft of this
# script reported success while skipping 5 of 13 tagged tests.
if [ "$MODE" = "self-test" ]; then
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
  # Plan counterweights prove grouping, ordering and cache inputs, not merely rejection.
  plan=$(build_plan "${HEAVY[@]}" backend/other-one backend/other-two) || exit 1
  jq -e 'length == 6 and (map(.name) == ["core","device-management","event-management","user-management","event-processing","small"]) and (.[5].modules == "backend/other-one backend/other-two") and (.[5].cache == "backend/other-one/go.mod\nbackend/other-one/go.sum\nbackend/other-two/go.mod\nbackend/other-two/go.sum")' <<<"$plan" >/dev/null || { echo "self-test FAILED: mixed plan"; exit 1; }
  plan=$(build_plan "${HEAVY[@]}") || exit 1
  jq -e 'length == 5 and all(.name != "small")' <<<"$plan" >/dev/null || { echo "self-test FAILED: heavy-only plan"; exit 1; }
  if out=$(build_plan "${HEAVY[@]:1}" 2>&1); then echo "self-test FAILED: stale HEAVY accepted"; exit 1; fi
  [[ "$out" == *"${HEAVY[0]}"* ]] || { echo "self-test FAILED: stale entry not named"; exit 1; }
  if build_plan >/dev/null 2>&1; then echo "self-test FAILED: empty plan accepted"; exit 1; fi
  out=$(check_plan_covers $'one\ntwo' $'two\none') || { echo "self-test FAILED: identical coverage rejected"; exit 1; }
  [ -z "$out" ] || { echo "self-test FAILED: identical coverage noisy"; exit 1; }
  if out=$(check_plan_covers one $'one\nmissing'); then echo "self-test FAILED: missing module accepted"; exit 1; fi
  [[ "$out" == *missing* ]] || { echo "self-test FAILED: missing module not named"; exit 1; }
  if out=$(check_plan_covers $'one\nextra' one); then echo "self-test FAILED: extra module accepted"; exit 1; fi
  [[ "$out" == *extra* ]] || { echo "self-test FAILED: extra module not named"; exit 1; }
  # The package selection. Selected: x/a (a tagged test file), x/b (links x/h, whose tagged NON-test
  # file the tag adds) and x/o (a package that has tests only under the tag). Not selected: x/c,
  # which links plain x/a — only x/a's TEST variant changed, and a match by package path rather
  # than exact import path selected 12 of core's 99 packages instead of 5 — nor x/a's plain line.
  t=$(mktemp -d)
  printf '%s\n' 'x/a|[a.go]' 'x/a [x/a.test]|[a.go a_test.go a_it_test.go]' 'x/a.test|[_testmain.go]' \
    'x/h|[h.go h_pg.go]' 'x/b|[b.go]' 'x/b [x/b.test]|[b.go b_test.go]' 'x/b.test|[_testmain.go]' \
    'x/c|[c.go]' 'x/c [x/c.test]|[c.go c_test.go]' 'x/c.test|[_testmain.go]' \
    'x/o_test [x/o.test]|[o_it_test.go]' 'x/o.test|[_testmain.go]' > "$t/on"
  printf '%s\n' 'x/a|[a.go]' 'x/a [x/a.test]|[a.go a_test.go]' 'x/a.test|[_testmain.go]' \
    'x/h|[h.go]' 'x/b|[b.go]' 'x/b [x/b.test]|[b.go b_test.go]' 'x/b.test|[_testmain.go]' \
    'x/c|[c.go]' 'x/c [x/c.test]|[c.go c_test.go]' 'x/c.test|[_testmain.go]' > "$t/off"
  printf '%s\n' 'x/a|fmt' 'x/a.test|x/a [x/a.test],fmt' 'x/b.test|x/b [x/b.test],x/h,fmt' \
    'x/c.test|x/c [x/c.test],x/a,fmt' 'x/o.test|x/o_test [x/o.test],x/o' > "$t/roots"
  got=$(tag_changed "$t/on" "$t/off" "$t/roots" | tr '\n' ' ')
  [ "$got" = "x/a x/b x/o " ] || { echo "self-test FAILED: tag_changed selected [$got], want [x/a x/b x/o ]"; rm -rf "$t"; exit 1; }
  # The counterweight: when the tag changes nothing, nothing is selected (a selection that always
  # named every package would pass the check above as a no-op).
  cp "$t/off" "$t/off2"
  got=$(tag_changed "$t/off" "$t/off2" "$t/roots")
  [ -z "$got" ] || { echo "self-test FAILED: tag_changed selected [$got] when the tag changes nothing"; rm -rf "$t"; exit 1; }
  rm -rf "$t"
  echo "self-test passed: no-tests-ran, per-test, plan and package-selection checks reject bad inputs and accept real coverage"
  exit 0
fi

root=$(git rev-parse --show-toplevel) || exit 1
cd "$root" || exit 1
# go list -m enumerates the workspace without loading packages or downloading their dependencies.
workspace=$(go list -m -f '{{.Dir}}') || exit 1
workspace=${workspace//$'\r'/}
modules=()
while IFS= read -r m; do
  [ -n "$m" ] || continue
  m=${m//\\//}
  ( cd "$m" && [ -n "$(tagged_files)" ] ) || continue
  modules+=("${m#"$root"/}")
done <<<"$workspace"
if [ "$MODE" = matrix ]; then
  build_plan "${modules[@]}"
  exit $?
fi

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
  trap 'docker rm -f "$NAME" >/dev/null 2>&1' EXIT
  # TCP avoids the entrypoint's socket-only temporary init server. Require a real query too.
  start_and_wait() {
    docker run -d --name "$NAME" -e POSTGRES_PASSWORD=postgres -P "$IMAGE" >/dev/null || return 1
    DC_IT_PGPORT=$(docker port "$NAME" 5432/tcp | sed -n '1s/.*://p')
    for _ in $(seq 1 90); do
      if docker exec "$NAME" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1 &&
         [ "$(docker exec "$NAME" psql -h 127.0.0.1 -U postgres -tAc 'select 1' 2>/dev/null)" = 1 ]; then
        return 0
      fi
      sleep 2
    done
    return 1
  }
  ready=0
  for attempt in 1 2; do
    echo "starting postgres (attempt $attempt of 2)"
    if start_and_wait; then ready=1; break; fi
    # 🔴 ON FAILURE, KEEP THE EVIDENCE before replacing the failed container.
    docker ps -a --filter "name=$NAME"
    docker inspect -f '{{.State.Status}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}}' "$NAME"
    docker logs "$NAME" 2>&1 | tail -80
    docker rm -f "$NAME" >/dev/null 2>&1
  done
  [ "$ready" = 1 ] || { echo "postgres never became ready"; exit 1; }
fi

export DC_IT_PGPORT="${DC_IT_PGPORT:-5432}"
export DC_IT_PGHOST="${DC_IT_PGHOST:-localhost}"
export DC_IT_PGUSER="${DC_IT_PGUSER:-postgres}"
export DC_IT_PGPASSWORD="${DC_IT_PGPASSWORD:-postgres}"
echo "postgres on ${DC_IT_PGHOST}:${DC_IT_PGPORT}"

# The workspace enumerates its own modules, so a module that GAINS an integration test is covered
# the day it does — no list here to fall out of step with go.work.
# Cross-check the count against a second, independent parse, the way CI's go-module discovery does:
# a loose matcher above would otherwise shrink the set with no one the wiser. This one asks the
# package loader instead of the text: a module is integration-gated iff `go list` sees more test
# files with the tag than without it.
want=0
while IFS= read -r m; do
  tw=$(cd "$m" && go list -tags integration -f '{{len .TestGoFiles}} {{len .XTestGoFiles}}' ./... 2>/dev/null | awk '{s+=$1+$2} END{print s+0}')
  to=$(cd "$m" && go list -f '{{len .TestGoFiles}} {{len .XTestGoFiles}}' ./... 2>/dev/null | awk '{s+=$1+$2} END{print s+0}')
  if [ "$tw" -gt "$to" ]; then want=$((want + 1)); fi
done <<<"$workspace"
if [ "$want" -ne ${#modules[@]} ]; then
  echo "module discovery disagrees: grep found ${#modules[@]} integration-tagged modules, go list found $want"
  exit 1
fi
if [ ${#modules[@]} -eq 0 ]; then
  echo "no module carries an integration-tagged test; this script would be a no-op that reports success"
  exit 1
fi

# 🔴 Each leg checks the WHOLE plan against fresh, package-loader-cross-checked discovery.
if [ "${DC_IT_PLAN+x}" = x ]; then
  planned=$(jq -er '[.[].modules | split(" ")[]] | join("\n")' <<<"$DC_IT_PLAN") || exit 1
  check_plan_covers "$planned" "$(printf '%s\n' "${modules[@]}")" >&2 || exit 1
fi
if [ "$SELECTED" = 1 ]; then
  for m in "${requested[@]}"; do
    found=0
    for discovered in "${modules[@]}"; do [ "$m" != "$discovered" ] || found=1; done
    [ "$found" = 1 ] || { echo "requested module is not integration-discovered: $m" >&2; exit 1; }
  done
  modules=("${requested[@]}")
fi

rc=0
for m in "${modules[@]}"; do
  name=$m
  m="$root/$m"
  echo "=== $name ==="
  # The packages whose test binary the tag changes (tag_changed, above). A module here carries a
  # tagged test file by construction, so selecting nothing is this script failing, not a pass. If
  # the package loader itself fails, the whole module runs, as it did before the selection existed.
  pkgs=()
  if sel=$(cd "$m" && tag_changed_here); then
    while IFS= read -r p; do [ -n "$p" ] && pkgs+=("$p"); done <<<"$sel"
    total=$(cd "$m" && go list ./... 2>/dev/null | grep -c . || true)
    if [ ${#pkgs[@]} -eq 0 ]; then
      echo "  FAILED: $name carries integration-tagged files, but no package's test binary changes under the tag"
      rc=1
      continue
    fi
    echo "  packages: ${#pkgs[@]} of $total change under the tag (the go job runs the other $((total - ${#pkgs[@]})), whose test binary is the same without it): ${pkgs[*]}"
  else
    echo "::warning::could not resolve which packages of $name the integration tag changes; running all of them"
    pkgs=(./...)
  fi
  # -timeout: go test's default of 10 minutes is per PACKAGE, and event-management's model
  # package builds tables of hundreds of chunks to test the chunk ceiling of its startup
  # migrations, which takes several minutes on its own. A package that hits the default dies
  # with a panic naming whichever test happened to be running, which reads as that test's fault.
  out=$(cd "$m" && go test -tags integration -count=1 -p 2 -timeout 30m -v "${pkgs[@]}" 2>&1)
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
