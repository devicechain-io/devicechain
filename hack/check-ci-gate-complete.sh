#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Asserts that ci.yml's `ci-complete` gate lists EVERY other job in its `needs`.
#
# 🔴 Why this exists. `ci-complete` is the single check branch protection
# requires, and it is only as complete as a hand-written list — GitHub Actions
# has no "needs: every job". So a new job added to ci.yml is unguarded by
# default: it runs, it can fail, and the gate goes green anyway. Nothing about
# that failure looks different from a passing run, because the gate reports on
# the jobs it was told about rather than on the workflow.
#
# This repo has already been bitten by exactly that shape once, on a different
# aggregate job whose `needs` omitted the frontend.
#
# It also checks the two properties that make the gate work at all, because both
# are easy to lose in an edit and neither shows up as a failure:
#   - `if: always()`, without which a gate whose dependency FAILED is SKIPPED,
#     and GitHub counts a skipped required check as satisfied;
#   - a `steps:` body, without which the job succeeds by doing nothing.
#
#   hack/check-ci-gate-complete.sh
#   hack/check-ci-gate-complete.sh --self-test   # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKFLOW="$ROOT/.github/workflows/ci.yml"
GATE="ci-complete"

usage() {
  echo "usage: $0 [--self-test]" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# check <workflow-file>
# ---------------------------------------------------------------------------
# 🔴 A missing YAML parser is a HARD FAILURE, never a skip. A guard that cannot
# read the file must not report on it — "clean" and "never looked" have to be
# distinguishable, which is the whole reason this file exists.
check() {
  python3 - "$1" "$GATE" <<'PY'
import sys

try:
    import yaml
except ImportError:
    sys.exit("ERROR: PyYAML is not available, so this guard could not parse the "
             "workflow. That is a failure, not a pass — nothing was checked.")

path, gate_name = sys.argv[1], sys.argv[2]
with open(path) as fh:
    doc = yaml.safe_load(fh)

jobs = doc.get("jobs") or {}
if not jobs:
    sys.exit(f"ERROR: no jobs found in {path}; the guard examined nothing.")

gate = jobs.get(gate_name)
if gate is None:
    sys.exit(f"ERROR: {path} has no `{gate_name}` job. It is the single check branch "
             f"protection requires; removing it silently unguards every merge.")

problems = []

needs = gate.get("needs") or []
if isinstance(needs, str):
    needs = [needs]
missing = sorted(set(jobs) - set(needs) - {gate_name})
if missing:
    problems.append(
        f"`{gate_name}` does not depend on: {', '.join(missing)}\n"
        f"    Those jobs can fail while the gate reports success. Add them to its `needs`."
    )

# `if: always()` — the difference between a gate and a decoration.
condition = str(gate.get("if", "")).strip()
if "always()" not in condition:
    problems.append(
        f"`{gate_name}` has `if: {condition or '(none)'}`; it must contain always().\n"
        f"    A job whose dependency failed is SKIPPED, and GitHub counts a skipped\n"
        f"    required check as satisfied — so without this the gate passes on exactly\n"
        f"    the runs it exists to block."
    )

if not gate.get("steps"):
    problems.append(
        f"`{gate_name}` has no steps, so it succeeds without inspecting anything."
    )

if problems:
    print(f"FAILED: {path}", file=sys.stderr)
    for p in problems:
        print(f"  - {p}", file=sys.stderr)
    sys.exit(1)

print(f"==> `{gate_name}` guards all {len(jobs) - 1} other jobs, runs on always(), and inspects them.")
PY
}

# ---------------------------------------------------------------------------
# Self-test.
# ---------------------------------------------------------------------------
# 🔴 Each defect is planted ALONE. A self-test that plants all three at once is
# satisfied by a checker that detects any one of them, which is how a guard ends
# up enforcing a third of what it claims.
self_test() {
  local tmp status
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064  # expand tmp now: the trap must not depend on scope
  trap "rm -rf '$tmp'" RETURN

  # The good workflow, in the shape of the real one.
  cat > "$tmp/good.yml" <<'EOF'
name: ci
jobs:
  alpha:
    runs-on: ubuntu-latest
    steps:
      - run: 'true'
  beta:
    runs-on: ubuntu-latest
    steps:
      - run: 'true'
  ci-complete:
    if: always()
    runs-on: ubuntu-latest
    needs: [alpha, beta]
    steps:
      - run: 'true'
EOF
  status=0
  check "$tmp/good.yml" >/dev/null 2>&1 || status=$?
  [ "$status" -eq 0 ] || { echo "SELF-TEST FAILED: a correct workflow was rejected." >&2; return 1; }
  echo "    a correct gate passes"

  # 1. A job the gate does not depend on.
  sed 's/needs: \[alpha, beta\]/needs: [alpha]/' "$tmp/good.yml" > "$tmp/bad.yml"
  status=0
  check "$tmp/bad.yml" >/dev/null 2>&1 || status=$?
  [ "$status" -eq 1 ] || { echo "SELF-TEST FAILED: an unguarded job was not reported." >&2; return 1; }
  echo "    an unguarded job is reported"

  # 2. The missing always(), which is the defect that looks most like working code.
  sed '/if: always()/d' "$tmp/good.yml" > "$tmp/bad.yml"
  status=0
  check "$tmp/bad.yml" >/dev/null 2>&1 || status=$?
  [ "$status" -eq 1 ] || { echo "SELF-TEST FAILED: a gate without always() was not reported." >&2; return 1; }
  echo "    a gate without always() is reported"

  # 3. A gate that inspects nothing.
  python3 - "$tmp/good.yml" "$tmp/bad.yml" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
lines = open(src).read().splitlines(keepends=True)
out, skipping = [], False
for line in lines:
    if line.strip() == "steps:" and skipping is False and "ci-complete" in "".join(out[-6:]):
        skipping = True
        continue
    if skipping and (line.startswith("      - ") or line.startswith("        ")):
        continue
    skipping = False
    out.append(line)
open(dst, "w").writelines(out)
PY
  status=0
  check "$tmp/bad.yml" >/dev/null 2>&1 || status=$?
  [ "$status" -eq 1 ] || { echo "SELF-TEST FAILED: a gate with no steps was not reported." >&2; return 1; }
  echo "    a gate with no steps is reported"

  # 4. The gate deleted outright.
  python3 -c "
import sys, yaml
d = yaml.safe_load(open('$tmp/good.yml'))
del d['jobs']['ci-complete']
yaml.safe_dump(d, open('$tmp/bad.yml', 'w'))
"
  status=0
  check "$tmp/bad.yml" >/dev/null 2>&1 || status=$?
  [ "$status" -eq 1 ] || { echo "SELF-TEST FAILED: a deleted gate was not reported." >&2; return 1; }
  echo "    a deleted gate is reported"

  echo "==> Self-test passed."
}

case "$#:${1:-}" in
  0:)            check "$WORKFLOW" ;;
  1:--self-test) self_test ;;
  *)             usage ;;
esac
