#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Fails if a Go build added hashes to go.work.sum — which means the committed file
# was incomplete.
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
# touched on purpose, in a module graph nobody looked at, and it looks exactly like
# noise from some unrelated `go` invocation — which is how one was left out of a
# commit in #1135 and then failed every module's CI job. A line a build appended is
# never unrelated: it is the build saying the committed file was not enough.
#
# Two modes, one question:
#
#   hack/check-go-work-sum.sh [--module NAME]
#       CI: after a module's build, compare go.work.sum with what is committed. The
#       checkout is clean, so ANY difference was added by the build. NAME only
#       labels the message.
#
#   hack/check-go-work-sum.sh --since SNAPSHOT
#       A local sweep: compare go.work.sum with a copy taken before the build. A
#       working tree is often not clean, so this compares against the file as it
#       was, not as it is committed: an edit already there neither fails the check
#       nor hides a gap the build reveals.
#
#   hack/check-go-work-sum.sh --self-test   # prove the check can fail
#
# Exit 0: nothing was added. Exit 1: the build added hashes (they are printed; commit
# them). Exit 2: the check could not run — a missing snapshot is a refusal, never a
# pass.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# added_since BASELINE CURRENT — print the lines CURRENT has that BASELINE lacks.
added_since() {
  diff "$1" "$2" | sed -n 's/^> //p' || true
}

# report LABEL ADDED — the failure message, with the lines to commit.
report() {
  local label="$1" added="$2"
  echo "::error::Building ${label} added hashes to go.work.sum, so the committed file is incomplete. The build has already written them; commit go.work.sum."
  echo "Added:"
  printf '%s\n' "$added" | sed 's/^/  /'
}

# check_committed DIR LABEL — CI mode, against the committed file.
check_committed() {
  local dir="$1" label="$2"
  if git -C "$dir" diff --quiet -- go.work.sum; then
    echo "go.work.sum already covers ${label}"
    return 0
  fi
  local tmp
  tmp="$(mktemp)"
  git -C "$dir" show HEAD:go.work.sum >"$tmp"
  report "$label" "$(added_since "$tmp" "$dir/go.work.sum")"
  rm -f "$tmp"
  return 1
}

# check_since DIR SNAPSHOT — sweep mode, against a copy taken before the build.
check_since() {
  local dir="$1" snapshot="$2"
  if [ ! -f "$snapshot" ]; then
    echo "check-go-work-sum: snapshot '$snapshot' does not exist, so there is nothing to compare against; refusing rather than reporting clean" >&2
    return 2
  fi
  if cmp -s "$snapshot" "$dir/go.work.sum"; then
    echo "go.work.sum: the build added no hashes"
    return 0
  fi
  local added
  added="$(added_since "$snapshot" "$dir/go.work.sum")"
  if [ -z "$added" ]; then
    # Lines went away rather than arrived. A build never removes hashes, so something
    # else rewrote the file during the sweep; that is worth a look but not this
    # check's failure.
    echo "go.work.sum: changed during the sweep but gained nothing; not a missing-hash failure" >&2
    return 0
  fi
  report "this workspace" "$added"
  return 1
}

self_test() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN
  local repo="$tmp/repo" fails=0
  git init -q "$repo"
  printf 'example.com/a v1.0.0/go.mod h1:aaa=\nexample.com/b v1.0.0/go.mod h1:bbb=\n' >"$repo/go.work.sum"
  git -C "$repo" add go.work.sum
  git -C "$repo" -c user.name=t -c user.email=t@t commit -qm init

  expect() {
    local want="$1" name="$2"
    shift 2
    local got=0 out
    out="$("$@" 2>&1)" || got=$?
    if [ "$got" -ne "$want" ]; then
      echo "self-test FAILED: $name — exit $got, want $want" >&2
      printf '%s\n' "$out" >&2
      fails=$((fails + 1))
    fi
    LAST_OUT="$out"
  }

  # CI mode.
  expect 0 "a clean checkout passes" check_committed "$repo" m
  echo 'example.com/c v1.0.0/go.mod h1:ccc=' >>"$repo/go.work.sum"
  expect 1 "an appended hash fails" check_committed "$repo" m
  case "$LAST_OUT" in
    *"example.com/c v1.0.0/go.mod h1:ccc="*) ;;
    *) echo "self-test FAILED: the failure does not print the hash to commit" >&2; fails=$((fails + 1)) ;;
  esac
  git -C "$repo" checkout -q -- go.work.sum

  # Sweep mode.
  cp "$repo/go.work.sum" "$tmp/snap"
  expect 0 "nothing added since the snapshot passes" check_since "$repo" "$tmp/snap"
  echo 'example.com/c v1.0.0/go.mod h1:ccc=' >>"$repo/go.work.sum"
  expect 1 "a hash added since the snapshot fails" check_since "$repo" "$tmp/snap"

  # 🔑 The property the snapshot exists for. An edit that was ALREADY in the working
  # tree must neither fail the sweep nor hide a hash the build adds after it.
  git -C "$repo" checkout -q -- go.work.sum
  echo 'example.com/unrelated v9.0.0/go.mod h1:zzz=' >>"$repo/go.work.sum"
  cp "$repo/go.work.sum" "$tmp/snap"
  expect 0 "a pre-existing edit alone does not fail" check_since "$repo" "$tmp/snap"
  echo 'example.com/c v1.0.0/go.mod h1:ccc=' >>"$repo/go.work.sum"
  expect 1 "a pre-existing edit does not hide a new hash" check_since "$repo" "$tmp/snap"
  case "$LAST_OUT" in
    *"example.com/unrelated"*)
      echo "self-test FAILED: the pre-existing edit was reported as added by the build" >&2
      fails=$((fails + 1)) ;;
  esac

  expect 2 "a missing snapshot is a refusal, not a pass" check_since "$repo" "$tmp/absent"

  if [ "$fails" -ne 0 ]; then
    echo "check-go-work-sum self-test: $fails failure(s)" >&2
    return 1
  fi
  echo "check-go-work-sum self-test passed: an appended hash fails in both modes and is printed, a pre-existing edit neither fails nor hides one, and a missing snapshot refuses."
}

case "${1:-}" in
  --self-test)
    self_test
    ;;
  --since)
    [ $# -eq 2 ] || { echo "usage: $0 --since SNAPSHOT" >&2; exit 2; }
    check_since "$ROOT" "$2"
    ;;
  --module)
    [ $# -eq 2 ] || { echo "usage: $0 --module NAME" >&2; exit 2; }
    check_committed "$ROOT" "$2"
    ;;
  "")
    check_committed "$ROOT" "the workspace"
    ;;
  *)
    echo "usage: $0 [--module NAME | --since SNAPSHOT | --self-test]" >&2
    exit 2
    ;;
esac
