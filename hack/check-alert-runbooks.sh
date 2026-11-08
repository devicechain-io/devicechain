#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guard: every alert the chart ships carries a `runbook_url` that lands on its entry in the
# published alert runbook, and that page (in every locale) is the one generated from the rules.
#
# WHY. An alert is a page at 3 a.m., and the person it wakes needs to get from the
# notification to "what do I do" in one click. Before this, the rules carried no link at
# all, and the runbook was whatever page happened to mention the alert's name.
#
# WHAT IT CHECKS (hack/alert-runbook.py check):
#   - every `- alert:` in deploy/helm/devicechain/templates/prometheusrule*.yaml has a
#     `runbook_url` equal to <runbook page>#<lowercased alert name>;
#   - no alert name is defined twice (the anchor would be ambiguous);
#   - docs/docs/deployment/alert-runbook.md and its es and zh-CN counterparts are exactly what
#     `hack/alert-runbook.py gen .` writes from the rules, so a new alert or a changed
#     description cannot ship without its entry.
#
# 🔴 A SCANNER FAILS BY REPORTING CLEAN, so it refuses to run on an empty alert list, and
# --self-test plants each defect ALONE in a copy of the tree and requires the check to name it,
# after proving a clean copy passes.
#
#   hack/check-alert-runbooks.sh              # check
#   hack/check-alert-runbooks.sh --self-test  # prove the check can fail

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOL="$ROOT/hack/alert-runbook.py"

command -v python3 >/dev/null || { echo "::error::python3 is required" >&2; exit 2; }

if [ "${1:-}" != "--self-test" ]; then
  exec python3 "$TOOL" check "$ROOT"
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fresh() { # fresh <dir>: a copy of just the files the check reads
  local d="$1"
  mkdir -p "$d/deploy/helm/devicechain" "$d/docs/docs" "$d/docs/i18n"
  cp -R "$ROOT/deploy/helm/devicechain/templates" "$d/deploy/helm/devicechain/"
  cp -R "$ROOT/docs/docs/deployment" "$d/docs/docs/"
  for loc in "$ROOT"/docs/i18n/*/docusaurus-plugin-content-docs/current/deployment; do
    rel="${loc#"$ROOT"/}"
    mkdir -p "$d/$rel"
    cp "$loc/alert-runbook.md" "$d/$rel/" 2>/dev/null || true
  done
}

fails=0
expect_fail() { # expect_fail <label> <dir> <needle>
  local out
  if out="$(python3 "$TOOL" check "$2" 2>&1)"; then
    echo "SELF-TEST FAILED: $1 passed the check"
    fails=$((fails + 1))
  elif ! grep -q -- "$3" <<<"$out"; then
    echo "SELF-TEST FAILED: $1 failed, but not by naming '$3':"
    echo "$out" | sed 's/^/    /'
    fails=$((fails + 1))
  else
    echo "ok: $1 is refused"
  fi
}

fresh "$work/clean"
python3 "$TOOL" check "$work/clean" >/dev/null || { echo "SELF-TEST FAILED: a clean copy does not pass"; exit 1; }
echo "ok: a clean copy passes"

rules="deploy/helm/devicechain/templates"
victim="$(grep -l 'alert: DeviceCredentialAttemptStoreFull' "$ROOT/$rules"/prometheusrule*.yaml | sed -n '1p')"
victim="$rules/$(basename "$victim")"

# 1. a rule loses its runbook_url
fresh "$work/d1"
python3 - "$work/d1/$victim" <<'PY'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8", newline="").read()
i = s.index("alert: DeviceCredentialAttemptStoreFull")
j = s.index("runbook_url:", i)
k = s.index("\n", j)
open(p, "w", encoding="utf-8", newline="").write(s[:s.rindex("\n", 0, j)] + s[k:])
PY
expect_fail "a rule with no runbook_url" "$work/d1" "DeviceCredentialAttemptStoreFull.*has no runbook_url"

# 2. a rule points somewhere else
fresh "$work/d2"
sed -i 's|alert-runbook#devicecredentialattemptstorefull|alert-runbook#wrong|' "$work/d2/$victim"
expect_fail "a runbook_url at the wrong anchor" "$work/d2" "has runbook_url"

# 3. the page is stale against a rule
fresh "$work/d3"
sed -i '0,/^### /s//### Planted {#planted}\n\n### /' "$work/d3/docs/docs/deployment/alert-runbook.md"
expect_fail "a stale runbook page" "$work/d3" "is stale against the rules"

# 4. a locale's page is missing
fresh "$work/d4"
rm -f "$work/d4/docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/alert-runbook.md"
expect_fail "a missing translated page" "$work/d4" "is missing"

# 5. a new alert nobody wrote an entry for
fresh "$work/d5"
python3 - "$work/d5/$victim" <<'PY'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8", newline="").read()
s = s.replace("        - alert: DeviceCredentialAttemptStoreFull",
              "        - alert: PlantedAlert\n          expr: vector(1) > 0\n          annotations:\n"
              "            summary: planted\n            runbook_url: \"https://docs.devicechain.io/deployment/alert-runbook#plantedalert\"\n"
              "        - alert: DeviceCredentialAttemptStoreFull", 1)
open(p, "w", encoding="utf-8", newline="").write(s)
PY
expect_fail "a new alert with no page entry" "$work/d5" "is stale against the rules"

# 6. an empty rules directory must not read as clean
fresh "$work/d6"
rm -f "$work/d6/$rules"/prometheusrule*.yaml
expect_fail "no rules at all" "$work/d6" "found no alerts"

[ "$fails" -eq 0 ] || exit 1
echo "self-test passed: the check names each planted defect"
