#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The git half of the LTS backport bot (.github/workflows/lts-backport.yml): decide whether a merged
# fix is to be proposed for the LTS branch and, if so, build the cherry-pick branch locally.
#
#   hack/lts-backport.sh prepare PR SHA LABELS [FILES]   LABELS is one label per line; FILES is a file listing the PR's paths
#                                                          (optional); prints key=value lines
#   hack/lts-backport.sh --self-test
#
# It never pushes and never opens a pull request -- the workflow does that with the result -- and it
# never merges anything. A backport is a PROPOSAL: the pull request it becomes runs the full CI matrix
# against the LTS branch and waits for a maintainer, exactly as any other.
#
# The status it prints (always exactly one `status=` line, exit 0), so the workflow can branch on it:
#   no-lts      hack/lts-policy designates no line; nothing to propose
#   ineligible  the fix carries neither the `security` nor the `critical` label
#   already     the commit is already on the LTS branch
#   partial     the merged commit does not carry the whole pull request (FILES differs from the commit's own paths,
#               as after a rebase merge of several commits); a human backports it
#   exists      a backport branch for this PR already exists on origin (a re-run, or a re-label)
#   conflict    the cherry-pick does not apply; a human backports it (the workflow says so on the PR)
#   ready       branch=<name> holds the cherry-picked commit, ready to push
# A designated line whose branch is missing is a CONFIGURATION error, and exits 1: staying quiet
# would turn "LTS is designated" into a promise nobody is keeping.

set -euo pipefail

this_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lts.sh
source "$this_dir/lts.sh"
repo_root="${DC_LTS_REPO:-$(cd "$this_dir/.." && pwd)}"

g() { git -C "$repo_root" "$@"; }

cmd_prepare() {
  local pr="${1:-}" sha="${2:-}" labels="${3:-}" files_file="${4:-}" lts branch ref target parents
  [[ "$pr" =~ ^[0-9]+$ ]] || die "PR '$pr' is not a pull request number"
  [[ "$sha" =~ ^[0-9a-f]{7,64}$ ]] || die "SHA '$sha' is not a commit id"
  lts="$(cmd_policy)" || return 1
  if [[ "$lts" == none ]]; then
    echo "status=no-lts"
    return 0
  fi
  local -a lbl
  # One label per line: a label name may itself contain a comma (`bug,security` is ONE label, not two).
  mapfile -t lbl <<<"$labels"
  if ! cmd_eligible "${lbl[@]}"; then
    echo "status=ineligible"
    return 0
  fi
  branch="$(cmd_branch "$lts")"
  ref=""
  for cand in "refs/remotes/origin/$branch" "refs/heads/$branch"; do
    if g rev-parse --verify -q "$cand^{commit}" >/dev/null; then
      ref="$cand"
      break
    fi
  done
  [[ -n "$ref" ]] || die "the LTS line is $lts but branch $branch does not exist; create it from the v$lts.0 tag"
  g rev-parse --verify -q "$sha^{commit}" >/dev/null || die "commit $sha is not in this checkout"
  if g merge-base --is-ancestor "$sha" "$ref"; then
    echo "status=already"
    return 0
  fi
  target="backport/pr-$pr-to-$branch"
  if g rev-parse --verify -q "refs/remotes/origin/$target^{commit}" >/dev/null; then
    echo "status=exists"
    return 0
  fi
  # The merged commit must carry the WHOLE change. A rebase merge reports only the last of several commits as the
  # merge commit, and cherry-picking that alone would propose an incomplete fix, so when the caller supplies the
  # pull request's file list the commit's own file list must equal it, else a human backports it.
  if [[ -n "$files_file" ]]; then
    if [[ "$(g diff --name-only "$sha^1" "$sha" | sort)" != "$(sort "$files_file")" ]]; then
      echo "status=partial"
      return 0
    fi
  fi
  parents="$(g rev-list --parents -n1 "$sha" | wc -w)"
  g checkout -q -B "$target" "$ref"
  local -a pick=(cherry-pick -x)
  # A merge commit (a "Create a merge commit" merge) needs its mainline named; a squash or rebase merge is
  # a single-parent commit and needs nothing. 3 words = the commit and two parents.
  if ((parents > 2)); then pick+=(-m 1); fi
  if ! g "${pick[@]}" "$sha" >/dev/null 2>&1; then
    g cherry-pick --abort >/dev/null 2>&1 || true
    g checkout -q --detach "$ref"
    g branch -q -D "$target" || true
    echo "status=conflict"
    return 0
  fi
  echo "status=ready"
  echo "branch=$target"
  echo "lts_branch=$branch"
}

self_test() {
  local fails=0 tmp
  tmp="$(mktemp -d)"
  gc() { git -C "$tmp/r" -c user.name=t -c user.email=t@t -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
  git init -q "$tmp/r"
  gc checkout -q -b main
  printf 'a\n' >"$tmp/r/f"
  printf 'x\n' >"$tmp/r/g"
  gc add -A
  gc commit -q -m base
  gc branch release-1.2-lts
  # a fix on main touching a line the LTS branch has not changed -> applies cleanly
  printf 'a\nfix\n' >"$tmp/r/f"
  gc commit -q -am "fix: clean"
  local clean
  clean="$(gc rev-parse HEAD)"
  # a fix whose context the LTS branch changed -> conflicts
  printf 'x\ny\n' >"$tmp/r/g"
  gc commit -q -am "fix: conflicting"
  local conflicting
  conflicting="$(gc rev-parse HEAD)"
  gc checkout -q release-1.2-lts
  printf 'z\n' >"$tmp/r/g"
  gc commit -q -am "lts diverges on g"
  gc checkout -q main
  local lts_on="$tmp/lts-on" lts_none="$tmp/lts-none" lts_missing="$tmp/lts-missing"
  printf 'current-lts: 1.2\n' >"$lts_on"
  printf 'current-lts: none\n' >"$lts_none"
  printf 'current-lts: 1.9\n' >"$lts_missing"

  prep() { (repo_root="$tmp/r"; policy_file="$1"; shift; cmd_prepare "$@" 2>&1); }
  want() { # NAME WANT_SUBSTRING GOT
    if [[ "$3" == *"$2"* ]]; then printf 'ok   %s\n' "$1"; else
      printf 'FAIL %s (wanted %q in %q)\n' "$1" "$2" "$3" >&2
      fails=$((fails + 1))
    fi
  }
  wantnot() {
    if [[ "$3" != *"$2"* ]]; then printf 'ok   %s\n' "$1"; else
      printf 'FAIL %s (did not want %q in %q)\n' "$1" "$2" "$3" >&2
      fails=$((fails + 1))
    fi
  }

  local out
  out="$(prep "$lts_none" 7 "$clean" security || true)"
  want "no LTS designated -> no-lts" "status=no-lts" "$out"
  out="$(prep "$lts_on" 7 "$clean" $'bug
enhancement' || true)"
  want "an unlabelled fix is ineligible" "status=ineligible" "$out"
  out="$(prep "$lts_on" 7 "$clean" "" || true)"
  want "no labels at all is ineligible" "status=ineligible" "$out"
  out="$(prep "$lts_on" 7 "$clean" $'security-review
not-critical' || true)"
  want "a near-miss label is ineligible" "status=ineligible" "$out"
  out="$(prep "$lts_missing" 7 "$clean" security || true)"
  want "a designated line with no branch is an error" "does not exist" "$out"
  out="$(prep "$lts_on" x "$clean" security || true)"
  want "a malformed PR number is refused" "not a pull request number" "$out"
  out="$(prep "$lts_on" 7 "$clean" security || true)"
  want "a clean cherry-pick is ready" "status=ready" "$out"
  want "...on the PR-named branch" "branch=backport/pr-7-to-release-1.2-lts" "$out"
  want "...with the cherry-pick provenance trailer" "(cherry picked from commit $clean)" "$(gc log -1 --format=%B backport/pr-7-to-release-1.2-lts)"
  want "...based on the LTS branch, not main" "lts diverges on g" "$(gc log --format=%s backport/pr-7-to-release-1.2-lts)"
  wantnot "...and carrying none of main's other commits" "fix: conflicting" "$(gc log --format=%s backport/pr-7-to-release-1.2-lts)"
  out="$(prep "$lts_on" 7 "$clean" "bug,security" || true)"
  want "a label that merely contains a comma is one label, not two" "status=ineligible" "$out"
  printf "f
other
" >"$tmp/files-wrong"
  printf "f
" >"$tmp/files-right"
  out="$(prep "$lts_on" 12 "$clean" security "$tmp/files-wrong" || true)"
  want "a commit carrying only part of the PR is partial" "status=partial" "$out"
  out="$(prep "$lts_on" 13 "$clean" $'bug
security' "$tmp/files-right" || true)"
  want "labels are one per line, and a complete file list passes" "status=ready" "$out"
  out="$(prep "$lts_on" 8 "$conflicting" critical || true)"
  want "a conflicting cherry-pick is reported, not forced" "status=conflict" "$out"
  wantnot "...and no stray branch listing" "backport/pr-8" "$(gc branch --list 'backport/pr-8*')"
  gc branch -q -f release-1.2-lts "$clean" 2>/dev/null || true
  out="$(prep "$lts_on" 9 "$clean" security || true)"
  want "a commit already on the LTS branch is already" "status=already" "$out"
  # exists: pretend origin already has the backport branch
  gc checkout -q main
  gc update-ref refs/remotes/origin/backport/pr-10-to-release-1.2-lts "$conflicting"
  gc update-ref refs/remotes/origin/release-1.2-lts release-1.2-lts
  out="$(prep "$lts_on" 10 "$conflicting" security || true)"
  want "an existing backport branch is exists" "status=exists" "$out"

  rm -rf "$tmp"
  if ((fails > 0)); then
    printf 'lts-backport self-test: %d failed\n' "$fails" >&2
    return 1
  fi
  echo "lts-backport self-test: all passed"
}

main() {
  case "${1:-}" in
  prepare) shift; cmd_prepare "$@" ;;
  --self-test) self_test ;;
  *) die "usage: lts-backport.sh prepare PR SHA LABELS | --self-test" ;;
  esac
}

main "$@"
