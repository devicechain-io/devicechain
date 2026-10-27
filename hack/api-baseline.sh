#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Sourced by hack/check-cli-compat.sh and hack/check-schema-compat.sh: the two ways every
# surface of the API-stability gate has to agree on, so they live once.
#
#   resolve_baseline [tag]   prints the baseline tag; exits 1 when there is none
#   extract_baseline tag dir extracts the release's workspace into dir
#   build_apicompat dir      builds backend/tools/docsgql/cmd/apicompat to dir/apicompat
#
# THE BASELINE is the nearest STABLE tag REACHABLE from HEAD, not the newest tag in the
# repository: a fix on a release branch must be compared with that branch's release, not
# with a later minor that added things, and a release candidate is not a release anyone
# runs. Same rule as hack/check-graphql-compat.sh.
#
# 🔴 NO TAG IS A FAILURE, NEVER A SKIP. On a shallow clone `git describe` finds nothing,
# and a compatibility check that compared against nothing would pass every break. The CI
# job checks out with fetch-depth: 0 for exactly this.

resolve_baseline() {
  local baseline="${1:-}"
  if [ -z "$baseline" ]; then
    # '[0-9]*' and the exclusion of any '-' keep release candidates and non-release tags out.
    baseline="$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' --exclude '*-*' HEAD 2>/dev/null || true)"
    if [ -z "$baseline" ]; then
      echo "::error::no stable vX.Y.Z release tag is reachable from HEAD. A shallow clone is the usual cause (check out with fetch-depth: 0); a check with no baseline compares against nothing and must not pass." >&2
      return 1
    fi
  fi
  git rev-parse --verify --quiet "${baseline}^{commit}" >/dev/null || {
    echo "::error::baseline '$baseline' is not a tag or commit in this repository." >&2
    return 1
  }
  echo "$baseline"
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
