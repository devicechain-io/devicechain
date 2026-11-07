#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The long-term-support (LTS) line's rules, in one place that the release workflow, the
# backport workflow and the LTS upgrade drill all call. Two guards that must agree are one
# guard with two callers.
#
#   hack/lts.sh policy                 print the designated LTS line (X.Y) or `none`
#   hack/lts.sh branch X.Y             print the branch name for a line: release-X.Y-lts
#   hack/lts.sh line-of-branch NAME    print X.Y for a release-X.Y-lts branch name; fail otherwise
#   hack/lts.sh line-of-tag TAG        print X.Y for a vX.Y.Z[-pre] tag
#   hack/lts.sh check-tag TAG          fail when TAG is cut from the wrong place (see below)
#   hack/lts.sh moves-latest TAG       print true when TAG should move the `latest` tags
#   hack/lts.sh eligible LABEL...      exit 0 when the labels make a fix backport-eligible
#   hack/lts.sh skip-minor FROM TO     exit 0 when FROM -> TO skips at least one whole minor
#   hack/lts.sh --self-test
#
# THE CONVENTION. An LTS line X.Y lives on the branch `release-X.Y-lts`, cut from the X.Y.0
# tag. Patch releases (vX.Y.Z) are tagged FROM THAT BRANCH, never from main: a patch tagged
# from main would ship every unrelated change merged since, under a version number that
# promises security and critical fixes only.
#
# check-tag enforces exactly that and nothing else: when the branch for a tag's line EXISTS
# (locally or on origin), the tag's commit must be an ancestor of it. When no such branch
# exists the tag is cut the way every release has always been cut, and the check says nothing,
# so it is a no-op until a line is actually branched.
#
# moves-latest: `latest` (images, console image, npm dist-tag) moves only for a stable tag that
# is the highest stable tag in the repository. Without this a v1.2.1 LTS patch, cut after
# v1.3.0, would move `latest` BACKWARDS onto the old line for every operator who tracks it.
#
# eligible: only a fix labelled `security` or `critical` is proposed for backport. Exact label
# names, nothing fuzzy: a label that merely contains the word does not count.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
policy_file="${DC_LTS_POLICY:-$repo_root/hack/lts-policy}"

die() { printf 'lts.sh: %s\n' "$*" >&2; exit 1; }

stable_re='^v([0-9]+)\.([0-9]+)\.([0-9]+)$'
any_re='^v([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?$'
line_re='^[0-9]+\.[0-9]+$'

# line_of TAG prints X.Y for a vX.Y.Z[-pre] tag.
line_of() {
  [[ "$1" =~ $any_re ]] || die "'$1' is not a vX.Y.Z[-pre] tag"
  printf '%s.%s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
}

cmd_policy() {
  [[ -f "$policy_file" ]] || die "the policy file $policy_file does not exist; refusing to guess which line is LTS"
  local n value
  n="$(grep -c '^current-lts:' "$policy_file" || true)"
  [[ "$n" == 1 ]] || die "$policy_file carries $n current-lts lines; it must carry exactly one"
  value="$(sed -n 's/^current-lts:[[:space:]]*//p' "$policy_file" | sed 's/[[:space:]]*$//')"
  if [[ "$value" == none ]] || [[ "$value" =~ $line_re ]]; then
    printf '%s\n' "$value"
  else
    die "current-lts '$value' is neither 'none' nor an X.Y line"
  fi
}

cmd_branch() {
  [[ "${1:-}" =~ $line_re ]] || die "'${1:-}' is not an X.Y line"
  printf 'release-%s-lts\n' "$1"
}

cmd_line_of_branch() {
  [[ "${1:-}" =~ ^release-([0-9]+\.[0-9]+)-lts$ ]] || die "'${1:-}' is not a release-X.Y-lts branch"
  printf '%s\n' "${BASH_REMATCH[1]}"
}

# branch_ref prints a ref for the line's branch if one exists, locally or on origin.
branch_ref() {
  local b="$1" ref
  for ref in "refs/heads/$b" "refs/remotes/origin/$b"; do
    if git -C "$repo_root" rev-parse --verify -q "$ref^{commit}" >/dev/null; then
      printf '%s\n' "$ref"
      return 0
    fi
  done
  return 1
}

cmd_check_tag() {
  local tag="${1:-}" line b ref
  line="$(line_of "$tag")" || return 1
  b="$(cmd_branch "$line")" || return 1
  if ! ref="$(branch_ref "$b")"; then
    echo "lts.sh: no $b branch exists, so $tag is cut the ordinary way."
    return 0
  fi
  git -C "$repo_root" rev-parse --verify -q "refs/tags/$tag^{commit}" >/dev/null ||
    die "tag $tag does not exist in this checkout; a shallow clone is the usual cause"
  if git -C "$repo_root" merge-base --is-ancestor "refs/tags/$tag^{commit}" "$ref"; then
    echo "lts.sh: $tag is on $b."
    return 0
  fi
  die "$tag is not an ancestor of $b. Line $line is branched for long-term support, so its releases are cut from that branch, never from main: a tag from main would ship every unrelated change merged since under a patch number that promises security and critical fixes only. Cherry-pick the fix onto $b and tag there."
}

cmd_moves_latest() {
  local tag="${1:-}" highest
  if [[ ! "$tag" =~ $stable_re ]]; then
    printf 'false\n'
    return 0
  fi
  highest="$({ git -C "$repo_root" tag -l 'v[0-9]*.[0-9]*.[0-9]*'; echo "$tag"; } | grep -E "$stable_re" | sort -V | tail -1 || true)"
  # The tag itself is always a candidate (it may not be visible to this checkout yet), so the list is never empty.
  if [[ "$highest" == "$tag" ]]; then
    printf 'true\n'
  else
    printf 'false\n'
  fi
}

cmd_eligible() {
  local l
  for l in "$@"; do
    case "$l" in
    security | critical) return 0 ;;
    esac
  done
  return 1
}

cmd_skip_minor() {
  local from="${1:-}" to="${2:-}" fmaj fmin tmaj tmin
  [[ "$from" =~ $stable_re ]] || die "FROM '$from' is not a stable vX.Y.Z tag"
  fmaj="${BASH_REMATCH[1]}"
  fmin="${BASH_REMATCH[2]}"
  # TO may be a candidate of the release it names: v1.5.0-rc.1 is a hop to the 1.5 line.
  [[ "$to" =~ $any_re ]] || die "TO '$to' is not a vX.Y.Z[-pre] tag"
  tmaj="${BASH_REMATCH[1]}"
  tmin="${BASH_REMATCH[2]}"
  [[ "$fmaj" == "$tmaj" ]] || die "$from -> $to crosses a major version; a major upgrade is not an LTS hop"
  if ((tmin - fmin < 2)); then
    die "$from -> $to skips no minor (the line moves by $((tmin - fmin))); the ordinary upgrade gate already drills that hop"
  fi
}

self_test() {
  local fails=0 tmp
  tmp="$(mktemp -d)"
  # expect NAME WANT_RC CMD... runs the command in a subshell and compares its exit status.
  expect() {
    local name="$1" want="$2" rc=0
    shift 2
    ("$@") >/dev/null 2>&1 || rc=$?
    if [[ "$rc" == "$want" ]]; then
      printf 'ok   %s\n' "$name"
    else
      printf 'FAIL %s (exit %s, wanted %s)\n' "$name" "$rc" "$want" >&2
      fails=$((fails + 1))
    fi
  }
  # expect_out NAME WANT CMD... compares stdout.
  expect_out() {
    local name="$1" want="$2" got
    shift 2
    got="$("$@" 2>/dev/null || true)"
    if [[ "$got" == "$want" ]]; then
      printf 'ok   %s\n' "$name"
    else
      printf 'FAIL %s (got %q, wanted %q)\n' "$name" "$got" "$want" >&2
      fails=$((fails + 1))
    fi
  }

  # --- eligible: exact labels only
  expect "eligible: security" 0 cmd_eligible security
  expect "eligible: critical among others" 0 cmd_eligible bug critical
  expect "eligible: no labels" 1 cmd_eligible
  expect "eligible: unrelated label" 1 cmd_eligible bug enhancement
  expect "eligible: substring is not a match" 1 cmd_eligible security-review not-critical
  expect "eligible: case is exact" 1 cmd_eligible Security CRITICAL

  # --- skip-minor
  expect "skip-minor: 1.2 -> 1.4 skips 1.3" 0 cmd_skip_minor v1.2.0 v1.4.0
  expect "skip-minor: patch of the from line" 0 cmd_skip_minor v1.2.3 v1.5.0
  expect "skip-minor: candidate target" 0 cmd_skip_minor v1.2.0 v1.4.0-rc.1
  expect "skip-minor: adjacent minors skip nothing" 1 cmd_skip_minor v1.2.0 v1.3.0
  expect "skip-minor: same line skips nothing" 1 cmd_skip_minor v1.2.0 v1.2.5
  expect "skip-minor: downgrade" 1 cmd_skip_minor v1.5.0 v1.2.0
  expect "skip-minor: major crossing" 1 cmd_skip_minor v1.2.0 v2.4.0
  expect "skip-minor: from must be stable" 1 cmd_skip_minor v1.2.0-rc.1 v1.5.0
  expect "skip-minor: garbage" 1 cmd_skip_minor 1.2 v1.5.0

  expect_out "line-of-tag: a candidate" 1.5 line_of v1.5.0-rc.1
  expect "line-of-tag: not a tag" 1 line_of main

  # --- branch naming
  expect_out "branch name" release-1.2-lts cmd_branch 1.2
  expect "branch: rejects a tag" 1 cmd_branch v1.2.0
  expect_out "line-of-branch" 1.12 cmd_line_of_branch release-1.12-lts
  expect "line-of-branch: rejects main" 1 cmd_line_of_branch main
  expect "line-of-branch: rejects a near miss" 1 cmd_line_of_branch release-1.2

  # --- policy file
  with_policy() { (policy_file="$1"; shift; "$@"); }
  printf 'current-lts: none\n' >"$tmp/p1"
  printf '# c\ncurrent-lts: 1.2\n' >"$tmp/p2"
  printf 'current-lts: 1.2\ncurrent-lts: 1.3\n' >"$tmp/p3"
  printf 'current-lts: v1.2\n' >"$tmp/p4"
  printf '# nothing\n' >"$tmp/p5"
  expect_out "policy: none" none with_policy "$tmp/p1" cmd_policy
  expect_out "policy: a line" 1.2 with_policy "$tmp/p2" cmd_policy
  expect "policy: duplicate lines" 1 with_policy "$tmp/p3" cmd_policy
  expect "policy: malformed line" 1 with_policy "$tmp/p4" cmd_policy
  expect "policy: absent declaration" 1 with_policy "$tmp/p5" cmd_policy
  expect "policy: missing file" 1 with_policy "$tmp/nope" cmd_policy
  expect "the committed policy parses" 0 cmd_policy

  # --- check-tag and moves-latest against a real throwaway repository
  local r="$tmp/repo" e="$tmp/empty" gg
  git init -q "$r"
  git init -q "$e"
  gg() { git -C "$r" -c user.name=t -c user.email=t@t -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
  gg commit -q --allow-empty -m base
  gg tag v1.2.0
  gg branch release-1.2-lts
  gg commit -q --allow-empty -m "main moves on"
  gg tag v1.3.0
  gg checkout -q release-1.2-lts
  gg commit -q --allow-empty -m "backported fix"
  gg tag v1.2.1
  gg checkout -q -b scratch v1.3.0
  gg commit -q --allow-empty -m "main-only"
  gg tag v1.2.2 # WRONG: a 1.2 patch cut from main
  gg tag v1.4.0-rc.1
  at() { (repo_root="$r"; "$@"); }
  expect "check-tag: patch cut from the LTS branch" 0 at cmd_check_tag v1.2.1
  expect "check-tag: the .0 the branch was cut from" 0 at cmd_check_tag v1.2.0
  expect "check-tag: a patch cut from main is refused" 1 at cmd_check_tag v1.2.2
  expect "check-tag: a line with no LTS branch is untouched" 0 at cmd_check_tag v1.3.0
  expect "check-tag: a candidate of an unbranched line" 0 at cmd_check_tag v1.4.0-rc.1
  expect "check-tag: a tag that does not exist" 1 at cmd_check_tag v1.2.9
  expect "check-tag: not a version" 1 at cmd_check_tag main
  expect_out "moves-latest: the highest stable tag" true at cmd_moves_latest v1.3.0
  expect_out "moves-latest: an LTS patch behind a newer minor" false at cmd_moves_latest v1.2.1
  expect_out "moves-latest: a prerelease never" false at cmd_moves_latest v1.4.0-rc.1
  expect_out "moves-latest: not a tag" false at cmd_moves_latest main
  at_empty() { (repo_root="$e"; "$@"); }
  expect_out "moves-latest: nothing visible" true at_empty cmd_moves_latest v1.0.0
  # version order, not text order: 1.10 is newer than 1.9
  local v="$tmp/vrepo"
  git init -q "$v"
  git -C "$v" -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -q --allow-empty -m v
  git -C "$v" -c tag.gpgsign=false tag v1.9.0
  git -C "$v" -c tag.gpgsign=false tag v1.10.0
  at_v() { (repo_root="$v"; "$@"); }
  expect_out "moves-latest: 1.10 beats 1.9" true at_v cmd_moves_latest v1.10.0
  expect_out "moves-latest: 1.9 does not beat 1.10" false at_v cmd_moves_latest v1.9.0
  expect_out "moves-latest: a newer tag not yet visible to this checkout" true at_v cmd_moves_latest v1.11.0

  rm -rf "$tmp"
  if ((fails > 0)); then
    printf 'lts.sh self-test: %d failed\n' "$fails" >&2
    return 1
  fi
  echo "lts.sh self-test: all passed"
}

main() {
  case "${1:-}" in
  policy) cmd_policy ;;
  branch) shift; cmd_branch "$@" ;;
  line-of-branch) shift; cmd_line_of_branch "$@" ;;
  line-of-tag) shift; line_of "${1:-}" ;;
  check-tag) shift; cmd_check_tag "$@" ;;
  moves-latest) shift; cmd_moves_latest "$@" ;;
  eligible) shift; cmd_eligible "$@" ;;
  skip-minor) shift; cmd_skip_minor "$@" ;;
  --self-test) self_test ;;
  *) die "usage: lts.sh policy | branch X.Y | line-of-branch NAME | line-of-tag TAG | check-tag TAG | moves-latest TAG | eligible LABEL... | skip-minor FROM TO | --self-test" ;;
  esac
}

# Sourceable: hack/lts-upgrade-drill.sh reuses these functions rather than re-implementing them.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
