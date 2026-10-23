#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# May the removals declared against the last release actually be removed? Only when each element
# was marked deprecated early enough for a client to have moved.
#
#   hack/check-deprecation-window.sh                 # baseline: nearest stable tag reachable from HEAD
#   hack/check-deprecation-window.sh v1.4.0          # baseline: the tag given
#   hack/check-deprecation-window.sh --enforce       # fail even before 1.0.0
#   hack/check-deprecation-window.sh --self-test     # prove the check can fail, through this script
#
# THE RULE (the versioning policy's deprecation window): an element may be REMOVED only if it was
# marked `@deprecated` in a release at least TWO MINOR versions before the baseline. A declared
# removal (hack/api-declared-breaks.sh lists them across every surface) is therefore checked by
# looking up the earliest release in which the element was deprecated, continuously, up to the
# baseline: git history is the source, not a field the author fills in.
#
#   deprecated in v1.1.0, baseline v1.3.0   2 minors   ok
#   deprecated in v1.2.0, baseline v1.3.0   1 minor    FAIL: deprecate it one more release first
#   not deprecated at the baseline                     FAIL: deprecate it, wait out the window, then remove
#
# 🔴 ARMED FROM 1.0.0. Before it nothing has been deprecated for two releases (the first
# `@deprecated` of the project does not exist yet) and every pre-1.0 removal would fail a rule that
# could not have been met, so a baseline below 1.0.0 REPORTS what would have failed and exits 0.
# `--enforce` arms it early, which is how the rule is exercised before there is anything to apply it
# to. The arming is the baseline's major, not a date: the day 1.0.0 is tagged the rule applies to
# the next release without anyone remembering to switch it on.
#
# WHAT IT CHECKS. GraphQL: fields, arguments, input fields and enum values, by reading the served
# schemas at every stable tag back to the first where the element stops being deprecated. The other
# surfaces cannot be read that way at a reasonable cost (a dcctl flag's deprecation needs the
# command tree BUILT at each tag; a C# [Obsolete] or an npm @deprecated tag is in source the reports
# do not record), so a removal declared there is printed as `window-unchecked`: the rule applies and
# a reviewer applies it. It is a visible gap, not a silent pass.
#
# Exit 1: a removal outside the window (armed), or a tree the check could not read. Anything that
# stops the comparison from happening is a different message.
set -euo pipefail

TOOL_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=hack/api-baseline.sh
. "$TOOL_ROOT/hack/api-baseline.sh"

# The window, in minor versions of the same major. A deprecation in an earlier MAJOR satisfies it
# outright: a major is the point at which removal is allowed at all.
WINDOW_MINORS=2

# The kinds of removal whose element can carry @deprecated. Removing a type, a union member or a
# schema cannot have been preceded by a deprecation of that thing, so those are `window-unchecked`.
DEPRECATABLE='^(field-removed|argument-removed|input-field-removed|enum-value-removed)$'

minor_of() { local v="${1#v}"; v="${v#*.}"; echo "${v%%.*}"; }
major_of() { local v="${1#v}"; echo "${v%%.*}"; }

# window_ok <deprecated-since-tag> <baseline-tag>: 0 when the gap is at least the window.
window_ok() {
  local since="$1" base="$2"
  [ "$(major_of "$since")" -lt "$(major_of "$base")" ] && return 0
  [ $(( $(minor_of "$base") - $(minor_of "$since") )) -ge "$WINDOW_MINORS" ]
}

# stable_tags_upto <data-root> <baseline>: stable release tags up to and including the baseline,
# newest first.
stable_tags_upto() {
  git -C "$1" tag --list 'v[0-9]*.[0-9]*.[0-9]*' | grep -v -- '-' | sort -V | awk -v b="$2" 'done == 0 { print } $0 == b { done = 1 }' | sort -rV
}

# deprecated_at <data-root> <tag> <out>: every `schema coordinate` deprecated in the tag's schemas.
deprecated_at() {
  local root="$1" tag="$2" out="$3" dir files
  dir="$(mktemp -d)"
  files="$(git -C "$root" ls-tree -r --name-only "$tag" backend/services | grep -E '^backend/services/[^/]+/graphql/[^/]+$' || true)"
  if [ -z "$files" ]; then
    : >"$out"
    rm -rf "$dir"
    return 0
  fi
  # shellcheck disable=SC2086 # one path per word is the point
  git -C "$root" archive --format=tar "$tag" $files | tar -x -C "$dir"
  if ! "$SDLCOMPAT" -list-deprecated "$dir" >"$out"; then
    rm -rf "$dir"
    return 1
  fi
  rm -rf "$dir"
}

# deprecated_since <data-root> <baseline> <schema> <coord> <cache-dir>: the earliest tag from which
# the element is deprecated continuously up to the baseline; empty when it is not deprecated at it.
deprecated_since() {
  local root="$1" baseline="$2" schema="$3" coord="$4" cache="$5" tag since=""
  while read -r tag; do
    if [ ! -f "$cache/$tag.txt" ]; then
      deprecated_at "$root" "$tag" "$cache/$tag.txt" || { echo "ERROR cannot read the schemas of $tag"; return 1; }
    fi
    if grep -qxF -- "$schema $coord" "$cache/$tag.txt"; then
      since="$tag"
    else
      break
    fi
  done < <(stable_tags_upto "$root" "$baseline")
  echo "$since"
}

# check <baseline> <data-root> <enforce 0|1>
check() {
  local baseline="$1" root="$2" enforce="$3" armed=0 declared cache failed=0 checked=0 unchecked=0
  if [ "$enforce" = 1 ] || [ "$(major_of "$baseline")" -ge 1 ]; then armed=1; fi

  declared="$("$TOOL_ROOT/hack/api-declared-breaks.sh" "$baseline" "$root")" || {
    echo "::error::could not list the declared breaks against $baseline; nothing was checked." >&2
    return 1
  }
  cache="$(mktemp -d)"
  local surface rest schema coord kind since line
  while IFS=$'\t' read -r surface rest; do
    [ -n "$surface" ] || continue
    # rest: <baseline> <schema|surface> <coordinate> <kind> <reason…>   (csharp: <baseline> csharp <symbol> <kind>)
    read -r _ schema coord kind _ <<<"$rest"
    if [ "$surface" != graphql ] || ! [[ "$kind" =~ $DEPRECATABLE ]]; then
      case "$kind" in
        *-removed* | *removed*)
          unchecked=$((unchecked + 1))
          echo "REPORT window-unchecked $surface $schema $coord $kind: the $WINDOW_MINORS-minor window applies; confirm by hand that it was deprecated in a release at least $WINDOW_MINORS minors before $baseline"
          ;;
      esac
      continue
    fi
    checked=$((checked + 1))
    since="$(deprecated_since "$root" "$baseline" "$schema" "$coord" "$cache")" || { echo "::error::$since" >&2; rm -rf "$cache"; return 1; }
    if [ -z "$since" ]; then
      line="$schema $coord $kind: it was not deprecated at $baseline; mark it @deprecated(reason: \"Use <replacement>.\"), let $WINDOW_MINORS minor releases ship, then remove it"
    elif ! window_ok "$since" "$baseline"; then
      line="$schema $coord $kind: deprecated since $since, only $(( $(minor_of "$baseline") - $(minor_of "$since") )) minor(s) before $baseline; the window is $WINDOW_MINORS"
    else
      echo "ok window $schema $coord: deprecated since $since"
      continue
    fi
    if [ "$armed" = 1 ]; then
      echo "FAIL window $line"
      failed=$((failed + 1))
    else
      echo "REPORT window $line (not enforced before 1.0.0)"
    fi
  done <<<"$declared"
  rm -rf "$cache"

  echo "deprecation-window: baseline $baseline, $checked GraphQL removal(s) checked, $unchecked on other surfaces unchecked, $failed failed; $([ "$armed" = 1 ] && echo enforced || echo "report-only (baseline is before 1.0.0)")"
  [ "$failed" -eq 0 ]
}

build_sdlcompat() {
  BIN_DIR="$(mktemp -d)"
  SDLCOMPAT="$BIN_DIR/sdlcompat"
  (cd "$TOOL_ROOT/backend/tools/docsgql" && go build -o "$SDLCOMPAT" ./cmd/sdlcompat)
}

self_test() {
  local tmp out rc f
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"; trap - RETURN' RETURN
  (
    cd "$tmp"
    git init -q .
    git config user.email t@example.com
    git config user.name t
    mkdir -p backend/services/foo/graphql sdks/csharp/src/DeviceChain.Sdk frontend/api \
      backend/tools/docsgql/sdlcompat backend/tools/docsgql/apicompat/clitree \
      backend/tools/docsgql/apicompat/jsonschema backend/tools/docsgql/apicompat/topics
    : >backend/tools/docsgql/sdlcompat/breaks.txt
    for f in clitree jsonschema topics; do : >"backend/tools/docsgql/apicompat/$f/breaks.txt"; done
    : >frontend/api/breaks.txt
    printf '#nullable enable\n' >sdks/csharp/src/DeviceChain.Sdk/PublicAPI.Unshipped.txt
    plain='type Query { a: Int b: Int }'
    dep='type Query { a: Int @deprecated(reason: "Use b.") b: Int }'
    cut() { # <tag> <schema>
      printf '%s\n' "$2" >backend/services/foo/graphql/schema.graphql
      git add -A && git commit -q --allow-empty -m "$1" && git tag "$1"
    }
    # 1.x: a deprecated in 1.1.0 and kept; b never deprecated.
    cut v1.0.0 "$plain"; cut v1.1.0 "$dep"; cut v1.2.0 "$dep"; cut v1.3.0 "$dep"
    # 0.x: the same shape, before the rule is armed.
    cut v0.1.0 "$plain"; cut v0.2.0 "$dep"; cut v0.3.0 "$dep"
  )
  declare_break() { # <baseline> <coord>
    printf '%s foo %s field-removed self-test\n' "$1" "$2" >>"$tmp/backend/tools/docsgql/sdlcompat/breaks.txt"
  }
  run() { # <args…> ; sets out and rc
    rc=0; out="$("$0" "$@" 2>&1)" || rc=$?
  }
  # 1. Deprecated two minors before the baseline: removal allowed.
  declare_break v1.3.0 Query.a
  run v1.3.0 --root "$tmp"
  check_rc "a removal two minors after its deprecation" 0 "$rc" "$out" || return 1
  check_has "a removal two minors after its deprecation" "ok window foo Query.a: deprecated since v1.1.0" "$out" || return 1
  # 2. Never deprecated: refused, with what to do.
  declare_break v1.3.0 Query.b
  run v1.3.0 --root "$tmp"
  check_rc "a removal of an element that was never deprecated" 1 "$rc" "$out" || return 1
  check_has "an element never deprecated" "FAIL window foo Query.b field-removed: it was not deprecated at v1.3.0" "$out" || return 1
  # 3. Deprecated one minor before the baseline: refused.
  declare_break v1.2.0 Query.a
  run v1.2.0 --root "$tmp"
  check_rc "a removal one minor after its deprecation" 1 "$rc" "$out" || return 1
  check_has "a removal one minor after its deprecation" "deprecated since v1.1.0, only 1 minor(s) before v1.2.0" "$out" || return 1
  # 4. Before 1.0.0 the same removal is reported and passes; --enforce arms it.
  declare_break v0.3.0 Query.a
  run v0.3.0 --root "$tmp"
  check_rc "a pre-1.0 removal inside the window" 0 "$rc" "$out" || return 1
  declare_break v0.2.0 Query.a
  run v0.2.0 --root "$tmp"
  check_rc "a pre-1.0 removal outside the window" 0 "$rc" "$out" || return 1
  check_has "a pre-1.0 removal outside the window" "REPORT window foo Query.a" "$out" || return 1
  run v0.2.0 --root "$tmp" --enforce
  check_rc "--enforce" 1 "$rc" "$out" || return 1
  # 5. A removal on a surface the check cannot read is printed, never silently passed.
  printf 'v1.3.0 dcctl install:--dev flag-removed self-test\n' >>"$tmp/backend/tools/docsgql/apicompat/clitree/breaks.txt"
  run v1.3.0 --root "$tmp"
  check_has "a non-GraphQL removal" "REPORT window-unchecked dcctl" "$out" || return 1
  # 6. The window arithmetic on its own, including the major boundary.
  window_ok v1.1.0 v1.3.0 || { echo "self-test: 2 minors is inside the window" >&2; return 1; }
  ! window_ok v1.2.0 v1.3.0 || { echo "self-test: 1 minor is outside the window" >&2; return 1; }
  window_ok v1.9.0 v2.0.0 || { echo "self-test: an earlier major satisfies the window" >&2; return 1; }
  echo "deprecation-window self-test: two minors pass; one minor and never-deprecated fail naming the element; pre-1.0 reports; --enforce arms it; other surfaces are listed as unchecked"
}

MODE=check ENFORCE=0 ROOT_ARG="" BASE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --self-test) MODE=self-test ;;
    --enforce) ENFORCE=1 ;;
    --root) shift; ROOT_ARG="${1:-}" ;;
    -*) echo "usage: $0 [baseline-tag] [--enforce] | --self-test" >&2; exit 2 ;;
    *) BASE="$1" ;;
  esac
  shift
done

command -v go >/dev/null 2>&1 || { echo "::error::go is not available; the deprecation-window check cannot run." >&2; exit 1; }
build_sdlcompat
trap 'rm -rf "$BIN_DIR"' EXIT

if [ "$MODE" = self-test ]; then
  self_test
  exit
fi

DATA_ROOT="${ROOT_ARG:-$TOOL_ROOT}"
cd "$DATA_ROOT"
baseline="$(resolve_baseline "$BASE")" || exit 1
echo "deprecation-window baseline: $baseline"
log="$(mktemp)"
rc=0
check "$baseline" "$DATA_ROOT" "$ENFORCE" 2>&1 | tee "$log" || rc=${PIPESTATUS[0]}
summarize "Deprecation window (${baseline})" "$log"
if [ "$rc" -ne 0 ]; then
  echo "::error::a declared removal falls outside the deprecation window. The FAIL lines above name each element." >&2
  exit 1
fi
