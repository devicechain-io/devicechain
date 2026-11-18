#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Sourced by every surface of the API-stability gate (hack/check-graphql-compat.sh,
# check-cli-compat.sh, check-schema-compat.sh, check-csharp-api.sh, check-npm-api.sh and
# check-deprecation-window.sh): the ways they have to agree on, so they live once.
#
#   resolve_baseline [tag]   prints the baseline tag; exits 1 when there is none
#   default_baseline [repo]  the baseline HEAD of repo is compared with when no tag is given
#   extract_baseline tag dir extracts the release's workspace into dir
#   build_apicompat dir      builds backend/tools/docsgql/cmd/apicompat to dir/apicompat
#
#   hack/api-baseline.sh --self-test   the baseline rule against planted histories (git only)
#
# THE BASELINE is the newest release a consumer of HEAD's line could be running:
#
#   - HEAD carries a release tag (vX.Y.Z or vX.Y.Z-<prerelease>: a release or candidate build).
#     The base is hack/changelog-base.sh's: the highest stable tag semver-LOWER than that X.Y.Z,
#     reachable or not, so a release is compared with exactly the release its changelog starts at.
#   - HEAD is untagged: a version in development, above every release on its line. The base is
#     the highest stable tag, reachable or not, leaving out the releases that are not behind
#     HEAD's line: one cut AFTER HEAD on its own branch (it descends from HEAD), and one cut on a
#     branch that left HEAD's history BEFORE the newest release HEAD contains (R). On
#     release-1.5, v1.6.0's branch point is the 1.5 freeze, which v1.5.x contains, so a fix
#     there is compared with v1.5.x and not with a minor that added things.
#
# 🔴 It used to be the nearest stable tag REACHABLE from HEAD, and that skips every minor: vX.Y.0
# is tagged on release-X.Y, which main never contains, so main was compared with v1.5.0 while
# v1.6.0 was out, and a break of anything v1.6.0 shipped was invisible. Release candidates are
# never a base: nobody runs one.
#
# 🔴 NO TAG IS A FAILURE, NEVER A SKIP. On a clone without tags nothing is found, and a
# compatibility check that compared against nothing would pass every break. The CI job checks
# out with fetch-depth: 0 for exactly this. A FIRST release (no stable tag below it) fails too:
# there is nothing to compare with, and a pass would read as "compatible".

API_BASELINE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
API_STABLE_TAG_RE='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
API_RELEASE_TAG_RE='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'

# default_baseline [repo]: the baseline for the HEAD of repo (default: the current directory).
default_baseline() {
  local repo="${1:-.}" tags here r t fp cands="" best
  git -C "$repo" rev-parse --verify --quiet HEAD >/dev/null || { echo "::error::$repo has no HEAD" >&2; return 1; }
  # Its own statement, so a failing `git tag` stops here instead of reading as no tags.
  tags="$(git -C "$repo" tag --list 'v*')" || { echo "::error::could not list the tags of $repo" >&2; return 1; }
  tags="$(printf '%s\n' "$tags" | tr -d '\r' | grep -E "$API_STABLE_TAG_RE" || true)"
  if [ -z "$tags" ]; then
    echo "::error::no stable vX.Y.Z release tag in $repo. A clone without tags is the usual cause (check out with fetch-depth: 0); a check with no baseline compares against nothing and must not pass." >&2
    return 1
  fi
  here="$(git -C "$repo" tag --points-at HEAD --list 'v*' | tr -d '\r' | grep -E "$API_RELEASE_TAG_RE" | sort -V | tail -n1 || true)"
  if [ -n "$here" ]; then
    best="$(cd "$repo" && "$API_BASELINE_DIR/changelog-base.sh" "$here")" || return 1
    [ -n "$best" ] || { echo "::error::$here is the first stable release; there is no earlier one to compare with." >&2; return 1; }
    echo "$best"
    return 0
  fi
  r="$(git -C "$repo" describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' --exclude '*-*' HEAD 2>/dev/null || true)"
  if [ -z "$r" ]; then
    echo "::error::no stable vX.Y.Z release tag is reachable from HEAD. A shallow clone is the usual cause (check out with fetch-depth: 0); a check with no baseline compares against nothing and must not pass." >&2
    return 1
  fi
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    if git -C "$repo" merge-base --is-ancestor "$t" HEAD; then
      cands+="$t"$'\n'
      continue
    fi
    git -C "$repo" merge-base --is-ancestor HEAD "$t" && continue # cut after HEAD, on its line
    fp="$(git -C "$repo" merge-base "$t" HEAD)" || { echo "::error::no merge base of $t and HEAD" >&2; return 1; }
    git -C "$repo" merge-base --is-ancestor "$fp" "$r" || cands+="$t"$'\n'
  done <<<"$tags"
  best="$(printf '%s' "$cands" | sort -V | tail -n1)"
  [ -n "$best" ] || { echo "::error::no baseline found for HEAD" >&2; return 1; }
  echo "$best"
}

resolve_baseline() {
  local baseline="${1:-}"
  if [ -z "$baseline" ]; then
    baseline="$(default_baseline .)" || return 1
  fi
  git rev-parse --verify --quiet "${baseline}^{commit}" >/dev/null || {
    echo "::error::baseline '$baseline' is not a tag or commit in this repository." >&2
    return 1
  }
  echo "$baseline"
}

# The baseline rule against planted histories, each with its required answer, through
# resolve_baseline (the function every surface calls). Needs only git.
baseline_self_test() {
  local tmp got
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'; trap - RETURN" RETURN
  g() { git -C "$tmp/r" -c user.name=t -c user.email=t@t -c commit.gpgsign=false -c tag.gpgsign=false "$@" >/dev/null; }
  git init -q -b main "$tmp/r"
  # main: c1 (v1.0.0) - F14 - F15 - F16 - head
  # release-1.4 from F14 tags v1.4.0; release-1.5 from F15 tags v1.5.0 and v1.5.1 and has a fix
  # after it; release-1.6 from F16 tags v1.6.0-rc.1 and v1.6.0.
  g commit -q --allow-empty -m c1; g tag v1.0.0
  g commit -q --allow-empty -m F14; g branch release-1.4
  g commit -q --allow-empty -m F15; g branch release-1.5
  g commit -q --allow-empty -m F16; g branch release-1.6
  g commit -q --allow-empty -m head
  g checkout -q release-1.4; g commit -q --allow-empty -m r14; g tag v1.4.0
  g checkout -q release-1.5; g commit -q --allow-empty -m r15; g tag v1.5.0
  g commit -q --allow-empty -m r151; g tag v1.5.1
  g commit -q --allow-empty -m fix15
  g checkout -q release-1.6; g commit -q --allow-empty -m rc; g tag v1.6.0-rc.1
  g commit -q --allow-empty -m pre16
  g commit -q --allow-empty -m r16; g tag v1.6.0

  in_repo() { (cd "$1" && resolve_baseline "${@:2}"); }
  expect_baseline() { # <what> <ref> <want>
    g checkout -q "$2"
    got="$(in_repo "$tmp/r" 2>&1)" || { echo "self-test: baseline for $1 failed: $got" >&2; return 1; }
    [ "$got" = "$3" ] || { echo "self-test: baseline for $1 is $got, want $3" >&2; return 1; }
  }
  # main is compared with v1.6.0, though only v1.0.0 is reachable from it.
  expect_baseline "main" main v1.6.0 || return 1
  # A fix on release-1.5 is compared with its own line, not with the later v1.6.0.
  expect_baseline "a fix on release-1.5" release-1.5 v1.5.1 || return 1
  # A release build: changelog-base.sh's answer, never a candidate, never itself.
  expect_baseline "the v1.6.0 tag" v1.6.0 v1.5.1 || return 1
  expect_baseline "the v1.5.1 tag" v1.5.1 v1.5.0 || return 1
  # A candidate build: the release below the one it is a candidate for.
  expect_baseline "the v1.6.0-rc.1 tag" v1.6.0-rc.1 v1.5.1 || return 1
  # An untagged commit on release-1.6 before v1.6.0: v1.6.0 is in its future, not its baseline.
  expect_baseline "release-1.6 before its first release" v1.6.0~1 v1.5.1 || return 1
  # A tag given explicitly is taken as given.
  got="$(in_repo "$tmp/r" v1.4.0 2>&1)" && [ "$got" = v1.4.0 ] || { echo "self-test: the explicit baseline v1.4.0 gave '$got'" >&2; return 1; }

  # The first stable release has nothing to compare with: a failure, never an empty baseline.
  git init -q -b main "$tmp/first"
  git -C "$tmp/first" -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -q --allow-empty -m c1
  git -C "$tmp/first" tag v0.1.0-rc.1
  git -C "$tmp/first" tag v0.1.0
  if got="$(in_repo "$tmp/first" 2>&1)"; then echo "self-test: the first release gave baseline '$got'" >&2; return 1; fi
  # No stable tag at all is a failure, never an empty baseline.
  git init -q -b main "$tmp/r2"
  git -C "$tmp/r2" -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -q --allow-empty -m c1
  if got="$(in_repo "$tmp/r2" 2>&1)"; then echo "self-test: a repository with no tag gave baseline '$got'" >&2; return 1; fi
  echo "api-baseline self-test: main -> v1.6.0 (unreachable), a release-1.5 fix -> v1.5.1, a release or candidate tag -> the stable tag below it, a commit before a release on its branch -> the one before, an explicit tag as given, the first release and no tag fail"
}

# The whole Go workspace, because the command tree and the topic layout are read by
# BUILDING the release's own code: go.work, the modules, and the deploy module cli embeds.
extract_baseline() {
  local baseline="$1" dir="$2" paths=(go.work backend deploy)
  git cat-file -e "${baseline}:go.work.sum" 2>/dev/null && paths+=(go.work.sum)
  # A release cut after the reference device joined the workspace lists it in go.work, and the
  # workspace does not load without it. Older releases have no such directory.
  git cat-file -e "${baseline}:examples" 2>/dev/null && paths+=(examples)
  git archive --format=tar "$baseline" "${paths[@]}" | tar -x -C "$dir"
  [ -f "$dir/go.work" ] && [ -d "$dir/backend" ] || {
    echo "::error::$baseline has no Go workspace to compare against." >&2
    return 1
  }
}

# Built, not `go run`: `go run` exits 1 for any non-zero exit of the program, which would
# fold "could not run" (2) into "found a break" (1), and the messages depend on telling
# those apart.
build_apicompat() {
  (cd "$ROOT/backend/tools/docsgql" && go build -o "$1/apicompat" ./cmd/apicompat)
}

# A copy of the working tree's workspace, for a self-test to plant a difference in.
copy_workspace() {
  local dir="$1" p
  for p in go.work go.work.sum backend deploy examples; do
    [ -e "$ROOT/$p" ] && cp -R "$ROOT/$p" "$dir/"
  done
  return 0
}

# check_rc <what> <want> <got> <output>: fail the self-test with the output when the exit
# status is not the one this step is meant to produce.
check_rc() {
  [ "$3" -eq "$2" ] || { echo "$4"; echo "self-test: $1 exited $3, want $2" >&2; return 1; }
}

# check_has <what> <needle> <output>
check_has() {
  grep -qF -- "$2" <<<"$3" || { echo "$3"; echo "self-test: $1: the output does not contain: $2" >&2; return 1; }
}

# verdict <what> <baseline> <rc>: how a wrapper reads the command's exit status. One
# function for every surface, and the self-tests drive it, so the mapping that turns a
# break red is itself proven to: a wrapper that read exit 1 as success would otherwise
# pass every break while each self-test (which calls the command directly) stayed green.
verdict() {
  case "$3" in
    0) return 0 ;;
    1)
      echo "::error::the $1 no longer matches $2, or a declaration is invalid. The FAIL lines above name each element and the line that would declare it." >&2
      return 1
      ;;
    *)
      echo "::error::the $1 compatibility check could not run (exit $3); nothing was compared. See the error above." >&2
      return 1
      ;;
  esac
}

verdict_self_test() {
  local out
  verdict probe v0.0.0 0 2>/dev/null || { echo "self-test: verdict fails a clean run" >&2; return 1; }
  if out="$(verdict probe v0.0.0 1 2>&1)"; then echo "self-test: verdict passes a break" >&2; return 1; fi
  check_has "verdict of a break" "no longer matches v0.0.0" "$out" || return 1
  if out="$(verdict probe v0.0.0 2 2>&1)"; then echo "self-test: verdict passes a check that could not run" >&2; return 1; fi
  check_has "verdict of a check that could not run" "could not run (exit 2)" "$out" || return 1
}

# summarize <title> <log>: copy what a reader acts on into the job summary, so a
# report-only change (which never turns anything red) is seen without opening the log.
# A no-op outside GitHub Actions.
summarize() {
  [ -n "${GITHUB_STEP_SUMMARY:-}" ] || return 0
  {
    echo "### $1"
    echo
    echo '```text'
    grep -E '^(FAIL|REPORT|dangerous|declared|expired|apicompat:|report-only:)' "$2" || echo "(no output: see the step log)"
    echo '```'
    echo
  } >>"$GITHUB_STEP_SUMMARY"
}

# Run directly rather than sourced: the baseline rule's self-test.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  set -euo pipefail
  case "${1:-}" in
    --self-test) baseline_self_test ;;
    *)
      echo "usage: $0 --self-test (otherwise this file is sourced)" >&2
      exit 2
      ;;
  esac
fi
