#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Print the tag a release's changelog starts from.
#
#   hack/changelog-base.sh <tag>        # e.g. v1.7.0 or v1.7.0-rc.1; reads this checkout's tags
#   hack/changelog-base.sh --self-test  # planted tag lists, each with its required answer
#
# THE RULE. The base is the highest stable tag (vX.Y.Z, no prerelease suffix) that is
# semver-LOWER than <tag>, whether or not it is reachable from <tag>. Release candidates are
# never a base: a reader wants "what changed since the last release I could have been running",
# and that never depends on how many candidates it took to get here.
#
# WHY NOT "THE NEWEST STABLE TAG REACHABLE FROM <tag>". That was the rule, and it skips any
# release cut from a release branch. vX.Y.0 is tagged on release-X.Y (the freeze commit plus
# whatever was fixed on the branch), which main never contains, so for the next minor the walk
# passes it and lands one minor too far back:
#   - v1.5.0's changelog started at v1.3.0, because v1.4.0 sat on release-1.4;
#   - v1.7.0's would have started at v1.5.0, because v1.6.0 sits on release-1.6.
# Measuring from an unreachable tag is still the right range: the generator compares
# base...tag from their merge base, which is the previous minor's freeze commit, so the range
# is exactly "main since the previous freeze".
#
# Semver-lower, not reachable, also settles the case the old rule needed a same-commit check
# for: a candidate re-run after its stable tag was pinned to the same commit. vX.Y.Z is higher
# than vX.Y.Z-rc.N, so it is never the candidate's base.
#
# OUTPUT AND EXIT STATUS.
#   0, the base on stdout             - the base was found
#   0, nothing on stdout, a notice    - the FIRST release: no stable tag exists other than <tag>'s
#                                       own X.Y.Z, so there is nothing to measure from and the
#                                       caller keeps the generator's own range
#   1, an error on stderr             - no base can be determined: <tag> is not vX.Y.Z or
#                                       vX.Y.Z-<prerelease>, the tag listing failed, or stable
#                                       tags exist but none is lower than <tag>
set -euo pipefail

STABLE_RE='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
TAG_RE='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'

# pick <tag>: the tag list on stdin, one per line; prints the base. Pure, so the self-test can
# drive it with literal lists.
pick() {
  local cur="$1" cmaj cmin cpat t maj min pat best="" bmaj=-1 bmin=-1 bpat=-1 stable=0
  if ! [[ "$cur" =~ $TAG_RE ]]; then
    echo "changelog-base: '$cur' is not a release tag (vX.Y.Z or vX.Y.Z-<prerelease>)" >&2
    return 1
  fi
  cmaj="${BASH_REMATCH[1]}" cmin="${BASH_REMATCH[2]}" cpat="${BASH_REMATCH[3]}"
  while IFS= read -r t; do
    t="${t%$'\r'}"
    [[ "$t" =~ $STABLE_RE ]] || continue
    maj="${BASH_REMATCH[1]}" min="${BASH_REMATCH[2]}" pat="${BASH_REMATCH[3]}"
    # The release's own X.Y.Z is skipped BEFORE it is counted: it is the tag being released, or
    # (for a candidate) the release that candidate is for. Counting it made the first stable
    # release refuse - v0.1.0 with v0.1.0-rc.1 and v0.1.0 tagged saw one stable tag, none lower,
    # and exited 1 instead of reporting a first release.
    (( maj == cmaj && min == cmin && pat == cpat )) && continue
    stable=$((stable + 1))
    # Strictly lower than the current tag's X.Y.Z.
    if (( maj < cmaj || (maj == cmaj && (min < cmin || (min == cmin && pat < cpat))) )); then
      if (( maj > bmaj || (maj == bmaj && (min > bmin || (min == bmin && pat > bpat))) )); then
        best="$t" bmaj="$maj" bmin="$min" bpat="$pat"
      fi
    fi
  done
  if [ -n "$best" ]; then
    echo "$best"
  elif [ "$stable" -eq 0 ]; then
    echo "changelog-base: no stable tag exists yet; '$cur' is the first release, so there is no base" >&2
  else
    echo "changelog-base: $stable stable tag(s) exist but none is lower than '$cur'; refusing to guess a base" >&2
    return 1
  fi
}

self_test() {
  local fails=0 got rc
  # expect <name> <tag> <want-or-FAIL-or-EMPTY> <tag list...>
  expect() {
    local name="$1" tag="$2" want="$3"
    shift 3
    rc=0
    got="$(printf '%s\n' "$@" | pick "$tag" 2>/dev/null)" || rc=$?
    case "$want" in
      FAIL) [ "$rc" -ne 0 ] || { echo "  FAIL $name: wanted a refusal, got '$got' (exit 0)"; fails=$((fails + 1)); return; } ;;
      EMPTY) [ "$rc" -eq 0 ] && [ -z "$got" ] || { echo "  FAIL $name: wanted no base, got '$got' (exit $rc)"; fails=$((fails + 1)); return; } ;;
      *) [ "$rc" -eq 0 ] && [ "$got" = "$want" ] || { echo "  FAIL $name: wanted $want, got '$got' (exit $rc)"; fails=$((fails + 1)); return; } ;;
    esac
    echo "  ok   $name"
  }
  # The real tag history around the two releases this rule exists for. Reachability plays no
  # part in pick(), so listing them is the whole fixture.
  local -a hist=(v1.3.0-rc.1 v1.3.0 v1.4.0-rc.1 v1.4.0-rc.2 v1.4.0 v1.5.0-rc.1 v1.5.0 v1.6.0-rc.1 v1.6.0)
  expect "v1.7.0 starts at v1.6.0"                  v1.7.0       v1.6.0 "${hist[@]}"
  expect "v1.6.0 starts at v1.5.0"                  v1.6.0       v1.5.0 "${hist[@]}"
  expect "v1.5.0 starts at v1.4.0 (not v1.3.0)"     v1.5.0       v1.4.0 "${hist[@]}"
  expect "an rc starts at the previous stable"      v1.7.0-rc.1  v1.6.0 "${hist[@]}"
  expect "a later rc ignores its own earlier rc"    v1.7.0-rc.2  v1.6.0 "${hist[@]}" v1.7.0-rc.1
  expect "an rc re-run after its stable tag"        v1.7.0-rc.2  v1.6.0 "${hist[@]}" v1.7.0-rc.1 v1.7.0-rc.2 v1.7.0
  expect "a stable re-run ignores itself"           v1.7.0       v1.6.0 "${hist[@]}" v1.7.0
  expect "a patch on an old branch"                 v1.5.1       v1.5.0 "${hist[@]}" v1.7.0
  expect "a patch after the next minor"             v1.6.1       v1.6.0 "${hist[@]}" v1.7.0 v1.7.1
  expect "numeric, not lexical (v1.10 > v1.9)"      v1.11.0      v1.10.0 v1.2.0 v1.9.0 v1.10.0
  expect "numeric patch (v1.9.10 > v1.9.9)"         v1.10.0      v1.9.10 v1.9.9 v1.9.10 v1.9.2
  expect "a major boundary"                         v2.0.0       v1.9.0 v1.8.0 v1.9.0 v2.0.0-rc.1
  expect "prerelease-only history is a first release" v0.1.0     EMPTY  v0.1.0-rc.1 v0.1.0-rc.2
  expect "no tags at all is a first release"        v0.1.0       EMPTY  ""
  expect "the first stable release ignores itself"  v0.1.0       EMPTY  v0.1.0-rc.1 v0.1.0
  expect "the first release's rc re-run after it"   v0.1.0-rc.2  EMPTY  v0.1.0-rc.1 v0.1.0-rc.2 v0.1.0
  expect "malformed tags are not stable"            v1.7.0       v1.6.0 v1.6.0 v1.6.01 v1.6.9.1 v1.6.9x release-1.6 "v1.6.5 "
  expect "stable tags but none lower is refused"    v0.9.0       FAIL   v1.0.0 v1.1.0
  expect "a non-release tag is refused"             release-1.7  FAIL   v1.6.0
  expect "a branch-style name is refused"           v1.7         FAIL   v1.6.0
  expect "an empty tag is refused"                  ""           FAIL   v1.6.0
  if [ "$fails" -ne 0 ]; then
    echo "==> changelog-base self-test: $fails failure(s)" >&2
    return 1
  fi
  echo "==> changelog-base self-test passed."
}

main() {
  case "${1:-}" in
    --self-test) self_test ;;
    "" | -h | --help)
      sed -n '5,7p' "$0" >&2
      return 2
      ;;
    *)
      local tags
      # Its own statement, so a failing `git tag` stops the script instead of reading as an
      # empty (first-release) listing.
      tags="$(git tag --list 'v*')"
      printf '%s\n' "$tags" | pick "$1"
      ;;
  esac
}

main "$@"
