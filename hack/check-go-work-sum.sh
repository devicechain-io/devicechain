#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Fails if go.work.sum holds anything that is not staged — which, after a build, means
# the build added hashes the committed file was missing.
#
# 🔴 WHY A BUILD CAN FIX THE FILE AND STILL BE A FAILURE. A `go` command in workspace
# mode does not refuse to run when go.work.sum is missing a hash it needs: it
# downloads, verifies and APPENDS it. So an incomplete go.work.sum builds, vets and
# tests green everywhere, and the gap shows up only as an uncommitted edit — until
# something refuses to run on a dirty tree. That something is the release: goreleaser
# aborts on "git is in a dirty state", and it did on v0.12.1-rc.1, after a Dependabot
# bump left the workspace sum nine hashes short.
#
# The edit is also easy to misread once it is there. It is a line in a file nobody
# touched on purpose, and it looks exactly like noise from some unrelated `go`
# invocation — which is how one was left out of a commit in #1135 and then failed the
# CI job of every module whose build needed it. A line a `go` command appended is
# never unrelated: it is the workspace saying the committed file was not enough.
#
# 🔑 ONE RULE FOR CI AND THE LOCAL SWEEP: go.work.sum must match what is staged. In CI
# the checkout is clean, so any difference was added by the build that just ran.
# Locally it catches the same hash whenever it was appended — by this sweep, by an
# earlier build, by an editor's language server. An earlier version of this check
# compared against a snapshot taken at the start of the sweep, so that an edit already
# in the tree would not fail it; that passed exactly the #1135 case, a line appended
# before anyone looked. There is no such thing as a harmless pre-existing line here:
# commit it, or `git add` it to say you mean to.
#
#   hack/check-go-work-sum.sh [--module NAME]   # NAME only labels the message
#   hack/check-go-work-sum.sh --self-test       # prove the check can fail
#
# Exit 0: go.work.sum matches the index. Exit 1: it does not (the lines to commit are
# printed). Exit 2: the check could not run — a missing file or a failed comparison is
# a refusal, never a pass.

set -euo pipefail

# 🔴 The comparison below sorts and compares text, and comm checks its input's order in
# its own locale: under en_US a real, mixed-case go.work.sum reads as unsorted and every
# run refuses. Fixing the locale for the whole script takes the question away rather
# than depending on each command remembering to.
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# check DIR LABEL
check() {
  local dir="$1" label="$2"
  if [ ! -f "$dir/go.work.sum" ]; then
    echo "check-go-work-sum: '$dir/go.work.sum' does not exist; refusing rather than reporting clean" >&2
    return 2
  fi

  # 🔴 DECIDED BY CONTENT, NOT BY `git diff`. git diff trusts the index's bits: a file
  # marked skip-worktree or assume-unchanged — the usual way to silence a file that
  # "keeps changing" — reads as unchanged whatever it holds, and so does one that is not
  # in the index at all. #1135's hash was exactly a real line mistaken for noise, so the
  # check reads the staged bytes and the working-tree bytes and compares those.
  # `:go.work.sum` is the index entry; a file that is not there (or is mid-conflict)
  # makes `git show` fail, which is a refusal.
  local staged added rc=0
  staged="$(mktemp)" || return 2
  if ! git -C "$dir" show :go.work.sum >"$staged"; then
    rm -f "$staged"
    echo "check-go-work-sum: could not read the staged go.work.sum (not in the index, or mid-conflict?); refusing rather than reporting clean" >&2
    return 2
  fi
  if cmp -s "$staged" "$dir/go.work.sum"; then
    rm -f "$staged"
    echo "go.work.sum matches what is staged (${label})"
    return 0
  fi

  # The lines the working tree has that the index does not. A set difference rather
  # than a positional diff, so a reordered or re-terminated file never blames a line
  # that was already there.
  if ! added="$(comm -13 <(sort -u "$staged") <(sort -u "$dir/go.work.sum"))"; then
    rc=2
  fi
  if [ "$rc" -ne 0 ]; then
    rm -f "$staged"
    echo "check-go-work-sum: could not compare go.work.sum with the index; refusing rather than reporting clean" >&2
    return 2
  fi

  if [ -n "$added" ]; then
    echo "::error::go.work.sum has hashes that are not committed (${label}). A go command added them because the committed file was missing them; commit go.work.sum."
    echo "Not committed:"
    printf '%s\n' "$added" | sed 's/^/  /'
  else
    # It differs but gained nothing: lines removed or reordered. No build does that,
    # so it is still not the committed file — show the change rather than guess at it.
    echo "::error::go.work.sum differs from what is committed (${label}) without gaining a hash; this is not what a build does. The change:"
    diff -u --label staged --label "working tree" "$staged" "$dir/go.work.sum" || true
  fi
  rm -f "$staged"
  return 1
}

self_test() (
  # A subshell with an EXIT trap, so an early abort cleans up as surely as a pass.
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  repo="$tmp/repo"
  fails=0
  mkdir -p "$repo/hack" "$repo/backend/core"
  git init -q "$repo"
  # Upper- and lower-case paths together, as a real go.work.sum has them: the order
  # C and en_US disagree on is what broke an earlier version of the comparison.
  printf '%s\n' \
    'github.com/Azure/sdk v1.0.0/go.mod h1:aaa=' \
    'github.com/a/b v1.0.0/go.mod h1:bbb=' >"$repo/go.work.sum"
  echo 'module x' >"$repo/backend/core/go.mod"
  # The script under test, run the way CI and the sweep run it: by path, as its own
  # process, from inside a module directory — so its root resolution, its argument
  # handling and its behaviour under `set -e` are all the real ones. Calling its
  # functions in-process would test none of those, and each can fail open.
  cp "${BASH_SOURCE[0]}" "$repo/hack/check-go-work-sum.sh"
  git -C "$repo" add -A
  git -C "$repo" -c user.name=t -c user.email=t@t -c commit.gpgsign=false \
    -c core.hooksPath=/dev/null commit -qm init

  expect() {
    local want="$1" name="$2" got=0
    shift 2
    LAST_OUT="$(cd "$repo/backend/core" && LC_ALL=en_US.UTF-8 ../../hack/check-go-work-sum.sh "$@" 2>&1)" || got=$?
    if [ "$got" -ne "$want" ]; then
      echo "self-test FAILED: $name — exit $got, want $want" >&2
      printf '%s\n' "$LAST_OUT" >&2
      fails=$((fails + 1))
    fi
  }
  mentions() {
    case "$LAST_OUT" in *"$1"*) return 0 ;; esac
    return 1
  }
  bad() { echo "self-test FAILED: $1" >&2; fails=$((fails + 1)); }
  reset() { git -C "$repo" checkout -q -- go.work.sum backend/core/go.mod; }
  # The locale case only bites where en_US.UTF-8 exists and sorts case-insensitively;
  # elsewhere bash falls back to C without failing, so say so rather than pass quietly.
  if [ "$(printf 'B\na\n' | LC_ALL=en_US.UTF-8 sort 2>/dev/null | head -1)" != "a" ]; then
    echo "check-go-work-sum self-test: WARNING — en_US.UTF-8 is not available here, so the mixed-case locale case is not exercised" >&2
  fi
  h1='github.com/Zeta/x v1.0.0/go.mod h1:ccc='
  h2='github.com/c/d v1.0.0/go.mod h1:ddd='

  expect 0 "a clean checkout passes" --module m
  expect 0 "a clean checkout passes with no arguments"

  # Two appended hashes, in both invocations: the list of what to commit is the point
  # of the message, and a report that printed only the first would leave the rest out.
  printf '%s\n%s\n' "$h1" "$h2" >>"$repo/go.work.sum"
  for args in "--module m" ""; do
    # shellcheck disable=SC2086 # deliberately split: "" must mean no arguments at all
    expect 1 "appended hashes fail (args: '${args}')" $args
    mentions "$h1" || bad "the failure does not print the first hash to commit (args: '${args}')"
    mentions "$h2" || bad "the failure does not print the second hash to commit (args: '${args}')"
    mentions "github.com/a/b" && bad "the failure lists a hash that was already committed (args: '${args}')"
  done

  # 🔑 Staging is how someone says "I mean this": a staged hash passes, and an
  # unstaged one still fails — whether it was appended before the sweep or during it.
  git -C "$repo" add go.work.sum
  expect 0 "a staged hash passes" --module m
  echo 'github.com/e/f v1.0.0/go.mod h1:eee=' >>"$repo/go.work.sum"
  expect 1 "an unstaged hash after a staged one fails" --module m
  mentions "$h1" && bad "a staged hash was reported as not committed"
  git -C "$repo" reset -q -- go.work.sum
  reset

  echo 'module y' >"$repo/backend/core/go.mod"
  expect 0 "another dirty file is not this check's business" --module m
  reset

  printf '%s\n' 'github.com/a/b v1.0.0/go.mod h1:bbb=' >"$repo/go.work.sum"
  expect 1 "a removal is still not the committed file" --module m
  mentions "github.com/Azure/sdk" || bad "a removal-only failure does not show the change"
  mentions "without gaining a hash" || bad "a removal-only failure is not reported as one"
  mentions "Not committed:" && bad "a removed line was reported as not committed"
  reset

  # The bits git diff trusts must not silence the check.
  for flag in --skip-worktree --assume-unchanged; do
    git -C "$repo" update-index "$flag" go.work.sum
    echo "$h1" >>"$repo/go.work.sum"
    expect 1 "an unstaged hash fails even with $flag set" --module m
    mentions "$h1" || bad "the hash hidden by $flag is not listed"
    git -C "$repo" update-index "--no-${flag#--}" go.work.sum
    reset
  done

  mv "$repo/go.work.sum" "$tmp/moved"
  expect 2 "a missing go.work.sum is a refusal, not a pass" --module m
  mv "$tmp/moved" "$repo/go.work.sum"

  # Refusals: each of these would otherwise have been a chance to report clean.
  git -C "$repo" rm -q --cached go.work.sum
  echo "$h1" >>"$repo/go.work.sum"
  expect 2 "a go.work.sum that is not in the index is a refusal" --module m
  git -C "$repo" add go.work.sum
  git -C "$repo" reset -q -- go.work.sum 2>/dev/null || true
  git -C "$repo" checkout -q HEAD -- go.work.sum
  mv "$repo/.git" "$tmp/git-moved"
  expect 2 "no repository is a refusal" --module m
  mv "$tmp/git-moved" "$repo/.git"
  expect 0 "premise: the repository is restored" --module m

  expect 2 "an unknown argument is refused" --bogus

  if [ "$fails" -ne 0 ]; then
    echo "check-go-work-sum self-test: $fails failure(s)" >&2
    exit 1
  fi
  echo "check-go-work-sum self-test passed: the script, run by path from a module directory, fails on unstaged hashes with and without --module and lists every one of them and nothing already committed; a staged hash passes and does not hide a later unstaged one; other dirty files are ignored; skip-worktree and assume-unchanged do not silence it; a removal fails and shows the change; a missing file, a file not in the index, no repository and an unknown argument are all refused."
)

case "${1:-}" in
  --self-test)
    self_test
    ;;
  --module)
    [ $# -eq 2 ] || { echo "usage: $0 --module NAME" >&2; exit 2; }
    check "$ROOT" "$2"
    ;;
  "")
    check "$ROOT" "workspace"
    ;;
  *)
    echo "usage: $0 [--module NAME | --self-test]" >&2
    exit 2
    ;;
esac
