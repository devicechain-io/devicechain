#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The loadtest gate's L3 contention stage runs twice: a floor-0 negative control,
# then a floor-1 positive test. Each run starts with a clean-tenant precheck that
# refuses a tenant carrying events from the last 30 seconds. When both runs drove
# the SAME tenants, the positive run's precheck counted the negative control's own
# tail, and the gate passed only while persistence was slow enough to leave 30
# seconds between them. It stopped passing once persistence got faster.
#
# This check fails if the two steps name any handshake file in common, and it fails
# if either step names fewer than two (a step that was renamed or reshaped would
# otherwise pass vacuously).
#
#   hack/check-loadtest-l3-tenants.sh [workflow-file]
#   hack/check-loadtest-l3-tenants.sh --self-test
set -euo pipefail

NEG_STEP='L3 negative control'
POS_STEP='L3 positive test'

# handshakes <file> <step-name-prefix>: every *-handshake path in that step's block.
handshakes() {
  awk -v want="$2" '
    /^[[:space:]]*- name:/ { inblk = (index($0, want) > 0) }
    inblk && match($0, /--[a-z]+-handshake[[:space:]]+"?[^" \\]+/) {
      s = substr($0, RSTART, RLENGTH); sub(/^--[a-z]+-handshake[[:space:]]+"?/, "", s); print s
    }' "$1" | sort -u
}

check() {
  local wf="$1" neg pos shared
  neg="$(handshakes "$wf" "$NEG_STEP")"
  pos="$(handshakes "$wf" "$POS_STEP")"
  if [ "$(printf '%s\n' "$neg" | grep -c .)" -lt 2 ] || [ "$(printf '%s\n' "$pos" | grep -c .)" -lt 2 ]; then
    echo "check-loadtest-l3-tenants: expected two handshakes in each of '$NEG_STEP' and '$POS_STEP' in $wf" >&2
    echo "  negative: ${neg:-<none>}" >&2
    echo "  positive: ${pos:-<none>}" >&2
    return 1
  fi
  shared="$(comm -12 <(printf '%s\n' "$neg") <(printf '%s\n' "$pos"))"
  if [ -n "$shared" ]; then
    echo "check-loadtest-l3-tenants: the L3 negative control and positive test share tenants in $wf:" >&2
    printf '%s\n' "$shared" | sed 's/^/  /' >&2
    echo "  The positive run's clean-tenant precheck would count the negative run's tail. Give it its own pair." >&2
    return 1
  fi
  echo "check-loadtest-l3-tenants: OK ($wf)"
}

self_test() {
  local d rc=0
  d="$(mktemp -d)"; trap 'rm -rf "$d"' RETURN
  mk() { # mk <file> <neg-gold> <neg-shed> <pos-gold> <pos-shed>
    cat >"$1" <<YAML
      - name: $NEG_STEP -- floor 0
        run: |
          x \\
            --gold-handshake "\$HOME/sims/$2.json" \\
            --shed-handshake "\$HOME/sims/$3.json" \\
            --expect-floor 0
      - name: something between
        run: echo hi --gold-handshake "\$HOME/sims/$4.json"
      - name: $POS_STEP -- floor 1
        run: |
          x \\
            --gold-handshake "\$HOME/sims/$4.json" \\
            --shed-handshake "\$HOME/sims/$5.json" \\
            --expect-floor 1
YAML
  }
  mk "$d/ok.yml" g s pg ps
  mk "$d/shared.yml" g s g ps
  mk "$d/allshared.yml" g s g s
  printf '      - name: renamed step\n        run: x --gold-handshake "a.json" --shed-handshake "b.json"\n' >"$d/vacuous.yml"
  check "$d/ok.yml" >/dev/null || { echo "self-test: disjoint pairs were refused" >&2; rc=1; }
  if check "$d/shared.yml" 2>/dev/null; then echo "self-test: a shared gold tenant passed" >&2; rc=1; fi
  if check "$d/allshared.yml" 2>/dev/null; then echo "self-test: identical pairs passed" >&2; rc=1; fi
  if check "$d/vacuous.yml" 2>/dev/null; then echo "self-test: a workflow without the two steps passed" >&2; rc=1; fi
  [ "$rc" = 0 ] && echo "check-loadtest-l3-tenants: self-test OK"
  return "$rc"
}

if [ "${1:-}" = "--self-test" ]; then self_test; exit $?; fi
check "${1:-$(git rev-parse --show-toplevel)/.github/workflows/loadtest-gate.yml}"
