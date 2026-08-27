#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guards the PATH FILTER on the upgrade gate.
#
# `.github/workflows/upgrade-gate.yml` runs the upgrade drill on a pull request only
# when that PR touches something which can change what an existing instance holds or
# how it is read. The dominant entry in that list is migrations: they are the direct
# cause of the class the drill exists to find.
#
# 🔴 A PATH FILTER THAT STOPS MATCHING IS SILENT, AND SILENT IN THE WORST DIRECTION.
# Nothing fails. The PR goes green, the gate simply does not appear, and "the upgrade
# drill did not object" reads exactly like "the upgrade drill passed" to everyone
# including the person who merges it. That is the same shape of defect the drill was
# built to catch — an unchecked claim that reads as a checked one — so the filter does
# not get to be an unchecked claim itself.
#
# The claim being checked here is narrow and exact: EVERY migration and baseline file
# in the tree is matched by at least one pattern in the workflow's `paths:` list. It
# deliberately does not try to verify the rest of the list (served schemas, models, the
# chart), because those are judgement calls about coverage and this is a check about
# completeness of the one set that can be enumerated without judgement.
#
# The most likely way it fires: a NEW functional area lands, with its schema under a
# directory shape the patterns do not reach. That area's migrations would then never
# trigger the gate, and nobody would learn it from a green PR.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

WORKFLOW=".github/workflows/upgrade-gate.yml"

usage() {
  echo "usage: $0 [--self-test]" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# gate_paths: the `paths:` entries, read from a workflow given as $1.
# ---------------------------------------------------------------------------
# Read with python's YAML parser rather than by grepping for `- '`: the list sits
# inside a `pull_request:` block that also carries `branches:`, and a text scrape
# cannot tell those two lists apart. Getting `main` into the pattern set would make
# every check below pass for the wrong reason.
gate_paths() {
  python3 - "$1" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
# `on:` parses as the boolean True in YAML 1.1, which is a trap worth naming: the key
# is not the string "on" and a doc["on"] lookup returns nothing at all.
triggers = doc.get(True, doc.get("on", {})) or {}
for p in (triggers.get("pull_request") or {}).get("paths") or []:
    print(p)
PY
}

# ---------------------------------------------------------------------------
# matches: does any pattern in $2.. match the path in $1?
# ---------------------------------------------------------------------------
# GitHub's filter language is not fnmatch: `**` crosses directory separators and `*`
# does not. Translating to a regex is the only way to say that, and it is why this is
# python rather than a `case` statement.
matches() {
  python3 - "$@" <<'PY'
import re, sys
path, pats = sys.argv[1], sys.argv[2:]
def to_re(p):
    out, i = "", 0
    while i < len(p):
        if p[i:i+3] == "**/": out += "(?:.*/)?"; i += 3
        elif p[i:i+2] == "**": out += ".*"; i += 2
        elif p[i] == "*":    out += "[^/]*"; i += 1
        else:                out += re.escape(p[i]); i += 1
    return re.compile("^" + out + "$")
sys.exit(0 if any(to_re(p).match(path) for p in pats) else 1)
PY
}

# ---------------------------------------------------------------------------
# check: every migration/baseline file must be matched.
# ---------------------------------------------------------------------------
# `_legacy/` is excluded because it is the archived pre-migration tree: not in the
# workspace, not built, and explicitly not maintained. Including it would make this
# check fail forever on files nobody is allowed to touch.
check() {
  local workflow="$1" pats=() f rc=0 checked=0
  mapfile -t pats < <(gate_paths "$workflow")

  if [ "${#pats[@]}" -eq 0 ]; then
    echo "::error::${workflow} declares no pull_request paths; either the trigger was removed or its shape changed, and either way this gate no longer runs on a migration PR"
    return 1
  fi

  while read -r f; do
    checked=$((checked + 1))
    if ! matches "$f" "${pats[@]}"; then
      echo "::error file=${f}::not matched by any path filter in ${workflow}, so a PR touching it would NOT run the upgrade drill"
      rc=1
    fi
  done < <(git ls-files | grep -E '(^|/)(migration[^/]*|baseline[^/]*)\.go$' | grep -v '^_legacy/')

  # 🔴 ZERO FILES CHECKED IS A FAILURE, NOT A PASS. If the enumeration above ever
  # returns nothing — a rename, a moved tree, a grep that stopped matching — every
  # file trivially passes and this script reports success having examined nothing.
  # That is precisely the vacuous green it exists to prevent elsewhere.
  if [ "$checked" -eq 0 ]; then
    echo "::error::found no migration or baseline files to check; this script examined nothing and its pass would mean nothing"
    return 1
  fi

  [ "$rc" -eq 0 ] && echo "upgrade-gate path filter covers all ${checked} migration/baseline files (${#pats[@]} patterns)"
  return "$rc"
}

# ---------------------------------------------------------------------------
# self-test: the check must be shown to FAIL.
# ---------------------------------------------------------------------------
# The house rule, and it earns its keep here more than most: this check's whole job is
# to notice an absence, and a check for an absence that has never been seen to fire is
# indistinguishable from one that always passes.
self_test() {
  local tmp rc
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  # 1. A workflow whose filter has lost the migration pattern must FAIL.
  sed "s|      - 'backend/\*\*/migration\*.go'||" "$WORKFLOW" > "$tmp/lost.yml"
  rc=0; check "$tmp/lost.yml" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "SELF-TEST FAILED: a filter with no migration pattern was accepted" >&2; return 1; }
  echo "    a filter that lost its migration pattern is REFUSED"

  # 2. A workflow with no pull_request trigger at all must FAIL, and for its own
  #    reason — an empty pattern list makes every path unmatched, which would
  #    otherwise be reported as a hundred file errors instead of one cause.
  python3 - "$WORKFLOW" "$tmp/notrigger.yml" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
key = True if True in doc else "on"
doc[key].pop("pull_request", None)
yaml.safe_dump(doc, open(sys.argv[2], "w"))
PY
  rc=0; check "$tmp/notrigger.yml" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "SELF-TEST FAILED: a workflow with no pull_request trigger was accepted" >&2; return 1; }
  echo "    a workflow with no pull_request trigger is REFUSED"

  # 3. THE COUNTERWEIGHT. Refusing everything would pass both cases above while being
  #    completely broken, so the real workflow must still be accepted.
  rc=0; check "$WORKFLOW" >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 0 ] || { echo "SELF-TEST FAILED: the real workflow was refused (exit $rc)" >&2; return 1; }
  echo "    the real workflow is ACCEPTED"

  echo "SELF-TEST PASSED"
}

case "${1:-}" in
  "")           check "$WORKFLOW" ;;
  --self-test)  self_test ;;
  *)            usage ;;
esac
