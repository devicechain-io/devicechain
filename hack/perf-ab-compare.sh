#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Compare perf-ab arms, and derive the noise band from identical replicates.
#
#   # 1. run the SAME arm 3 or more times (hack/perf-ab.sh --arm main-1 / main-2 / main-3 ...), then
#   #    set the per-metric noise band from the replicates:
#   hack/perf-ab-compare.sh noise noise.json out/main-1 out/main-2 out/main-3
#
#   # 2. compare an arm (or a replicate set, joined with commas) to the baseline:
#   hack/perf-ab-compare.sh compare --noise noise.json out/main-1,out/main-2,out/main-3 out/maxbatch-64
#
#   hack/perf-ab-compare.sh --self-test
#
# A metric is flagged CHANGED when its delta against the baseline mean is larger than its band.
# The band is the 99% prediction interval of ONE new run from the replicates (Student t, so it is
# wide with three replicates and narrows with more), never below 2% (5% for CPU); a test arm that
# is itself several runs gets a tighter band. `noise --confidence 95` is tighter and leakier.
# Runs whose harness verdict failed are left out (loudly) unless `compare --include-failed`.
# Without --noise, a baseline given as several replicates supplies its own band; a single baseline
# falls back to the floor. Exit 0: nothing outside the band. Exit 1: something is. Metrics that are
# diagnostics, not costs (rates, loss counters, full-page-image counts, JetStream API calls) are shown
# with (info) and never flagged.
# Always read the warnings under each table: a CFS-throttled arm, a restarted pod, a checkpoint or
# a standby backup in the window, or a different leader layout makes the delta not like-for-like.
# Compare arms that were rolled the same way (hack/perf-ab.sh --roll): rolling the pods alone moves
# the route message count by 15-20 percent.
#
# Arguments are arm directories (containing summary.json) or summary.json files.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HELPER="$ROOT/hack/perf-ab/perf_ab.py"

py() {
  if [[ -n "${PYTHON:-}" ]]; then "$PYTHON" -I "$@"
  elif python3 -c 'import sys' >/dev/null 2>&1; then python3 -I "$@"
  else python -I "$@"; fi
}

case "${1:-}" in
  --self-test) exec_rc=0; py "$HELPER" self-test || exec_rc=$?; exit "$exec_rc" ;;
  noise|compare)
    cmd="$1"; shift
    py "$HELPER" "$cmd" "$@" ;;
  *) sed -n '4,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
