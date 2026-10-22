#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Does any served GraphQL schema break a client written against the last release?
#
#   hack/check-graphql-compat.sh            # baseline: nearest stable tag reachable from HEAD
#   hack/check-graphql-compat.sh v0.24.0    # baseline: the tag given
#   hack/check-graphql-compat.sh --self-test # prove the check can fail, through this script
#
# Extracts the baseline release's backend/services with `git archive` and hands both
# trees to backend/tools/docsgql/cmd/sdlcompat, which classifies every difference and
# fails on a breaking one not declared in backend/tools/docsgql/sdlcompat/breaks.txt.
#
# THE BASELINE is the nearest STABLE tag REACHABLE from HEAD, not the newest tag in the
# repository: a fix on a release branch must be compared with that branch's release, not
# with a later minor that added things, and a release candidate is not a release anyone
# runs. Same exclusion the upgrade gate makes.
#
# 🔴 NO TAG IS A FAILURE, NEVER A SKIP. On a shallow clone `git describe` finds nothing,
# and a compatibility check that compared against nothing would pass every break. The CI
# job checks out with fetch-depth: 0 for exactly this.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# The command is BUILT, not `go run`: `go run` exits 1 for any non-zero exit of the
# program, which would fold "could not run" (2) into "found a break" (1) — and the
# messages below depend on telling those apart.
BIN_DIR="$(mktemp -d)"
trap 'rm -rf "$BIN_DIR"' EXIT
SDLCOMPAT="$BIN_DIR/sdlcompat"
(cd "$ROOT/backend/tools/docsgql" && go build -o "$SDLCOMPAT" ./cmd/sdlcompat)

# compat <baseline-root> <baseline-label> [extra sdlcompat flags...]
compat() {
  local base="$1" label="$2"
  shift 2
  "$SDLCOMPAT" -base "$base" -baseline "$label" "$@" "$ROOT"
}

# ---------------------------------------------------------------------------
# --self-test: the check, end to end through this script and the command, on the
# REAL served schemas — not on a fixture. The baseline is a copy of the working
# tree's schemas, so the only difference is the one planted here.
# ---------------------------------------------------------------------------
self_test() {
  local tmp out rc probe_schema probe
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  probe_schema="device-management"
  probe="dcCompatSelfTestProbe"

  mkdir -p "$tmp/base/backend/services"
  local copied=0 d area
  for d in backend/services/*/graphql; do
    area="$(basename "$(dirname "$d")")"
    mkdir -p "$tmp/base/backend/services/$area"
    cp -R "$d" "$tmp/base/backend/services/$area/"
    copied=$((copied + 1))
  done
  [ "$copied" -gt 0 ] || { echo "self-test: copied no schema directories; nothing would be tested" >&2; return 1; }
  : > "$tmp/none.txt"

  # 1. Identical trees: no change at all, so the check must pass and report 0 breaking.
  #    This is the case that proves the check does not cry wolf on today's tree.
  out="$(compat "$tmp/base" v0.0.0 -breaks "$tmp/none.txt" 2>&1)" || {
    echo "$out"; echo "self-test: identical trees FAILED; the check reports a change where there is none" >&2; return 1; }
  grep -q ' 0 breaking (0 declared), 0 dangerous, 0 safe; 0 failed' <<<"$out" || {
    echo "$out"; echo "self-test: identical trees did not report zero changes" >&2; return 1; }

  # 2. The baseline served a field the working tree does not: a removed field. The check
  #    must FAIL and name it by schema and coordinate.
  local f="$tmp/base/backend/services/$probe_schema/graphql/schema.graphql"
  awk -v p="$probe" '{ print } /^type Query \{/ && !done { print "  " p ": Boolean"; done = 1 }' "$f" > "$f.new"
  mv "$f.new" "$f"
  grep -q "$probe" "$f" || { echo "self-test: could not plant the probe field in $f" >&2; return 1; }
  rc=0
  out="$(compat "$tmp/base" v0.0.0 -breaks "$tmp/none.txt" 2>&1)" || rc=$?
  [ "$rc" -eq 1 ] || { echo "$out"; echo "self-test: a removed field exited $rc, want 1" >&2; return 1; }
  grep -q "$probe_schema: Query.$probe field-removed" <<<"$out" || {
    echo "$out"; echo "self-test: the failure does not name $probe_schema: Query.$probe field-removed" >&2; return 1; }

  # 3. The same removal, declared against this baseline: passes, and reports it declared.
  echo "v0.0.0 $probe_schema Query.$probe field-removed self-test probe" > "$tmp/declared.txt"
  out="$(compat "$tmp/base" v0.0.0 -breaks "$tmp/declared.txt" 2>&1)" || {
    echo "$out"; echo "self-test: a declared removal FAILED" >&2; return 1; }
  grep -q '1 breaking (1 declared)' <<<"$out" || { echo "$out"; echo "self-test: the declared removal was not counted" >&2; return 1; }

  # 4. The declaration against a DIFFERENT baseline is expired: it covers nothing, so the
  #    removal fails again.
  rc=0
  out="$(compat "$tmp/base" v0.0.1 -breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  if [ "$rc" -ne 1 ] || ! grep -q '^expired' <<<"$out"; then
    echo "$out"; echo "self-test: a declaration for another baseline still covered the change (rc=$rc)" >&2; return 1
  fi

  # 5. A declaration with no change behind it is stale and fails.
  rm -rf "$tmp/base/backend/services"
  mkdir -p "$tmp/base/backend/services"
  for d in backend/services/*/graphql; do
    area="$(basename "$(dirname "$d")")"
    mkdir -p "$tmp/base/backend/services/$area"
    cp -R "$d" "$tmp/base/backend/services/$area/"
  done
  rc=0
  out="$(compat "$tmp/base" v0.0.0 -breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  if [ "$rc" -ne 1 ] || ! grep -q 'but that change is not in the schema' <<<"$out"; then
    echo "$out"; echo "self-test: a stale declaration did not fail (rc=$rc)" >&2; return 1
  fi

  echo "graphql-compat self-test: identical trees pass; a removed field fails naming it; declared passes; expired and stale fail ($copied schema directories)"
}

case "${1:-}" in
  --self-test)
    [ $# -eq 1 ] || { echo "usage: $0 --self-test" >&2; exit 2; }
    self_test
    exit
    ;;
esac

if [ $# -gt 1 ]; then
  echo "usage: $0 [baseline-tag | --self-test]" >&2
  exit 2
fi

if [ $# -eq 1 ]; then
  baseline="$1"
else
  # '[0-9]*' and the exclusion of any '-' keep release candidates and non-release tags out.
  baseline="$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' --exclude '*-*' HEAD 2>/dev/null || true)"
  if [ -z "$baseline" ]; then
    echo "::error::no stable vX.Y.Z release tag is reachable from HEAD. A shallow clone is the usual cause (check out with fetch-depth: 0); a check with no baseline compares against nothing and must not pass." >&2
    exit 1
  fi
fi
git rev-parse --verify --quiet "${baseline}^{commit}" >/dev/null || {
  echo "::error::baseline '$baseline' is not a tag or commit in this repository." >&2
  exit 1
}
echo "GraphQL compatibility baseline: $baseline"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp" "$BIN_DIR"' EXIT
git archive --format=tar "$baseline" backend/services | tar -x -C "$tmp"
[ -d "$tmp/backend/services" ] || {
  echo "::error::$baseline has no backend/services to compare against." >&2
  exit 1
}

rc=0
compat "$tmp" "$baseline" || rc=$?
case "$rc" in
  0) ;;
  1)
    echo "::error::a served GraphQL schema breaks clients written against $baseline, or a declaration or deprecation is invalid. The FAIL lines above name each element and the line that would declare it." >&2
    exit 1
    ;;
  *)
    echo "::error::the GraphQL compatibility check could not run (exit $rc); nothing was compared. See the error above." >&2
    exit 1
    ;;
esac
