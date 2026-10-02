#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Runs `go test -race` on every workspace module except a short exempt list, and
# says out loud which side of that line the module it was handed falls on.
#
#   hack/go-race.sh <module>      # run the race detector, or say why it did not
#   hack/go-race.sh --list        # coverage table for every workspace module
#   hack/go-race.sh --self-test   # prove the detector can still go red
#
# 🔴 WHY A SCRIPT AND NOT `run: go test -race ./...` BEHIND AN `if:`.
#
# The race step rides the per-module `go` matrix, so it is asked about every
# workspace module and runs on every one the exempt set does not name. A step that
# skips renders in the GitHub UI as the same green tick as a step that ran, which
# makes "this module was race checked" and "this module was never race checked"
# indistinguishable at exactly the moment somebody wants to know. So the step is
# UNCONDITIONAL and the verdict is a line in the log naming the module and the flag:
#
#   race: COVERED backend/core -- go test -race -count=1 ./...
#   race: NOT COVERED <module> -- exempt (hack/go-race.sh): <reason>
#
# The workflow greps its own output for that line, so a future edit that leaves
# the step returning 0 without deciding anything fails instead of passing.
#
# 🔴 AND WHY THE SELF-TEST IS THE LOAD-BEARING HALF. The detector is a tool that
# exits 0 when it finds nothing, which makes "no races" and "never instrumented"
# the same result. Dropping `-race` from the command, a toolchain built without
# cgo, a missing C compiler — every one of those turns this into a second, slower
# copy of `go test` that reports success forever. --self-test builds a program
# with a known race, runs it through the same command the step runs, and requires
# a DATA RACE report; it also runs a race-free control, because a checker that
# fails on everything proves nothing either. Both go through do_module, the same
# entry point the step calls, against a throwaway go.work holding the probes, so
# the self-test proves the step's own call and not a copy of it. It runs in
# `discover`, before the matrix fans out.
#
# This replaced backend/services/device-management/.github/workflows/test.yaml,
# a workflow inherited from that service's pre-monorepo repository. It ran
# `go test -race` and GitHub never triggered it: Actions reads .github/workflows
# at the repository root only, so a nested copy is an ordinary file. It had never
# run, so it had never passed and had never failed.

set -euo pipefail

HACK="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# The workspace this script reports on. The self-test points it at a throwaway
# workspace of probe modules, so everything that reads the workspace reads it
# through ROOT.
ROOT="$(cd "$HACK/.." && pwd)"

# The ONE command the race step runs. do_module runs it through race_test, and the
# self-test drives do_module itself on a module with a known race: a second
# spelling of the command in do_module is how `-race` could be deleted from the
# step while a self-test that ran its own copy stayed green, and driving the step's
# own entry point is what closes that.
#
# -count=1 for the same reason every other test gate in this repo passes it: `go
# test` does not track files outside the module, so a cached pass can survive a
# change that must fail.
RACE_TEST=(go test -race -count=1)
race_test() { "${RACE_TEST[@]}" "$@"; }

# ---------------------------------------------------------------------------
# THE EXEMPT SET — every workspace module is race checked unless it is named here
# ---------------------------------------------------------------------------
# One entry per line: <module path exactly as go.work writes it, minus "./">|<reason>.
# The reason is free prose and may itself contain "|": everything after the first
# one is the reason.
#
# 🔴 THE DEFAULT IS COVERED, AND THAT IS THE POINT. This used to be an allowlist,
# chosen by counting the `go` statements in each module's tests, and that count
# could not see a test that starts production goroutines through Initialize or
# Start. A module left off it was unraced by default and nothing said so:
# event-management's persistence tests read a mock's call list while the worker
# pool its Initialize had started appended to it, for as long as the module was
# off the list. Now a module is raced the moment it joins go.work, and leaving one
# out takes a written reason here.
#
# 🔴 A REASON IS A MEASUREMENT OR A FACT ABOUT THE MODULE, NEVER "no goroutines".
# The detector reports accesses that happen at run time; whether a module's tests
# drive concurrency is exactly the question a static count got wrong. Cost is the
# accepted reason: an entry is justified when instrumenting the module would make
# its `go` job the slowest job of the ci run, or visibly lengthen the run itself.
# Measure that on a real runner against the change's own merge base; the runners
# are noisy enough that a baseline from a different hour is not comparable.
#
# 🔴 A RED UNDER -race THAT IS NOT A DATA RACE IS NOT A REASON EITHER. The detector
# slows execution several times over, so a test that asserts a wall-clock budget
# can fail for no reason but the instrumentation. Fix the test so its budget does
# not depend on how fast the binary runs, and never exempt the module for it.
#
# Not covered here whatever this set says: `//go:build integration` tests. They run
# in the `integration` job (hack/integration-tests.sh), never under -race.
#
# 🔴 A SLOW RACE STEP IS USUALLY PAYING FOR A FIXTURE, NOT FOR CONCURRENCY. The
# instrumentation covers everything in the test binary, embedded servers and fixture
# builders included, so a fixture rebuilt per test multiplies the bill. Before reaching
# for this set: share the fixture across the package (one embedded broker, a distinct
# instance id per test — the way instances are kept apart on a production broker; an
# expensive immutable value minted once), run tests that assert no wall-clock bound in
# parallel, and shorten test-only ack waits. event-processing's processor package is the
# worked example (processor/shared_broker_test.go): it was exempt here on cost until those
# changes took its race step from about 450s to about 150s on four cores, measured
# locally, and the cost had never been its concurrency.
exempt_modules() {
  : # Empty: every workspace module is race checked. One line per entry, <path>|<reason>.
}

exempt_paths()  { exempt_modules | cut -d'|' -f1; }
exempt_reason() { exempt_modules | awk -v m="$1" 'index($0, m "|") == 1 { print substr($0, length(m) + 2) }'; }
is_exempt()     { [ -n "$1" ] && grep -qxF -- "$1" <<<"$(exempt_paths)"; }

# verdict_for <module> — COVERED or EXEMPT. do_module acts on exactly this answer,
# and the self-test asks it about every workspace module, so the default the step
# acts on is the default the self-test pins.
verdict_for() {
  if is_exempt "$1"; then echo EXEMPT; else echo COVERED; fi
}

# ---------------------------------------------------------------------------
# workspace_modules
# ---------------------------------------------------------------------------
# go.work membership, read through the toolchain-free reader so this works in a
# job with no Go installed (--list is called from one).
workspace_modules() {
  "$HACK/list-workspace-modules.sh" "$ROOT/go.work" | sed 's|^\./||'
}

# ---------------------------------------------------------------------------
# check_set_is_sane
# ---------------------------------------------------------------------------
# An exempt entry naming a module go.work no longer declares is harmless to
# coverage now — the renamed module is raced by default — but the stale entry and
# its reason would go on telling a reader something false, so it is refused. So is
# an entry with no reason, a path named twice, and an exempt set that leaves no
# module covered at all.
check_set_is_sane() {
  local ws entry missing="" covered=0 m

  ws="$(workspace_modules)"
  if [ -z "$ws" ]; then
    echo "::error::go.work declares no modules, so there is nothing to race check" >&2
    return 1
  fi

  while read -r entry; do
    [ -n "$entry" ] || continue
    if ! grep -qxF -- "$entry" <<<"$ws"; then
      missing="$missing $entry"
    fi
  done <<EOF
$(exempt_paths)
EOF
  if [ -n "$missing" ]; then
    echo "::error::the exempt set names module(s) go.work does not declare:$missing" >&2
    echo "  A module that was renamed or moved is race checked under its new path," >&2
    echo "  and the stale entry's reason now describes nothing. Fix the path or drop it." >&2
    return 1
  fi

  local unreasoned
  unreasoned="$(exempt_modules | awk -F'|' 'NF < 2 || $2 ~ /^[[:space:]]*$/ { print $1 }')"
  if [ -n "$unreasoned" ]; then
    echo "::error::exempt entries with no reason: $unreasoned" >&2
    echo "  Leaving a module out of the race detector takes a written reason." >&2
    return 1
  fi

  local dups
  dups="$(exempt_paths | sort | uniq -d)"
  if [ -n "$dups" ]; then
    echo "::error::the exempt set names a module more than once: $dups" >&2
    return 1
  fi

  while read -r m; do
    [ -n "$m" ] || continue
    if [ "$(verdict_for "$m")" = COVERED ]; then covered=$((covered + 1)); fi
  done <<EOF
$ws
EOF
  if [ "$covered" -eq 0 ]; then
    echo "::error::the exempt set covers every workspace module, so no module would be race checked at all" >&2
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# --list — the whole picture in one place
# ---------------------------------------------------------------------------
# A matrix of separate green ticks cannot tell a reader which modules are covered.
# This prints the answer once, in `discover`, where it is read as a table rather
# than inferred from the absence of a failure.
do_list() {
  check_set_is_sane
  local m covered=0 exempt=0
  while read -r m; do
    [ -n "$m" ] || continue
    if [ "$(verdict_for "$m")" = EXEMPT ]; then
      printf '  exempt  %s -- %s\n' "$m" "$(exempt_reason "$m")"
      exempt=$((exempt + 1))
    else
      printf '  race    %s\n' "$m"
      covered=$((covered + 1))
    fi
  done <<EOF
$(workspace_modules)
EOF
  echo "race detector covers $covered of $((covered + exempt)) workspace modules ($exempt exempt, reasons above)"
}

# ---------------------------------------------------------------------------
# --self-test — the standing negative control
# ---------------------------------------------------------------------------
do_self_test() {
  local tmp
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064 # expand tmp now, not at trap time
  trap "rm -rf '$tmp'" EXIT

  # A throwaway workspace of probe modules, each its own module in its own go.work
  # entry, so do_module -- the step's own entry point -- can be driven on them
  # exactly as the step drives it on a real module.
  local ws="$tmp/ws" m
  for m in racy clean skipped; do
    mkdir -p "$ws/$m"
    printf 'module racecheckprobe/%s\n\ngo 1.26\n' "$m" > "$ws/$m/go.mod"
  done
  cat > "$ws/go.work" <<'EOF'
go 1.26

use (
	./racy
	./clean
	./skipped
)
EOF

  # An unsynchronised read-modify-write from two goroutines. Nothing subtle: the
  # point is a report the detector is certain to produce, so that its ABSENCE is
  # unambiguous evidence the instrumentation is not on. `skipped` carries the same
  # race, so running an exempt module anyway is a failure too.
  for m in racy skipped; do
    cat > "$ws/$m/racy_test.go" <<'EOF'
package racy

import (
	"sync"
	"testing"
)

func TestUnsynchronisedCounter(t *testing.T) {
	var wg sync.WaitGroup
	n := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5000; j++ {
				n++
			}
		}()
	}
	wg.Wait()
	_ = n
}
EOF
  done

  # The counterweight. A checker that reports DATA RACE on everything would pass
  # the case above and be worthless, so the same toolchain must also come back
  # clean on the same shape done correctly.
  cat > "$ws/clean/clean_test.go" <<'EOF'
package clean

import (
	"sync"
	"testing"
)

func TestSynchronisedCounter(t *testing.T) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5000; j++ {
				mu.Lock()
				n++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if n != 20000 {
		t.Fatalf("counter = %d, want 20000", n)
	}
}
EOF

  # probe <exempt set> <command...> runs a command of this script against the
  # probe workspace with the given exempt set, in a subshell so neither leaks.
  # GOWORK is unset so the go command finds the probe's go.work from the module
  # directory, as it finds the real one in CI.
  probe() {
    local set="$1"
    shift
    (
      # shellcheck disable=SC2030 # local to this subshell on purpose
      ROOT="$ws"
      # shellcheck disable=SC2317 # called indirectly, through the command run below
      exempt_modules() { printf '%s\n' "$set"; }
      unset GOWORK
      "$@"
    )
  }
  local probe_set='skipped|probe: exempt on purpose'

  echo "==> Self-test: the step's entry point must REPORT a known race"
  local out rc
  set +e
  out="$(probe "$probe_set" do_module racy 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -eq 0 ]; then
    echo "  FAIL: a module with an unsynchronised counter passed the race step:" >&2
    echo "        ${RACE_TEST[*]}" >&2
    echo "        The detector is not instrumenting this build, so every green" >&2
    echo "        race step in this workflow means nothing. Check that do_module" >&2
    echo "        runs race_test, that RACE_TEST passes -race, that cgo is enabled" >&2
    echo "        (CGO_ENABLED=1) and that a C compiler is installed." >&2
    echo "$out" >&2
    return 1
  fi
  case "$out" in
    *"DATA RACE"*) ;;
    *)
      echo "  FAIL: the probe failed, but not with a DATA RACE report -- so this" >&2
      echo "        self-test would also 'pass' on an unrelated build failure." >&2
      echo "$out" >&2
      return 1
      ;;
  esac
  case "$out" in
    *"race: COVERED racy -- "*) ;;
    *)
      echo "  FAIL: the race step failed on the probe without its COVERED verdict line." >&2
      echo "$out" >&2
      return 1
      ;;
  esac
  echo "  ok: DATA RACE reported, exit status $rc"

  echo "==> Self-test: correctly synchronised code must stay GREEN"
  set +e
  out="$(probe "$probe_set" do_module clean 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -ne 0 ]; then
    echo "  FAIL: a mutex-guarded counter was reported as racy. A checker that" >&2
    echo "        fails on everything is not evidence about anything." >&2
    echo "$out" >&2
    return 1
  fi
  echo "  ok: no report on synchronised code"

  echo "==> Self-test: an exempt module is NOT run, and says so"
  set +e
  out="$(probe "$probe_set" do_module skipped 2>&1)"
  rc=$?
  set -e
  case "$out" in
    *"race: NOT COVERED skipped -- exempt (hack/go-race.sh): probe: exempt on purpose"*) ;;
    *)
      echo "  FAIL: an exempt module did not print its NOT COVERED verdict:" >&2
      echo "$out" >&2
      return 1
      ;;
  esac
  if [ "$rc" -ne 0 ]; then
    echo "  FAIL: an exempt module (whose tests race) was run anyway, exit status $rc:" >&2
    echo "$out" >&2
    return 1
  fi
  echo "  ok: NOT COVERED, not run"

  echo "==> Self-test: an exempt path matches only the module it names"
  # A module whose path is a substring of an exempt path is still covered: the
  # match is exact, or an exempt entry would silently exempt its neighbours.
  if [ "$(probe "$probe_set" verdict_for skip)" != COVERED ]; then
    echo "  FAIL: 'skip' was treated as exempt because the exempt set names 'skipped'." >&2
    return 1
  fi
  echo "  ok: exact match"

  # 🔴 THE EARLY-CLOSED PIPE. Membership used to be `printf list | grep -q`, and
  # grep -q stops reading at its match: the writer's next write then fails, and
  # under pipefail that failure read as "not in the list". On a 29-line go.work
  # that was a rare CI flake. With 1 MiB after the match -- far beyond a pipe's
  # 64 KiB plus grep's read-ahead -- the writer ALWAYS outlives the reader, so
  # the old code fails these cases every time, under either SIGPIPE disposition.
  # Long lines rather than many: check_set_is_sane forks once per module.
  local long pad_ws pad_set
  long="$(printf '%*s' 16384 '' | tr ' ' x)"
  pad_lines() { local i; for ((i = 0; i < 64; i++)); do printf 'padding/%s-%02d\n' "$long" "$i"; done; }
  pad_ws="$(printf 'skipped\nclean\n'; pad_lines)"
  pad_set="$(printf '%s\n' "$probe_set"; pad_lines | sed 's/$/|padding/')"
  # probe_padded runs a probe whose go.work view is pad_ws; stub_race stands in
  # for the go test run, which the DATA RACE case above already pins.
  probe_padded() { local set="$1"; shift; probe "$set" with_padded_ws "$@"; }
  # shellcheck disable=SC2317 # called indirectly, through probe
  with_padded_ws() {
    # shellcheck disable=SC2317 # called indirectly, through the command run below
    workspace_modules() { printf '%s\n' "$pad_ws"; }
    "$@"
  }
  # shellcheck disable=SC2317 # called indirectly, through probe
  stub_race() {
    # shellcheck disable=SC2317 # called indirectly, by do_module
    race_test() { echo "race_test stubbed: $*"; }
    "$@"
  }

  echo "==> Self-test: the padded fixture still closes a pipe early (premise)"
  # The one place the old shape is written on purpose. It runs in a child shell
  # fed by a quoted heredoc, because the early-closing-pipe lint in
  # hack/shellcheck.sh does not read heredoc bodies; the fixture goes through a
  # FILE because a single argument or environment string is capped at 128 KiB,
  # and that E2BIG would be a non-zero status for the wrong reason.
  printf '%s\n' "$pad_ws" >"$tmp/pad_ws"
  if bash -o pipefail -s "$tmp/pad_ws" 2>/dev/null <<'PREMISE'
printf '%s\n' "$(<"$1")" | grep -qxF -- skipped
PREMISE
  then
    echo "  FAIL: the padded fixture no longer closes a pipe early on this runner, so the" >&2
    echo "        cases below would pass against the old piped membership test too." >&2
    return 1
  fi
  echo "  ok: piping the padded list into grep -q reads a listed module as absent"

  echo "==> Self-test: a declared module is found however early the reader stops"
  set +e
  out="$(probe_padded "" stub_race do_module clean 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -ne 0 ] || [[ "$out" == *"::error::"* ]] || [[ "$out" != *"race: COVERED clean -- "* ]] || [[ "$out" != *"race_test stubbed: ./..."* ]]; then
    echo "  FAIL: a module go.work declares was not raced (exit $rc):" >&2
    echo "$out" | sed -n '1,5p' | cut -c1-200 >&2
    return 1
  fi
  echo "  ok: COVERED and run"

  echo "==> Self-test: an exempt module stays exempt however early the reader stops"
  out="$(probe "$pad_set" verdict_for skipped)"
  if [ "$out" != EXEMPT ]; then
    echo "  FAIL: an exempt module's verdict was '$out', not EXEMPT -- an early-closed" >&2
    echo "        pipe read as 'not exempt', which races a module on purpose left out." >&2
    return 1
  fi
  echo "  ok: EXEMPT"

  echo "==> Self-test: the sanity check finds an exempt entry however early the reader stops"
  # check_set_is_sane is asked directly as well as through do_module: these
  # captures run under set +e, where do_module carries on past a failed sanity
  # check, so its verdict line alone cannot show that the check passed.
  set +e
  out="$(probe_padded "$probe_set" check_set_is_sane 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -ne 0 ] || [ -n "$out" ]; then
    echo "  FAIL: the sanity check refused an exempt entry go.work declares (exit $rc):" >&2
    echo "$out" | sed -n '1,5p' | cut -c1-200 >&2
    return 1
  fi
  set +e
  out="$(probe_padded "$probe_set" do_module skipped 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -ne 0 ] || [[ "$out" == *"::error::"* ]] || [[ "$out" != *"race: NOT COVERED skipped -- exempt (hack/go-race.sh): probe: exempt on purpose"* ]]; then
    echo "  FAIL: an exempt entry go.work declares was refused or not reported (exit $rc):" >&2
    echo "$out" | sed -n '1,5p' | cut -c1-200 >&2
    return 1
  fi
  echo "  ok: NOT COVERED, not refused"

  # The counterweights: finding a declared module must not have become finding
  # anything. Each is refused, and none reaches the test run.
  local bad
  for bad in not-declared skippe skip.ed; do
    echo "==> Self-test: '$bad' is not a module go.work declares, and is refused"
    set +e
    out="$(probe_padded "" stub_race do_module "$bad" 2>&1)"
    rc=$?
    set -e
    if [ "$rc" -eq 0 ] || [[ "$out" != *"'$bad' is not a module go.work declares"* ]] || [[ "$out" == *"race_test stubbed"* ]]; then
      echo "  FAIL: '$bad' was not refused as undeclared (exit $rc):" >&2
      echo "$out" | sed -n '1,5p' | cut -c1-200 >&2
      return 1
    fi
    echo "  ok: refused"
  done

  echo "==> Self-test: an exempt set that names a module twice is refused"
  set +e
  out="$(probe "$probe_set
skipped|probe: named again" check_set_is_sane 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -eq 0 ] || [[ "$out" != *"names a module more than once: skipped"* ]]; then
    echo "  FAIL: a duplicated exempt entry was not refused as a duplicate (exit $rc):" >&2
    echo "$out" >&2
    return 1
  fi
  echo "  ok: refused"

  echo "==> Self-test: the step's command passes -race"
  case " ${RACE_TEST[*]} " in
    *" -race "*) ;;
    *)
      echo "  FAIL: the race step's command does not pass -race: ${RACE_TEST[*]}" >&2
      return 1
      ;;
  esac
  echo "  ok: ${RACE_TEST[*]}"

  echo "==> Self-test: a module this script has never heard of is COVERED"
  if [ "$(verdict_for "backend/services/__a-module-added-tomorrow__")" != COVERED ]; then
    echo "  FAIL: an unnamed module was treated as exempt; the default must be covered." >&2
    return 1
  fi
  local m exempt_seen=0
  while read -r m; do
    [ -n "$m" ] || continue
    if is_exempt "$m"; then
      exempt_seen=$((exempt_seen + 1))
      [ "$(verdict_for "$m")" = EXEMPT ] || {
        echo "  FAIL: $m is in the exempt set but its verdict is not EXEMPT" >&2
        return 1
      }
    elif [ "$(verdict_for "$m")" != COVERED ]; then
      echo "  FAIL: $m is not in the exempt set but its verdict is not COVERED" >&2
      return 1
    fi
  done <<EOF
$(workspace_modules)
EOF
  echo "  ok: every workspace module outside the exempt set is COVERED ($exempt_seen exempt)"

  echo "==> Self-test: the exempt set names only modules go.work declares, each with a reason"
  check_set_is_sane
  echo "  ok: $(exempt_modules | wc -l | tr -d ' ') exempt entries, each in go.work with a reason"
  return 0
}

# ---------------------------------------------------------------------------
# The per-module entry point.
# ---------------------------------------------------------------------------
do_module() {
  local module="$1"
  check_set_is_sane

  # 🔴 NEVER PIPE A LIST INTO grep -q HERE. In a pipeline, printf writes the list
  # one line at a time and grep -q exits at its first match, so the write after
  # the match fails (EPIPE where SIGPIPE is ignored, as on GitHub's runners, or a
  # SIGPIPE death elsewhere) and pipefail made that failure the pipeline's answer:
  # "declared, and grep stopped reading" became "not declared". That failed the
  # go job of backend/tools/credguard, a module nobody had touched, with
  # `printf: write error: Broken pipe`. The list is captured first, in its own
  # assignment so a lister failure aborts here under set -e rather than reading
  # as "not declared", and grep reads it from a here-string, which is written in
  # full before grep starts. The self-test case "a declared module is found
  # however early the reader stops" reproduces the old failure every time.
  # The pattern must be non-empty: a here-string ends in a newline, so `-x ''`
  # would match the empty line an empty list becomes. The dispatch below
  # refuses an empty argument.
  local ws
  ws="$(workspace_modules)"
  if ! grep -qxF -- "$module" <<<"$ws"; then
    echo "::error::'$module' is not a module go.work declares; refusing to report on it" >&2
    exit 1
  fi

  if [ "$(verdict_for "$module")" = EXEMPT ]; then
    # 🔴 The wording is deliberate. This step's tick is green either way, so the
    # log line is the only place the difference exists -- do not soften it into
    # something that could be read as "checked, nothing found".
    echo "race: NOT COVERED $module -- exempt (hack/go-race.sh): $(exempt_reason "$module")"
    return 0
  fi

  echo "race: COVERED $module -- ${RACE_TEST[*]} ./..."
  # shellcheck disable=SC2031 # the self-test's subshell override is meant not to leak here
  cd "$ROOT/$module"
  race_test ./...
}

case "${1:-}" in
  --self-test) do_self_test ;;
  --list)      do_list ;;
  "")          echo "usage: $0 <module> | --list | --self-test" >&2; exit 2 ;;
  -*)          echo "usage: $0 <module> | --list | --self-test" >&2; exit 2 ;;
  *)           do_module "$1" ;;
esac
