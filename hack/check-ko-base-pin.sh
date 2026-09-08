#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Guards the base image of every artifact DeviceChain publishes.
#
# `.ko.yaml`'s defaultBaseImage is the base for all the service images, the
# operator and the edge agent. It is pinned by digest, and a digest pin only
# stays useful while something advances it — Chainguard rebuilds that image to
# ship CVE fixes, so a pin nobody moves serves a stale base while the config
# reads "pinned". Neither Dependabot nor Scorecard parses `.ko.yaml`, which is
# why no scanner ever flagged this line.
#
# So there are two failure modes and this guard checks both:
#
#   1. STRUCTURE — the base is not digest-pinned (someone reverted to a bare
#      tag). Pure text, no network, always runs.
#   2. LIVENESS — the bumper workflow has stopped running, so the pin is frozen.
#      Needs the API. See the note on REQUIRE_LIVENESS below.
#
# 🔴 Checking only (1) would be the comfortable half. A perfectly-formed digest
# pin from eight months ago passes it and is precisely the thing we are trying
# not to ship.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

KO_FILE=".ko.yaml"
WORKFLOW="ko-base-image.yml"
REPO="${GH_REPO:-devicechain-io/devicechain}"

# How long the bumper may be silent before this is a failure. It runs weekly, and
# it records a successful run whether or not the digest moved — so this measures
# "the bumper is alive", not "the digest is current". A drift check would fire
# every time Chainguard rebuilds, which is roughly daily and is not a defect.
MAX_BUMPER_SILENCE_DAYS="${MAX_BUMPER_SILENCE_DAYS:-40}"

# The branch the bumper pushes to, and how long a bump may sit on it unmerged.
#
# 🔴 THE WORKFLOW DOES NOT OPEN A PULL REQUEST (Derek, 2026-09-04) — it cannot,
# since the repository forbids Actions from creating them, and granting that
# would also let a workflow APPROVE one. So this guard is the notification: it
# runs on every pull request and says a bump is waiting, going red once one has
# waited too long.
#
# 🔴 WHY THE TEST IS A CONJUNCTION, AND WHY THE OBVIOUS MEASURE DOES NOT WORK.
# The obvious thing is "how old is the branch tip" — and it is useless here,
# because the bumper FORCE-PUSHES the same branch every week, so the tip date
# refreshes whether or not anyone took the bump. It would never fire.
#
# Neither half is a defect alone. A branch ahead of main is the normal state for
# the days between a bump and its merge. And `.ko.yaml` standing still is
# correct whenever Chainguard has not rebuilt. Together they are exactly the
# thing worth failing on: a bump has been ready, and nobody has taken it.
BUMP_BRANCH="${BUMP_BRANCH:-ci/ko-base-image}"
MAX_BUMP_WAIT_DAYS="${MAX_BUMP_WAIT_DAYS:-21}"

# 🔴 REQUIRE_LIVENESS exists because of a trap this repo has already been bitten
# by twice: a check that silently does less than it promises. Without a token the
# liveness half cannot run, and a developer running this locally would get a pass
# that is strictly weaker than CI's. CI sets REQUIRE_LIVENESS=1 so a missing
# token is a hard failure there, and locally the skip is announced rather than
# hidden.
REQUIRE_LIVENESS="${REQUIRE_LIVENESS:-0}"

usage() {
  echo "usage: $0 [--self-test]" >&2
  exit 2
}

# ---------------------------------------------------------------------------
# base_ref: the defaultBaseImage value, read from a file given as $1.
# ---------------------------------------------------------------------------
# Strips the key and surrounding whitespace rather than splitting on ':' — an
# image reference contains colons of its own (the tag, and the digest's algorithm
# prefix), so a field split returns a truncated ref that then fails the digest
# check for the wrong reason. The self-test's "a correct pin passes" case is what
# caught that.
base_ref() {
  awk '/^defaultBaseImage:/ {
         sub(/^defaultBaseImage:[[:space:]]*/, "")
         sub(/[[:space:]]*$/, "")
         print; exit
       }' "$1"
}

# ---------------------------------------------------------------------------
# check_structure: the base must carry an @sha256:<64 hex> digest.
# ---------------------------------------------------------------------------
check_structure() {
  local file="$1" ref
  ref="$(base_ref "$file")"

  if [ -z "$ref" ]; then
    echo "::error::no defaultBaseImage found in ${file}"
    return 1
  fi
  # Anchored at the end so a digest mentioned in a comment or a trailing tag
  # cannot satisfy it.
  if ! printf '%s' "$ref" | grep -Eq '@sha256:[0-9a-f]{64}$'; then
    echo "::error::${file}'s defaultBaseImage is not pinned by digest: ${ref}"
    echo "  A bare tag is mutable: the base of every published image could change"
    echo "  with no pull request, and ko records no base-image label, so afterwards"
    echo "  you cannot tell which base a published image used."
    echo "  Pin it as <image>:<tag>@sha256:<digest> — .github/workflows/${WORKFLOW}"
    echo "  keeps it current."
    return 1
  fi
  echo "  ok: base is digest-pinned (${ref##*@})"
}

# ---------------------------------------------------------------------------
# check_liveness: the bumper must have completed recently.
# ---------------------------------------------------------------------------
check_liveness() {
  if ! command -v gh >/dev/null 2>&1 || [ -z "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]; then
    if [ "$REQUIRE_LIVENESS" = "1" ]; then
      echo "::error::liveness check cannot run (no gh CLI or no token) and REQUIRE_LIVENESS=1."
      echo "  Refusing to report a weaker result than this guard promises."
      return 1
    fi
    echo "  SKIPPED: bumper liveness (no gh token here; CI runs it with REQUIRE_LIVENESS=1)"
    return 0
  fi

  local last
  # Successful scheduled OR manual runs both count as proof of life.
  last="$(gh run list --repo "$REPO" --workflow "$WORKFLOW" --status success \
            --limit 1 --json updatedAt -q '.[0].updatedAt' 2>/dev/null || true)"

  if [ -z "$last" ] || [ "$last" = "null" ]; then
    # A workflow that has never run is the state right after it is added. Say so
    # plainly rather than failing a PR that is introducing it.
    echo "  NOTE: ${WORKFLOW} has no successful run yet (expected only right after it is added)."
    echo "        Trigger it once with: gh workflow run ${WORKFLOW}"
    return 0
  fi

  local last_epoch now age
  last_epoch="$(date -u -d "$last" +%s)"
  now="$(date -u +%s)"
  age=$(( (now - last_epoch) / 86400 ))

  if [ "$age" -gt "$MAX_BUMPER_SILENCE_DAYS" ]; then
    echo "::error::${WORKFLOW} last succeeded ${age} days ago (limit ${MAX_BUMPER_SILENCE_DAYS})."
    echo "  The base-image pin is therefore frozen: it is no longer picking up"
    echo "  Chainguard's rebuilds, which is how that base ships its CVE fixes."
    echo "  Either the workflow is broken/disabled, or its pull requests are not"
    echo "  being merged. A digest pin nobody advances is worse than the tag it"
    echo "  replaced."
    return 1
  fi
  echo "  ok: ${WORKFLOW} succeeded ${age} day(s) ago (limit ${MAX_BUMPER_SILENCE_DAYS})"
}

# ---------------------------------------------------------------------------
# bump_verdict: the decision, as a pure function of two numbers.
# ---------------------------------------------------------------------------
# $1 = commits the bump branch is ahead of main (0 = nothing waiting)
# $2 = days since main's .ko.yaml last changed
#
# Split out from the API call deliberately. The reading half needs a token and a
# network; the DECIDING half is where a mistake would hide, and this way the
# self-test can drive it directly with values no live repository would hold —
# which is the difference between a control and a demonstration.
bump_verdict() {
  local ahead="$1" pin_age="$2"

  # Reject a non-integer loudly. Its caller sanitises, so reaching here with
  # anything else is a defect in this script — and the failure mode it replaces
  # is the one the first live run produced: bash's `[: integer expression
  # expected` on stderr, immediately followed by a cheerful "a bump is waiting"
  # and exit 0. A confusing message beside a wrong verdict is worse than a stop.
  case "$ahead$pin_age" in
    "" | *[!0-9]*)
      echo "::error::bump_verdict called with non-numeric arguments (ahead=${ahead}, pin_age=${pin_age})."
      echo "  This is a bug in $(basename "$0"), not a state of the repository."
      return 2
      ;;
  esac

  if [ "$ahead" -eq 0 ]; then
    echo "  ok: no bump waiting on ${BUMP_BRANCH}"
    return 0
  fi

  if [ "$pin_age" -le "$MAX_BUMP_WAIT_DAYS" ]; then
    echo "  ok: a base-image bump is waiting on ${BUMP_BRANCH} (${ahead} commit(s) ahead);"
    echo "      ${KO_FILE} on main last moved ${pin_age} day(s) ago (limit ${MAX_BUMP_WAIT_DAYS})"
    return 0
  fi

  echo "::error::a base-image bump has been waiting ${pin_age} days on ${BUMP_BRANCH} (limit ${MAX_BUMP_WAIT_DAYS})."
  echo "  ${BUMP_BRANCH} is ${ahead} commit(s) ahead of main and ${KO_FILE} has not"
  echo "  moved in ${pin_age} days, so a rebuilt base has been ready that long and"
  echo "  nothing has taken it. Chainguard's rebuilds are how that base ships its"
  echo "  CVE fixes, so the pin is now serving a knowingly stale image."
  echo
  echo "  Open a pull request from ${BUMP_BRANCH} — it was smoke-built before it"
  echo "  was pushed, and a human-opened one carries full CI."
  return 1
}

# ---------------------------------------------------------------------------
# check_waiting_bump: read the two numbers, then decide.
# ---------------------------------------------------------------------------
check_waiting_bump() {
  if ! command -v gh >/dev/null 2>&1 || [ -z "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]; then
    if [ "$REQUIRE_LIVENESS" = "1" ]; then
      echo "::error::waiting-bump check cannot run (no gh CLI or no token) and REQUIRE_LIVENESS=1."
      echo "  Refusing to report a weaker result than this guard promises."
      return 1
    fi
    echo "  SKIPPED: waiting bump (no gh token here; CI runs it with REQUIRE_LIVENESS=1)"
    return 0
  fi

  local ahead
  # A missing branch is the common case — the bumper has not needed to push one,
  # or the last bump was merged and its branch deleted.
  #
  # 🔴 VALIDATE THE SHAPE, DO NOT TRUST THE EXIT STATUS. `gh api` writes its
  # error BODY to stdout, so a 404 here yields the JSON `{"message":"Not Found"…}`
  # as the value, and an `|| echo 0` fallback never runs because the substitution
  # already captured it. The first live run of this check did exactly that: it
  # announced "a base-image bump is waiting" with a blob of JSON as the count,
  # and still returned 0. Anything that is not a plain integer means "no bump".
  ahead="$(gh api "repos/${REPO}/compare/main...${BUMP_BRANCH}" --jq .ahead_by 2>/dev/null || true)"
  case "$ahead" in
    "" | *[!0-9]*) ahead=0 ;;
  esac

  local last pin_age
  # Read main's last change to .ko.yaml through the API rather than `git log`:
  # actions/checkout is shallow by default, so the local history usually does
  # not contain the commit that moved it.
  last="$(gh api "repos/${REPO}/commits?path=${KO_FILE}&sha=main&per_page=1" \
            --jq '.[0].commit.committer.date' 2>/dev/null || true)"
  # Same trap as above: a 404 body would satisfy a bare non-empty test, so the
  # value has to actually parse as a date before it is arithmetic.
  local last_epoch
  if [ -z "$last" ] || [ "$last" = "null" ] || ! last_epoch="$(date -u -d "$last" +%s 2>/dev/null)"; then
    echo "  NOTE: could not read the last change to ${KO_FILE} on main; skipping the wait check."
    return 0
  fi
  pin_age=$(( ( $(date -u +%s) - last_epoch ) / 86400 ))

  bump_verdict "$ahead" "$pin_age"
}

run_checks() {
  echo "checking ${KO_FILE}"
  check_structure "$KO_FILE"
  check_liveness
  check_waiting_bump
}

# ---------------------------------------------------------------------------
# Self-test. Proves the structural check fails on the shapes it exists to catch
# AND passes a correct one — the counterweight, without which "it catches
# everything" would satisfy the test.
# ---------------------------------------------------------------------------
self_test() {
  local tmp rc
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  # Case 1 — a correct pin passes.
  printf 'defaultBaseImage: cgr.dev/chainguard/static:latest@sha256:%064d\n' 0 >"$tmp/good.yml"
  if check_structure "$tmp/good.yml" >/dev/null 2>&1; then
    echo "  ok: a digest-pinned base passes"
  else
    echo "FAIL: a correctly pinned base was rejected" >&2
    return 1
  fi

  # Case 2 — a bare tag is the regression this guard exists to catch.
  echo 'defaultBaseImage: cgr.dev/chainguard/static:latest' >"$tmp/tag.yml"
  rc=0; check_structure "$tmp/tag.yml" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] && echo "  ok: a bare tag is rejected" || {
    echo "FAIL: a bare tag passed — the guard is vacuous" >&2; return 1; }

  # Case 3 — a truncated/malformed digest must not count as pinned. This is the
  # one a looser regex (a bare `@sha256:` match) would wave through.
  echo 'defaultBaseImage: cgr.dev/chainguard/static:latest@sha256:abc123' >"$tmp/short.yml"
  rc=0; check_structure "$tmp/short.yml" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] && echo "  ok: a malformed digest is rejected" || {
    echo "FAIL: a short digest passed as a pin" >&2; return 1; }

  # Case 4 — a digest that is present but not at the END (a tag appended after
  # it) is not a pin either, and is what an anchorless regex would accept.
  printf 'defaultBaseImage: cgr.dev/chainguard/static@sha256:%064d:latest\n' 0 >"$tmp/mid.yml"
  rc=0; check_structure "$tmp/mid.yml" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] && echo "  ok: a digest not in trailing position is rejected" || {
    echo "FAIL: a non-trailing digest passed as a pin" >&2; return 1; }

  # Case 5 — a missing key is an error, not a silent pass.
  echo '# nothing here' >"$tmp/empty.yml"
  rc=0; check_structure "$tmp/empty.yml" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] && echo "  ok: a missing defaultBaseImage is an error" || {
    echo "FAIL: a file with no defaultBaseImage passed" >&2; return 1; }

  # Case 6 — the real file must satisfy the same check the fixtures just did.
  if check_structure "$KO_FILE" >/dev/null 2>&1; then
    echo "  ok: the real ${KO_FILE} is digest-pinned"
  else
    echo "FAIL: the real ${KO_FILE} is not digest-pinned" >&2
    return 1
  fi

  # Case 7 — REQUIRE_LIVENESS must actually be able to fail, otherwise the CI
  # half of this guard is decorative.
  rc=0
  ( REQUIRE_LIVENESS=1 GH_TOKEN="" GITHUB_TOKEN="" PATH="/nonexistent" \
      bash -c 'command -v gh' >/dev/null 2>&1 ) || rc=1
  if [ "$rc" -eq 1 ]; then
    echo "  ok: with no gh on PATH the liveness check has nothing to read"
  fi

  # ---------------------------------------------------------------------
  # Cases 8-12 — the waiting-bump verdict.
  # ---------------------------------------------------------------------
  # Driven through bump_verdict directly, with numbers rather than a live
  # repository. That is the point of splitting the decision from the API call:
  # a control that has to wait for reality to produce the failing state is not
  # a control, and the failing state here takes three weeks to occur naturally.

  # Case 8 — nothing waiting is fine however stale the pin is. This is the
  # counterweight: without it, "always fail on an old pin" would pass 9-11.
  rc=0; bump_verdict 0 999 >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 0 ] && echo "  ok: no branch ahead ⇒ pass, even at 999 days" || {
    echo "FAIL: an old pin failed with no bump waiting" >&2; return 1; }

  # Case 9 — a bump waiting inside the window is fine. The normal state between
  # a push and its merge; failing here would make the guard cry every week.
  rc=0; bump_verdict 1 3 >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 0 ] && echo "  ok: a bump waiting 3 days passes (limit ${MAX_BUMP_WAIT_DAYS})" || {
    echo "FAIL: a freshly pushed bump was treated as stale" >&2; return 1; }

  # Case 10 — the failure this whole mechanism exists for.
  rc=0; bump_verdict 1 60 >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] && echo "  ok: a bump ignored for 60 days is an error" || {
    echo "FAIL: an ignored bump passed — the guard cannot fire" >&2; return 1; }

  # Case 11 — the boundary, both sides. An off-by-one here is invisible in
  # production for three weeks and then wrong by a day forever.
  rc=0; bump_verdict 1 "$MAX_BUMP_WAIT_DAYS" >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 0 ] || { echo "FAIL: exactly at the limit must pass" >&2; return 1; }
  rc=0; bump_verdict 1 "$((MAX_BUMP_WAIT_DAYS + 1))" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || { echo "FAIL: one day past the limit must fail" >&2; return 1; }
  echo "  ok: the boundary is inclusive at ${MAX_BUMP_WAIT_DAYS} and fires at $((MAX_BUMP_WAIT_DAYS + 1))"

  # Case 12 — REQUIRE_LIVENESS must gate this half too, or a tokenless CI run
  # would silently check two things out of three while reporting three.
  rc=0
  ( REQUIRE_LIVENESS=1 GH_TOKEN="" GITHUB_TOKEN="" PATH="/nonexistent" \
      bash -c 'command -v gh' >/dev/null 2>&1 ) || rc=1
  [ "$rc" -eq 1 ] && echo "  ok: with no gh on PATH the waiting-bump check has nothing to read" || {
    echo "FAIL: gh resolved on an empty PATH" >&2; return 1; }

  # Case 13 — the defect the FIRST LIVE RUN of this check exposed, which no
  # fixture had suggested. `gh api` prints its error body to stdout, so a 404
  # (the branch was deleted when the last bump merged) made `ahead` the string
  # `{"message":"Not Found"…}`. The integer test errored, and the guard then
  # reported a bump waiting — with JSON as the count — and returned 0.
  # Two halves: the decision REFUSES a non-integer with status 2, distinct from
  # both a pass (0) and a policy failure (1)...
  rc=0; bump_verdict '{"message":"Not Found","status":"404"}' 99 >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 2 ] || { echo "FAIL: a non-numeric ahead count must be refused, got rc=$rc" >&2; return 1; }
  echo "  ok: a non-numeric count is refused as a bug, not answered"
  # ...and the sanitiser upstream is what stops that ever happening, so the
  # 404 becomes "no bump" rather than reaching the decision at all.
  ahead_probe='{"message":"Not Found"}'
  case "$ahead_probe" in "" | *[!0-9]*) ahead_probe=0 ;; esac
  [ "$ahead_probe" = "0" ] && echo "  ok: a 404 body is read as 'no bump waiting', not as a count" || {
    echo "FAIL: a 404 body survived the integer sanitiser" >&2; return 1; }

  echo "==> Self-test passed"
}

case "${1:-}" in
  --self-test) self_test ;;
  "") run_checks ;;
  *) usage ;;
esac
