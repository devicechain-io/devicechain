#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Fails unless GitHub DETECTS this repository as Apache-2.0.
#
# ---------------------------------------------------------------------------
# WHY THIS IS WORTH A CI STEP AT ALL.
#
# The SPDX headers are already gated (the addlicense step next door), the LICENSE
# file is in the tree, and NOTICE says the same thing. None of that is what the
# ecosystem reads. GitHub runs its own detector over the repository and publishes one
# value from it, and that value — not our headers — is what drives:
#
#   - the `license:apache-2.0` search filter, i.e. whether this project appears at all
#     when somebody filters by license;
#   - the licence shown on the repo page and in the sidebar;
#   - what SBOM and dependency tooling records for anything that depends on us;
#   - the `licenses` field of the repository API, which downstream automation trusts.
#
# When the detector cannot match the file it reports NOASSERTION, and every one of
# those goes blank or wrong — silently, with the LICENSE file sitting right there
# looking correct. That is not hypothetical: it is the live state of a comparable
# project in this space whose hand-edited LICENSE.txt matches nothing, so it is
# invisible to license search and shows up in SBOMs as unlicensed. Nothing in their
# repository is red about it. Nothing would be red about it here either.
#
# ---------------------------------------------------------------------------
# 🔴 HOW THIS IS ASSERTED, AND THE TRADEOFF THAT WAS ACCEPTED.
#
# Two designs were available:
#
#   A. Run a detector locally over the working tree. Tests the PR's own tree, needs no
#      network — but it is a DIFFERENT DETECTOR from GitHub's, so it answers a
#      different question. It would go green while GitHub reported NOASSERTION, which
#      is precisely and only the failure this exists to catch. A check that cannot see
#      the failure it was built for is worse than no check.
#
#   B. Ask GitHub what it detected. Reports the actual value we care about, with no
#      detector skew and no version drift. The stated cost is that it describes the
#      PUSHED state rather than the tree under review.
#
# B was chosen, and that stated cost turns out to be avoidable: the endpoint takes a
# `ref`, and on a pull_request event GITHUB_SHA is the ephemeral merge commit, which
# IS resolvable in the base repository (measured against a live open PR). So the gate
# asks about the merged tree the PR would produce, not about main.
#
# Two things were measured rather than assumed, because the whole value of B rests on
# them:
#
#   1. `?ref=<sha>` really does select that ref's tree — the returned blob sha follows
#      the ref.
#   2. Detection is RECOMPUTED per ref rather than served from a repository-level
#      cache. Asked at this repo's first commit, which predates the LICENSE file, the
#      endpoint returns 404 rather than the current answer. A cache would have handed
#      back Apache-2.0 and this gate would have been decorative.
#
# The residual cost of B, stated plainly: it needs the network and a token, so an
# api.github.com outage reds this step over nothing in the tree. It is one request.
#
# ---------------------------------------------------------------------------
# 🔑 THE TRAP THIS SCRIPT IS SHAPED AROUND, and it is the same one govulncheck.sh
# documents: an ABSENCE reads exactly like an ANSWER. `curl | jq -r .license.spdx_id`
# prints an empty line for a 404, for a 500, for an HTML error page and for a
# rate-limit body — and a `[ "$x" = "Apache-2.0" ]` on that fails with a message about
# licensing when the real news is that nothing was asked. So the HTTP status is
# captured separately from the body and checked first, and every non-200 says which of
# the two happened.
#
#   hack/check-detected-license.sh              # check GITHUB_SHA (or the default branch)
#   hack/check-detected-license.sh <ref>        # check one ref
#   hack/check-detected-license.sh --self-test  # prove the check can fail
set -euo pipefail

# The one value this whole file exists to assert.
WANT_SPDX="Apache-2.0"

REPO="${GITHUB_REPOSITORY:-devicechain-io/devicechain}"

# evaluate: decide pass/fail from ONE fetch, given its HTTP status and body file.
#
# Split out from the fetch on purpose — it is the half with the judgement in it, and
# the half the self-test can exercise offline. A checker whose only test is "it was
# green against the real repo today" has never been shown to fail.
evaluate() {
  local status="$1" body="$2" ref="$3"

  case "$status" in
    200) ;;
    404)
      echo "::error::GitHub detects NO license for $REPO at ${ref:-the default branch}."
      echo "    The API returns 404 when its detector cannot match the LICENSE file — or"
      echo "    when the file is absent. Either way the repository reads as unlicensed to"
      echo "    the license search filter and to every SBOM tool downstream."
      echo "    Fix by restoring an unmodified Apache-2.0 LICENSE at the repository root."
      echo "    (A 404 can also mean the repo or ref does not exist, or the token cannot"
      echo "    see it — check that before assuming the license moved.)"
      return 1
      ;;
    *)
      echo "::error::could not ask GitHub what license it detects (HTTP $status)."
      echo "    NOTHING WAS CHECKED. This is not 'no license problem found'."
      sed 's/^/    /' "$body" | head -10
      return 1
      ;;
  esac

  # A 200 whose body is not JSON is the rate-limit / proxy-HTML case. jq -e makes it
  # a failure instead of an empty string that compares unequal for the wrong reason.
  local spdx
  if ! spdx="$(jq -er '.license.spdx_id // empty' "$body" 2>/dev/null)"; then
    echo "::error::GitHub answered 200 for $REPO but with no detected license in the body."
    echo "    NOTHING WAS CHECKED. This is not 'no license problem found'."
    sed 's/^/    /' "$body" | head -10
    return 1
  fi

  if [ "$spdx" = "NOASSERTION" ]; then
    echo "::error::GitHub detects the license of $REPO as NOASSERTION at ${ref:-the default branch}."
    echo "    That is the detector saying it found a license file it could not identify —"
    echo "    the exact state that makes a project invisible to \`license:\` search and"
    echo "    unlicensed in every SBOM built from it."
    echo "    Fix by restoring an unmodified Apache-2.0 LICENSE at the repository root."
    return 1
  fi

  if [ "$spdx" != "$WANT_SPDX" ]; then
    echo "::error::GitHub detects the license of $REPO as '$spdx', not '$WANT_SPDX'."
    echo "    If this project's license genuinely changed, that is a decision with its own"
    echo "    paperwork (LICENSE, NOTICE, every SPDX header, the published packages) and"
    echo "    this line is updated last, deliberately — not first, to make CI green."
    return 1
  fi

  echo "OK: GitHub detects $REPO as $spdx at ${ref:-the default branch} ($(jq -r '.path' "$body"))."
  return 0
}

# fetch: one request, status and body kept apart. `-f` is deliberately NOT used — it
# would discard the body on an error status, which is the body worth printing.
fetch() {
  local ref="$1" body="$2" url="https://api.github.com/repos/$REPO/license"
  # --retry covers transient 5xx and connection failures and does NOT retry a 404, so
  # it cannot paper over the finding this gate is for. It is two extra attempts on one
  # request, not retry machinery.
  local -a args=(-sS -o "$body" -w '%{http_code}'
    --retry 2 --retry-delay 3 --retry-connrefused
    -H "Accept: application/vnd.github+json"
    -H "X-GitHub-Api-Version: 2022-11-28")
  # GITHUB_TOKEN is what the workflow passes; GH_TOKEN is what a maintainer running
  # this by hand is likely to already have exported. Unauthenticated works too, at the
  # shared rate limit, which is why neither is required.
  local token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  [ -n "$ref" ] && url="$url?ref=$ref"
  curl "${args[@]}" "$url"
}

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the checker must fail on every way this can go wrong, and pass"
  echo "    on the one way it can go right"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  printf '{"license":{"spdx_id":"Apache-2.0"},"path":"LICENSE"}\n' > "$tmp/ok.json"
  printf '{"license":{"spdx_id":"NOASSERTION"},"path":"LICENSE"}\n' > "$tmp/noassertion.json"
  printf '{"license":{"spdx_id":"MIT"},"path":"LICENSE"}\n' > "$tmp/mit.json"
  printf '{"license":null,"path":"LICENSE"}\n' > "$tmp/null.json"
  printf '{"path":"LICENSE"}\n' > "$tmp/nolicense.json"
  printf '<html><body>rate limited</body></html>\n' > "$tmp/html.json"
  printf '{"message":"Not Found"}\n' > "$tmp/404.json"
  printf '{"message":"Server Error"}\n' > "$tmp/500.json"

  fail=0

  # 1. THE COUNTERWEIGHT, first rather than last. Without it every case below is also
  #    satisfied by a checker that returns 1 unconditionally.
  if evaluate 200 "$tmp/ok.json" "abc123" >/dev/null 2>&1; then
    echo "  ok: a correctly detected Apache-2.0 passes"
  else
    echo "  FAIL: the real, healthy state was rejected — this gate would be red forever" >&2
    fail=1
  fi

  # 2. The failure this whole file exists for.
  if evaluate 200 "$tmp/noassertion.json" "abc123" >/dev/null 2>&1; then
    echo "  FAIL: NOASSERTION was accepted — the one state this gate is for goes unreported" >&2
    fail=1
  else
    echo "  ok: NOASSERTION fails"
  fi

  # 3. A different, perfectly valid license. Detection working is not the assertion;
  #    detecting THIS license is.
  if evaluate 200 "$tmp/mit.json" "abc123" >/dev/null 2>&1; then
    echo "  FAIL: a different SPDX id was accepted — the gate checks only that SOMETHING was detected" >&2
    fail=1
  else
    echo "  ok: a different SPDX id fails"
  fi

  # 4 & 5. A 200 that carries no detection. `jq -r .license.spdx_id` yields "null" or
  #        "" here, and a naive string compare fails with a message blaming the
  #        license when nothing was actually reported.
  for case in null nolicense; do
    if evaluate 200 "$tmp/$case.json" "abc123" >/dev/null 2>&1; then
      echo "  FAIL: a 200 with no detected license ($case) was accepted" >&2
      fail=1
    else
      echo "  ok: a 200 with no detected license ($case) fails"
    fi
  done

  # 6. A 200 that is not JSON at all — the rate-limit and proxy-error shape.
  if evaluate 200 "$tmp/html.json" "abc123" >/dev/null 2>&1; then
    echo "  FAIL: a non-JSON 200 body was accepted" >&2
    fail=1
  else
    echo "  ok: a non-JSON 200 body fails"
  fi

  # 7 & 8. Nothing was asked. Must fail, and must NOT be reported as a license problem.
  if evaluate 404 "$tmp/404.json" "abc123" >/dev/null 2>&1; then
    echo "  FAIL: a 404 was accepted — an undetectable license would read as fine" >&2
    fail=1
  else
    echo "  ok: a 404 fails"
  fi
  if evaluate 500 "$tmp/500.json" "abc123" >/dev/null 2>&1; then
    echo "  FAIL: a 500 was accepted — an unasked question would read as an answer" >&2
    fail=1
  else
    echo "  ok: a 500 fails"
  fi

  # 9. The two unasked-question cases must be DISTINGUISHABLE from the license cases in
  #    what they print, or the person reading the red goes and inspects LICENSE for an
  #    outage. This is the part a pass/fail-only self-test cannot see.
  out="$(evaluate 500 "$tmp/500.json" "abc123" 2>&1 || true)"
  case "$out" in
    *"NOTHING WAS CHECKED"*) echo "  ok: an outage says nothing was checked, not that the license is wrong" ;;
    *) echo "  FAIL: a transport failure is reported as a licensing failure" >&2; fail=1 ;;
  esac

  [ "$fail" -eq 0 ] || { echo "==> Self-test FAILED"; exit 1; }
  echo "==> Self-test passed"
  exit 0
fi

command -v jq >/dev/null || { echo "::error::jq is required"; exit 1; }
command -v curl >/dev/null || { echo "::error::curl is required"; exit 1; }

# GITHUB_SHA on a pull_request event is the ephemeral merge commit, which is
# resolvable in the base repository — so in CI this asks about the tree the PR would
# merge, not about whatever is on the default branch. Run by hand with no argument and
# no GITHUB_SHA, it falls back to the default branch, because a local commit that has
# not been pushed is not a ref GitHub can be asked about.
REF="${1:-${GITHUB_SHA:-}}"

BODY="$(mktemp)"
trap 'rm -f "$BODY"' EXIT

# curl failing OUTRIGHT (DNS, TLS, connection refused after retries) prints to stderr
# and returns non-zero with no HTTP status to hand to evaluate. Under `set -e` that
# would end the script silently with curl's exit code, which reads in the CI log as a
# license failure. Name it instead: this is the "nothing was asked" case too.
if ! STATUS="$(fetch "$REF" "$BODY")"; then
  echo "::error::the request to api.github.com did not complete, so NOTHING WAS CHECKED."
  echo "    This is not 'no license problem found'. If GitHub is having an incident,"
  echo "    re-run once it clears."
  exit 1
fi

evaluate "$STATUS" "$BODY" "$REF"
