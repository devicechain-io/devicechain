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
#   race: NOT COVERED backend/services/event-processing -- exempt (hack/go-race.sh): cost: ...
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
# fails on everything proves nothing either. It runs in `discover`, before the
# matrix fans out.
#
# This replaced backend/services/device-management/.github/workflows/test.yaml,
# a workflow inherited from that service's pre-monorepo repository. It ran
# `go test -race` and GitHub never triggered it: Actions reads .github/workflows
# at the repository root only, so a nested copy is an ordinary file. It had never
# run, so it had never passed and had never failed.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# The ONE command the race step runs, and the one the self-test proves. Both go
# through race_test and nothing else: a second spelling of the command is how
# `-race` could be deleted from the step while the self-test, which ran its own
# copy, stayed green.
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
# 🔴 EVENT-PROCESSING IS THE ONE THIS CANNOT AFFORD, AND IT IS NOT BECAUSE THE
# MODULE IS WRONG — it has the most concurrency of any service here, and it is the
# one you would pick first. Measured on a real runner against its own merge base,
# instrumented execution took its `go` job from 101s to 647s, the slowest job in
# the workflow by a factor of two, and the ci run from 5m42s to 15m47s, since the
# extra runner minutes also queue behind a pool that is regularly saturated. A warm
# build cache did not help: a second run measured 676s, so the cost is instrumented
# EXECUTION, not an instrumented build. Those figures predate later changes to the
# module's tests; re-measure before relying on them to keep the entry or to drop it.
#
# It is worth knowing where that time goes before anyone re-litigates the entry,
# because it is not the concurrency. Nine tests in `processor` that each stand up
# an embedded NATS server were 338s of the 354s race step, at roughly 7x their
# uninstrumented time, while the lease, sweep and dead-letter tests — the ones
# actually exercising the concurrency — are sub-second either way. So the bill is
# the FIXTURE, and a cheaper shape for those nine would bring the module inside
# budget without giving up anything the detector is for.
exempt_modules() {
  cat <<'EOF'
backend/services/event-processing|cost: instrumented, its go job went from 101s to 647s (the slowest job in the workflow by 2x) and the ci run from 5m42s to 15m47s; nine embedded-NATS tests in processor are nearly all of that
EOF
}

exempt_paths()  { exempt_modules | cut -d'|' -f1; }
exempt_reason() { exempt_modules | awk -v m="$1" 'index($0, m "|") == 1 { print substr($0, length(m) + 2) }'; }
is_exempt()     { exempt_paths | grep -qxF -- "$1"; }

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
  "$ROOT/hack/list-workspace-modules.sh" | sed 's|^\./||'
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
    if ! printf '%s\n' "$ws" | grep -qxF -- "$entry"; then
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

  mkdir -p "$tmp/racy" "$tmp/clean"
  cat > "$tmp/go.mod" <<'EOF'
module racecheckprobe

go 1.26
EOF

  # An unsynchronised read-modify-write from two goroutines. Nothing subtle: the
  # point is a report the detector is certain to produce, so that its ABSENCE is
  # unambiguous evidence the instrumentation is not on.
  cat > "$tmp/racy/racy_test.go" <<'EOF'
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

  # The counterweight. A checker that reports DATA RACE on everything would pass
  # the case above and be worthless, so the same toolchain must also come back
  # clean on the same shape done correctly.
  cat > "$tmp/clean/clean_test.go" <<'EOF'
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

  echo "==> Self-test: a known race must be REPORTED by the step's own command"
  local out rc
  # GOWORK=off so the probe is built as its own module rather than being refused
  # for sitting outside the workspace.
  set +e
  out="$(cd "$tmp" && GOWORK=off race_test ./racy 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -eq 0 ]; then
    echo "  FAIL: a program with an unsynchronised counter passed under: ${RACE_TEST[*]}" >&2
    echo "        The detector is not instrumenting this build, so every green" >&2
    echo "        race step in this workflow means nothing. Check that the command" >&2
    echo "        passes -race, that cgo is enabled (CGO_ENABLED=1) and that a C" >&2
    echo "        compiler is installed." >&2
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
  echo "  ok: DATA RACE reported, exit status $rc"

  echo "==> Self-test: correctly synchronised code must stay GREEN"
  set +e
  out="$(cd "$tmp" && GOWORK=off race_test ./clean 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -ne 0 ]; then
    echo "  FAIL: a mutex-guarded counter was reported as racy. A checker that" >&2
    echo "        fails on everything is not evidence about anything." >&2
    echo "$out" >&2
    return 1
  fi
  echo "  ok: no report on synchronised code"

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

  if ! printf '%s\n' "$(workspace_modules)" | grep -qxF -- "$module"; then
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
