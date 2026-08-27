#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Owns, and guards, the rule that decides whether a pull request runs the upgrade drill.
#
# `.github/workflows/upgrade-gate.yml` runs the drill on a pull request only when that
# PR touches something which can change what an existing instance holds or how it is
# read. The dominant entry in that list is migrations: they are the direct cause of the
# class the drill exists to find.
#
# 🔴 THE LIST LIVES HERE, NOT IN THE WORKFLOW'S `on: paths:`, AND THAT IS THE POINT.
# The drill has to be a REQUIRED check to be worth anything — a red gate nobody is
# obliged to look at is discipline wearing a gate's clothes — and a required check that
# never reports leaves every unrelated pull request pending forever. So the workflow
# fires on every PR and this script decides, inside the run, whether the drill is
# warranted. One list, read by both the completeness check and the decision, because
# the alternative is two lists and a promise that they agree.
#
# 🔴 A LIST THAT STOPS MATCHING IS SILENT, AND SILENT IN THE WORST DIRECTION. Nothing
# fails. The PR goes green, the drill simply does not appear, and "the upgrade drill did
# not object" reads exactly like "the upgrade drill passed" to everyone including the
# person who merges it. That is the same shape of defect the drill was built to catch —
# an unchecked claim that reads as a checked one — so the list does not get to be an
# unchecked claim itself.
#
# Three claims are checked, and they are deliberately of different kinds:
#
#   1. COMPLETENESS. Every migration and baseline file in the tree is matched by at
#      least one pattern. This is the one set that can be enumerated without judgement;
#      the rest of the list (served schemas, models, the chart) is a coverage judgement
#      and is not second-guessed here.
#   2. SHAPE. The workflow still has a `pull_request` trigger, and that trigger declares
#      NO `paths:`/`paths-ignore:`. A filter reintroduced up there would AND with the
#      decision below, invisibly, and the decision would never see the files it filtered
#      out — the two-lists failure this file exists to avoid, wearing a disguise.
#   3. THE DECISION ITSELF (`--match-changed`), which is what the workflow calls.
#
# The most likely way claim 1 fires: a NEW functional area lands, with its schema under
# a directory shape the patterns do not reach. That area's migrations would then never
# trigger the drill, and nobody would learn it from a green PR.
#
# Usage:
#   hack/check-upgrade-gate-paths.sh                  # claims 1 and 2
#   hack/check-upgrade-gate-paths.sh --match-changed  # paths on stdin -> run=true|false
#   hack/check-upgrade-gate-paths.sh --self-test      # prove all three can fail

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

WORKFLOW=".github/workflows/upgrade-gate.yml"

# ---------------------------------------------------------------------------
# GATE_PATHS — the single source of truth for "can this change what an instance holds?"
# ---------------------------------------------------------------------------
# Priced rather than guessed: over the sixty merged commits before 2026-08-27 these
# patterns fire on twenty-five of them, and the drill costs about eight minutes on a
# runner. What the list has to cover is anything that can change what an existing
# instance holds or how it is read:
#
#   - migrations and baselines, the direct cause;
#   - a served schema, because the drill's read-backs select named fields;
#   - `model_*.go`, because a stored shape can move with NO migration at all — that is
#     3 of the 25, and it is the class the geofence-archive defect belonged to;
#   - apiprobe and the rig, so a change to the instrument is measured by the instrument;
#   - the chart, because the documented upgrade IS a `helm upgrade`, and a chart change
#     is what broke that procedure in v0.11.0;
#   - THIS FILE, because it now decides whether the drill runs at all. A change to the
#     decider that quietly narrowed the decision would otherwise be the one change the
#     drill never sees.
#
# Syntax is GitHub's filter language, kept rather than switched to globs, so that the
# list stays copy-pasteable back into an `on: paths:` block if this design is ever
# reversed — and so `**` keeps meaning "crosses directories" while `*` does not.
GATE_PATHS=(
  'backend/**/migration*.go'
  'backend/**/baseline*.go'
  'backend/services/*/graphql/**'
  'backend/services/*/model/model_*.go'
  'backend/tools/apiprobe/**'
  'hack/upgrade-rig.sh'
  'hack/check-upgrade-gate-paths.sh'
  'deploy/helm/**'
  '.github/workflows/upgrade-gate.yml'
)

usage() {
  echo "usage: $0 [--match-changed | --self-test]" >&2
  exit 2
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
# CLAIM 1 — completeness: every migration/baseline file must be matched.
# ---------------------------------------------------------------------------
# `_legacy/` is excluded because it is the archived pre-migration tree: not in the
# workspace, not built, and explicitly not maintained. Including it would make this
# check fail forever on files nobody is allowed to touch.
check_completeness() {
  local pats=("$@") f rc=0 checked=0

  if [ "${#pats[@]}" -eq 0 ]; then
    echo "::error::the pattern list is empty, so nothing would ever trigger the upgrade drill"
    return 1
  fi

  while read -r f; do
    checked=$((checked + 1))
    if ! matches "$f" "${pats[@]}"; then
      echo "::error file=${f}::not matched by any pattern in GATE_PATHS, so a PR touching it would NOT run the upgrade drill"
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

  [ "$rc" -eq 0 ] && echo "GATE_PATHS covers all ${checked} migration/baseline files (${#pats[@]} patterns)"
  return "$rc"
}

# ---------------------------------------------------------------------------
# CLAIM 2 — shape: the workflow fires on every PR, and filters none of them away.
# ---------------------------------------------------------------------------
# Read with python's YAML parser rather than by grepping: the trigger sits in a block
# that also carries `branches:`, and a text scrape cannot tell those two lists apart.
check_workflow_shape() {
  local workflow="$1" verdict
  verdict="$(python3 - "$workflow" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
# `on:` parses as the boolean True in YAML 1.1, which is a trap worth naming: the key
# is not the string "on" and a doc["on"] lookup returns nothing at all.
triggers = doc.get(True, doc.get("on", {})) or {}
if "pull_request" not in triggers:
    print("no-trigger"); raise SystemExit
pr = triggers.get("pull_request") or {}
bad = [k for k in ("paths", "paths-ignore") if k in pr]
print("filtered:" + ",".join(bad) if bad else "ok")
PY
)"

  case "$verdict" in
    ok)
      echo "${workflow} fires on every pull request and filters none of them away"
      return 0 ;;
    no-trigger)
      echo "::error::${workflow} has no pull_request trigger, so the upgrade drill never runs on a pull request at all"
      return 1 ;;
    filtered:*)
      echo "::error::${workflow}'s pull_request trigger declares ${verdict#filtered:}."
      echo "    That filter ANDs with the decision in this script, invisibly: a file it"
      echo "    excludes never reaches --match-changed, so the drill can be skipped for a"
      echo "    change GATE_PATHS says needs it. The workflow must fire on every PR and"
      echo "    let this script decide. Delete the filter, or move its patterns into"
      echo "    GATE_PATHS if they belong there."
      return 1 ;;
    *)
      echo "::error::could not read ${workflow}'s triggers (got '${verdict}')"
      return 1 ;;
  esac
}

# ---------------------------------------------------------------------------
# CLAIM 3 — the decision. Paths on stdin, `run=true|false` on stdout.
# ---------------------------------------------------------------------------
# Output goes to stdout in $GITHUB_OUTPUT's own key=value form so the caller can append
# it directly; everything a human reads goes to stderr, so the two cannot be confused.
#
# 🔴 EVERY UNCERTAINTY RESOLVES TO `run=true`. Running the drill when it was not needed
# costs eight minutes. NOT running it when it was needed produces a green pull request
# that was never drilled — which is the exact outcome this whole file exists to prevent,
# and it is indistinguishable from a drill that passed.
match_changed() {
  local f matched=() total=0

  while read -r f; do
    [ -n "$f" ] || continue
    total=$((total + 1))
    if matches "$f" "${GATE_PATHS[@]}"; then
      matched+=("$f")
    fi
  done

  # An empty file list is not "nothing to drill", it is "the file list did not arrive".
  # A pull request always changes at least one file, so zero means the caller's `gh api`
  # returned nothing, silently, and a `run=false` here would be a vacuous green.
  if [ "$total" -eq 0 ]; then
    echo "::warning::no changed files were supplied, which cannot be true of a pull request; running the drill rather than assuming there is nothing to drill" >&2
    echo "run=true"
    return 0
  fi

  if [ "${#matched[@]}" -eq 0 ]; then
    echo "None of the ${total} changed file(s) can change what an existing instance holds or how it is read. Skipping the drill." >&2
    echo "run=false"
    return 0
  fi

  {
    echo "${#matched[@]} of ${total} changed file(s) can change what an existing instance holds or how it is read:"
    printf '    %s\n' "${matched[@]}"
  } >&2
  echo "run=true"
}

check() {
  local rc=0
  check_completeness "${GATE_PATHS[@]}" || rc=1
  check_workflow_shape "$WORKFLOW" || rc=1
  return "$rc"
}

# ---------------------------------------------------------------------------
# self-test: each claim must be shown to FAIL, and each must be shown to PASS.
# ---------------------------------------------------------------------------
# The house rule, and it earns its keep here more than most: this script's whole job is
# to notice an absence, and a check for an absence that has never been seen to fire is
# indistinguishable from one that always passes. Each defect is planted ALONE — a
# self-test that plants all of them at once is satisfied by a checker that finds any one.
self_test() {
  local tmp rc out
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  # --- claim 1: completeness -------------------------------------------------
  local without_migrations=()
  for p in "${GATE_PATHS[@]}"; do
    [ "$p" = 'backend/**/migration*.go' ] || without_migrations+=("$p")
  done
  rc=0; check_completeness "${without_migrations[@]}" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "SELF-TEST FAILED: a list with no migration pattern was accepted" >&2; return 1; }
  echo "    a list that lost its migration pattern is REFUSED"

  rc=0; check_completeness >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "SELF-TEST FAILED: an empty pattern list was accepted" >&2; return 1; }
  echo "    an empty pattern list is REFUSED"

  # THE COUNTERWEIGHT. Refusing everything would pass both cases above while being
  # completely broken, so the real list must still be accepted.
  rc=0; check_completeness "${GATE_PATHS[@]}" >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 0 ] || { echo "SELF-TEST FAILED: the real pattern list was refused (exit $rc)" >&2; return 1; }
  echo "    the real pattern list is ACCEPTED"

  # --- claim 2: workflow shape ----------------------------------------------
  python3 - "$WORKFLOW" "$tmp/notrigger.yml" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
key = True if True in doc else "on"
doc[key].pop("pull_request", None)
yaml.safe_dump(doc, open(sys.argv[2], "w"))
PY
  rc=0; check_workflow_shape "$tmp/notrigger.yml" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "SELF-TEST FAILED: a workflow with no pull_request trigger was accepted" >&2; return 1; }
  echo "    a workflow with no pull_request trigger is REFUSED"

  # The regression this design introduces the possibility of, so it is planted here
  # before anyone can introduce it for real.
  python3 - "$WORKFLOW" "$tmp/refiltered.yml" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
key = True if True in doc else "on"
doc[key]["pull_request"]["paths"] = ["backend/**/migration*.go"]
yaml.safe_dump(doc, open(sys.argv[2], "w"))
PY
  rc=0; check_workflow_shape "$tmp/refiltered.yml" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "SELF-TEST FAILED: a reintroduced paths: filter was accepted" >&2; return 1; }
  echo "    a reintroduced paths: filter is REFUSED"

  rc=0; check_workflow_shape "$WORKFLOW" >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 0 ] || { echo "SELF-TEST FAILED: the real workflow was refused (exit $rc)" >&2; return 1; }
  echo "    the real workflow is ACCEPTED"

  # --- claim 3: the decision -------------------------------------------------
  out="$(printf 'backend/services/device-management/schema/baseline.go\ndocs/README.md\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=true" ] || { echo "SELF-TEST FAILED: a baseline change did not run the drill (got '$out')" >&2; return 1; }
  echo "    a changed baseline RUNS the drill"

  # THE COUNTERWEIGHT for claim 3, and the only case that proves the decision is a
  # decision. A --match-changed that answered run=true unconditionally would satisfy
  # every other case here while reinstating a drill on all sixty commits in sixty.
  out="$(printf 'docs/README.md\nfrontend/apps/console/src/main.tsx\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=false" ] || { echo "SELF-TEST FAILED: an unrelated change still ran the drill (got '$out')" >&2; return 1; }
  echo "    a change that cannot touch stored data SKIPS the drill"

  out="$(printf '' | match_changed 2>/dev/null)"
  [ "$out" = "run=true" ] || { echo "SELF-TEST FAILED: an empty file list did not fail closed (got '$out')" >&2; return 1; }
  echo "    an empty file list FAILS CLOSED and runs the drill"

  # `*` must not cross a directory separator, or `backend/services/*/model/model_*.go`
  # would quietly widen to every model file at any depth. This is the property that
  # makes the regex translation necessary; a plain fnmatch would get it wrong.
  out="$(printf 'backend/services/a/b/model/model_x.go\n' | match_changed 2>/dev/null)"
  [ "$out" = "run=false" ] || { echo "SELF-TEST FAILED: '*' crossed a directory separator (got '$out')" >&2; return 1; }
  echo "    a single '*' does not cross a directory separator"

  echo "SELF-TEST PASSED"
}

case "${1:-}" in
  "")               check ;;
  --match-changed)  match_changed ;;
  --self-test)      self_test ;;
  *)                usage ;;
esac
