#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Runs `go test -race` on the workspace modules that carry real concurrency, and
# says out loud which side of that line the module it was handed falls on.
#
#   hack/go-race.sh <module>      # run the race detector, or say why it did not
#   hack/go-race.sh --list        # coverage table for every workspace module
#   hack/go-race.sh --self-test   # prove the detector can still go red
#
# 🔴 WHY A SCRIPT AND NOT `run: go test -race ./...` BEHIND AN `if:`.
#
# The race step rides the per-module `go` matrix, so it is asked about all ~27
# workspace modules and runs on a handful. A step that skips renders in the GitHub
# UI as the same green tick as a step that ran, which makes "this module was race
# checked" and "this module was never race checked" indistinguishable at exactly
# the moment somebody wants to know. So the step is UNCONDITIONAL and the verdict
# is a line in the log naming the module and the flag:
#
#   race: COVERED backend/core -- go test -race -count=1 ./...
#   race: NOT COVERED backend/services/mcp -- not in the race set
#
# The workflow greps its own output for that line, so a future edit that leaves
# the step returning 0 without deciding anything fails instead of passing.
#
# 🔴 AND WHY THE SELF-TEST IS THE LOAD-BEARING HALF. The detector is a tool that
# exits 0 when it finds nothing, which makes "no races" and "never instrumented"
# the same result. Dropping `-race` from the command, a toolchain built without
# cgo, a missing C compiler — every one of those turns this into a second, slower
# copy of `go test` that reports success forever. --self-test builds a program
# with a known race, runs it through the same toolchain, and requires a DATA RACE
# report; it also runs a race-free control, because a checker that fails on
# everything proves nothing either. It runs in `discover`, before the matrix
# fans out.
#
# This replaced backend/services/device-management/.github/workflows/test.yaml,
# a workflow inherited from that service's pre-monorepo repository. It ran
# `go test -race` and GitHub never triggered it: Actions reads .github/workflows
# at the repository root only, so a nested copy is an ordinary file. It had never
# run, so it had never passed and had never failed.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ---------------------------------------------------------------------------
# THE RACE SET
# ---------------------------------------------------------------------------
# Paths exactly as go.work writes them, minus the leading "./" — the same strings
# the ci.yml `go` matrix carries.
#
# Chosen by measuring goroutine surface across all 27 workspace modules
# (goroutines spawned in non-test code, goroutines spawned by the TESTS, and
# references to sync primitives) and then timing each candidate both ways.
#
# 🔑 BOTH HALVES OF THE SURFACE MATTER, AND THE SECOND IS THE ONE THAT DECIDES.
# The detector reports on accesses that actually happen — it is not a static
# analysis — so a module that ships plenty of concurrency but whose tests never
# start a second goroutine cannot produce a report however long it is
# instrumented for. event-management (6 production goroutines, 0 in tests),
# notification-management (4/0) and outbound-connectors (6/0) are all in that
# position, and outbound-connectors would be the most expensive module in the
# workspace to instrument because of its Bento dependency tree.
#
#   module                    go(prod)  go(test)  sync   plain -> race
#   backend/core                    18        66    53    165s -> 168s
#   .../event-processing            14        21    22     69s -> 386s
#   .../event-sources                9        13    19     39s ->  41s
#   .../lwm2m-ingest                12        15    39      8s ->  10s
#   .../command-delivery             3        10    17      5s ->  59s
#   ---- above this line: in the set ----------------------------------
#   .../device-management            7         3    11      8s ->  69s
#   sims/dc-simulator               15         2    33      7s ->  14s
#   .../sparkplug-ingest             6         2    11      2s ->   2s
#   edge/dc-edge-agent               4         2     6     30s ->  37s
#   .../device-state                 3         3     4      1s ->   4s
#
# (`go test -count=1 -p 4 ./...`, warm build cache, 20-core host. Re-measure
# the surface with:
#   grep -rhE '^[[:space:]]*go (func|[a-zA-Z_])' <module> --include='*.go' )
#
# The line falls where the tests stop driving the concurrency hard: the five
# above it are the modules whose own suites spin up ten or more goroutines, and
# they are also where every data race this repository has actually found has
# lived. dc-simulator is the interesting near-miss — 15 production goroutines,
# more than anything but core — but its tests start two, and it is a load
# generator rather than a service on the data path. Adding it costs 7s, so the
# argument for it is cheap to revisit.
#
# 🔴 THE COST IS THE REASON THIS IS A SET AND NOT THE WHOLE MATRIX, and it is
# concentrated in one package. Instrumented builds are separate build-cache
# entries and instrumented tests run slower — mostly by a little, but
# event-processing's `processor` suite goes from 66s to 378s on its own, which
# is the single largest line item here and the one to look at first if this ever
# needs to get cheaper.
race_modules() {
  cat <<'EOF'
backend/core
backend/services/command-delivery
backend/services/event-processing
backend/services/event-sources
backend/services/lwm2m-ingest
EOF
}

# ---------------------------------------------------------------------------
# is_workspace_module <path>
# ---------------------------------------------------------------------------
# go.work membership, read through the toolchain-free reader so this works in a
# job with no Go installed (--list is called from one).
workspace_modules() {
  "$ROOT/hack/list-workspace-modules.sh" | sed 's|^\./||'
}

# ---------------------------------------------------------------------------
# check_set_is_sane
# ---------------------------------------------------------------------------
# 🔴 A race-set entry naming a module that no longer exists is the failure this
# guards, and it is silent in the direction that matters: the module is renamed
# or moved, its matrix entry keeps running, its race entry matches nothing, and
# every tick stays green while coverage has quietly gone to zero. An empty set is
# the same failure with the volume turned up.
check_set_is_sane() {
  local ws set_entry missing=""
  ws="$(workspace_modules)"

  if [ -z "$(race_modules)" ]; then
    echo "::error::the race set is empty, so no module would be race checked at all" >&2
    return 1
  fi

  while read -r set_entry; do
    [ -n "$set_entry" ] || continue
    if ! printf '%s\n' "$ws" | grep -qxF "$set_entry"; then
      missing="$missing $set_entry"
    fi
  done <<EOF
$(race_modules)
EOF

  if [ -n "$missing" ]; then
    echo "::error::the race set names module(s) go.work does not declare:$missing" >&2
    echo "  Those entries match nothing, so they are race checked by nobody while" >&2
    echo "  their matrix entries still report success. Fix the path or drop it." >&2
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# --list — the whole picture in one place
# ---------------------------------------------------------------------------
# 27 separate green ticks cannot tell a reader which modules are covered. This
# prints the answer once, in `discover`, where it is read as a table rather than
# inferred from the absence of a failure.
do_list() {
  check_set_is_sane
  local m covered=0 uncovered=0
  while read -r m; do
    [ -n "$m" ] || continue
    if printf '%s\n' "$(race_modules)" | grep -qxF "$m"; then
      printf '  race    %s\n' "$m"
      covered=$((covered + 1))
    else
      printf '  --      %s\n' "$m"
      uncovered=$((uncovered + 1))
    fi
  done <<EOF
$(workspace_modules)
EOF
  echo "race detector covers $covered of $((covered + uncovered)) workspace modules"
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

  echo "==> Self-test: a known race must be REPORTED"
  local out rc
  # GOWORK=off so the probe is built as its own module rather than being refused
  # for sitting outside the workspace.
  set +e
  out="$(cd "$tmp" && GOWORK=off go test -race -count=1 ./racy 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -eq 0 ]; then
    echo "  FAIL: a program with an unsynchronised counter passed under -race." >&2
    echo "        The detector is not instrumenting this build, so every green" >&2
    echo "        race step in this workflow means nothing. Check that cgo is" >&2
    echo "        enabled (CGO_ENABLED=1) and a C compiler is installed." >&2
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
  out="$(cd "$tmp" && GOWORK=off go test -race -count=1 ./clean 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -ne 0 ]; then
    echo "  FAIL: a mutex-guarded counter was reported as racy. A checker that" >&2
    echo "        fails on everything is not evidence about anything." >&2
    echo "$out" >&2
    return 1
  fi
  echo "  ok: no report on synchronised code"

  echo "==> Self-test: the race set names only modules go.work declares"
  check_set_is_sane
  echo "  ok: $(race_modules | wc -l | tr -d ' ') entries, all present in go.work"
  return 0
}

# ---------------------------------------------------------------------------
# The per-module entry point.
# ---------------------------------------------------------------------------
do_module() {
  local module="$1"
  check_set_is_sane

  if ! printf '%s\n' "$(workspace_modules)" | grep -qxF "$module"; then
    echo "::error::'$module' is not a module go.work declares; refusing to report on it" >&2
    exit 1
  fi

  if ! printf '%s\n' "$(race_modules)" | grep -qxF "$module"; then
    # 🔴 The wording is deliberate. This step's tick is green either way, so the
    # log line is the only place the difference exists -- do not soften it into
    # something that could be read as "checked, nothing found".
    echo "race: NOT COVERED $module -- not in the race set (hack/go-race.sh)"
    return 0
  fi

  echo "race: COVERED $module -- go test -race -count=1 ./..."
  cd "$ROOT/$module"
  # -count=1 for the same reason every other test gate in this repo passes it:
  # `go test` does not track files outside the module, so a cached pass can
  # survive a change that must fail.
  go test -race -count=1 ./...
}

case "${1:-}" in
  --self-test) do_self_test ;;
  --list)      do_list ;;
  "")          echo "usage: $0 <module> | --list | --self-test" >&2; exit 2 ;;
  -*)          echo "usage: $0 <module> | --list | --self-test" >&2; exit 2 ;;
  *)           do_module "$1" ;;
esac
