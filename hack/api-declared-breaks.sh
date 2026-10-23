#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Every intended break of a covered interface that is declared against a baseline, across ALL
# surfaces, one per line:
#
#   hack/api-declared-breaks.sh <baseline-tag> [repo-root]
#   hack/api-declared-breaks.sh --self-test
#
#   graphql   <baseline> <schema> <coordinate> <kind> <reason>
#   dcctl     <baseline> <surface> <coordinate> <kind> <reason>
#   contract  ...                                              (the device wire)
#   topics    ...
#   npm       <baseline> <package> <id> <kind> <reason>
#   csharp    <baseline> csharp <symbol> member-removed-or-changed
#
# This is the one place that knows where every surface keeps its declarations, so the release
# guard (check-release-highlights.sh) and the deprecation-window check read the same list. A
# surface added to the gate is added HERE, and the self-test fails if one of the files below
# goes missing - a list of declarations that quietly lost a source would let a release ship a
# break with no highlight.
#
# A line is LIVE only while its baseline-tag IS the baseline; the others are expired and are
# not printed. The C# surface declares by `*REMOVED*` lines in PublicAPI.Unshipped.txt, which
# carry no tag: a line is live unless the baseline release's own Unshipped.txt already had it
# (it shipped with that release and has not been promoted into Shipped.txt yet).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Relative to the repository root. Every one must exist: a missing file is an error, not an
# empty list.
DECLARATION_FILES=(
  "graphql:backend/tools/docsgql/sdlcompat/breaks.txt"
  "dcctl:backend/tools/docsgql/apicompat/clitree/breaks.txt"
  "contract:backend/tools/docsgql/apicompat/jsonschema/breaks.txt"
  "topics:backend/tools/docsgql/apicompat/topics/breaks.txt"
  "npm:frontend/api/breaks.txt"
)
UNSHIPPED=sdks/csharp/src/DeviceChain.Sdk/PublicAPI.Unshipped.txt

declared_breaks() { # <baseline> <root>
  local baseline="$1" root="$2" entry surface file
  for entry in "${DECLARATION_FILES[@]}"; do
    surface="${entry%%:*}"
    file="$root/${entry#*:}"
    [ -f "$file" ] || { echo "api-declared-breaks: $file does not exist; the list of declarations would be incomplete" >&2; return 2; }
    # Comments and blanks out; the first token is the baseline tag.
    tr -d '\r' <"$file" | awk -v b="$baseline" -v s="$surface" '
      /^[[:space:]]*(#|$)/ { next }
      $1 == b { print s "\t" $0 }'
  done

  local u="$root/$UNSHIPPED"
  [ -f "$u" ] || { echo "api-declared-breaks: $u does not exist; the list of declarations would be incomplete" >&2; return 2; }
  local shipped_with
  shipped_with="$(mktemp)"
  if git -C "$root" cat-file -e "${baseline}:${UNSHIPPED}" 2>/dev/null; then
    git -C "$root" show "${baseline}:${UNSHIPPED}" | tr -d '\r' >"$shipped_with"
  fi
  tr -d '\r' <"$u" | grep '^\*REMOVED\*' | grep -vxF -f "$shipped_with" \
    | sed -e 's/^\*REMOVED\*//' -e "s/^/csharp\t$baseline csharp /" -e 's/$/ member-removed-or-changed/' || true
  rm -f "$shipped_with"
}

self_test() {
  local tmp out
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"; trap - RETURN' RETURN
  (
    cd "$tmp"
    git init -q .
    git config user.email t@example.com
    git config user.name t
    local entry f
    for entry in "${DECLARATION_FILES[@]}"; do
      f="${entry#*:}"
      mkdir -p "$(dirname "$f")"
      printf '# header\n\nv0.1.0 x a kind live one\nv0.0.9 x b kind expired one\nv0.1.0 x c kind live two\n' >"$f"
    done
    mkdir -p "$(dirname "$UNSHIPPED")"
    printf '#nullable enable\n*REMOVED*A.Old\nA.New\n' >"$UNSHIPPED"
    git add -A
    git commit -qm base
    git tag v0.1.0
    # The next cycle: an old removal already shipped with v0.1.0, a new one since.
    printf '#nullable enable\n*REMOVED*A.Old\n*REMOVED*A.Newer\n' >"$UNSHIPPED"
  )
  out="$(declared_breaks v0.1.0 "$tmp")"
  local want=$(( ${#DECLARATION_FILES[@]} * 2 + 1 ))
  [ "$(grep -c . <<<"$out")" -eq "$want" ] || { echo "$out"; echo "self-test: want $want live lines (2 per file + 1 C#), got $(grep -c . <<<"$out")" >&2; return 1; }
  grep -q $'^npm\tv0.1.0 x c kind live two$' <<<"$out" || { echo "$out"; echo "self-test: an npm declaration is missing" >&2; return 1; }
  grep -q $'^csharp\tv0.1.0 csharp A.Newer member-removed-or-changed$' <<<"$out" || { echo "$out"; echo "self-test: the new C# removal is missing" >&2; return 1; }
  if grep -q 'A.Old' <<<"$out"; then echo "$out"; echo "self-test: a C# removal that shipped with the baseline was counted as live" >&2; return 1; fi
  if grep -q 'expired' <<<"$out"; then echo "$out"; echo "self-test: an expired declaration was counted as live" >&2; return 1; fi
  # A baseline nothing is declared against yields nothing.
  [ -z "$(declared_breaks v9.9.9 "$tmp" | grep -v csharp || true)" ] || { echo "self-test: a baseline with no declarations printed some" >&2; return 1; }
  # A missing declarations file is an error, not a shorter list.
  rm "$tmp/frontend/api/breaks.txt"
  if declared_breaks v0.1.0 "$tmp" >/dev/null 2>&1; then echo "self-test: a missing declarations file did not fail" >&2; return 1; fi
  echo "api-declared-breaks self-test: live lines from every surface; expired and already-shipped ones excluded; a missing file fails"
}

case "${1:-}" in
  --self-test) self_test; exit ;;
  "") echo "usage: $0 <baseline-tag> [repo-root] | --self-test" >&2; exit 2 ;;
esac
declared_breaks "$1" "${2:-$(cd "$SCRIPT_DIR/.." && pwd)}"
