#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Runs govulncheck over one workspace module and fails on any REACHABLE
# vulnerability that is not covered by a reviewed, justified entry in
# hack/govulncheck-allow.txt.
#
# ---------------------------------------------------------------------------
# WHY THIS EXISTS AT ALL, RATHER THAN JUST CALLING govulncheck.
#
# Plain `govulncheck ./...` is the right gate everywhere except one place, and
# that place is real: backend/cli (dcctl) imports helm.sh/helm/v3/pkg/action,
# whose package-init chain reaches golang.org/x/crypto/openpgp — GO-2026-5932,
# "unmaintained, unsafe by design", **Fixed in: N/A**. helm v3.21.3 is the latest
# v3 there is, so there is no version to upgrade to; the only real fix is helm
# dropping the dependency or us dropping helm.
#
# That leaves three options and two of them are bad:
#
#   - Fail the build. backend/cli goes permanently red over something no one can
#     fix. A gate that can never be green gets ignored exactly as fast as one
#     that can never fail, and it takes the other 21 modules' signal down with it.
#   - `continue-on-error`. The gate-that-cannot-fail, which this repo has already
#     shipped three times. Absolutely not.
#   - An explicit, per-module, justified exception that EXPIRES LOUDLY. This.
#
# The third option is only better than the second if the exception cannot outlive
# its reason, which is why a stale allowlist entry — one naming a vulnerability
# that is no longer reachable — is itself a FAILURE here. An exception you can
# forget about is just `continue-on-error` with extra steps.
#
# ---------------------------------------------------------------------------
# 🔑 THE TRAP THIS SCRIPT IS SHAPED AROUND. With `-format json`, govulncheck
# exits **0 whether or not it found anything** — the findings are in the JSON,
# not the status. It exits 1 only when the scan itself failed. So "the JSON
# contained no findings" and "govulncheck never ran" are the same observation
# unless the exit code is checked separately, and a script that only parsed
# findings would report a confident, cheerful, meaningless success on a module
# that failed to load. Measured: findings -> 0, no findings -> 0, not-a-module
# -> 1. Both guards below exist because of that.
#
# Usage:
#   hack/govulncheck.sh backend/cli        # scan one module
#   hack/govulncheck.sh --self-test        # prove the check can fail
set -euo pipefail

ALLOWLIST="${ALLOWLIST:-hack/govulncheck-allow.txt}"
GOVULNCHECK_VERSION="v1.6.0"

# Reachable = govulncheck resolved the vulnerability down to a specific FUNCTION
# in the call graph. The same OSV is also emitted at package level and at module
# level with no function; those are the "found in a module you require but do not
# call" rows, which are informational and must not fail a build on their own.
reachable_osvs() {
  jq -r 'select(.finding) | select(.finding.trace[0].function != null) | .finding.osv' "$1" | sort -u
}

# Entries are: <module-dir> <OSV-ID> # <justification>
# The justification is mandatory — an exception whose reason was never written
# down cannot be reviewed later, and will not be removed later either.
allowed_osvs() {
  local module="$1" file="$2"
  [ -f "$file" ] || return 0
  awk -v m="$module" '
    /^[[:space:]]*(#|$)/ { next }
    {
      if ($1 != m) next
      if ($0 !~ /#/) {
        printf("::error::%s: allowlist entry for %s has no justification comment\n", m, $2) > "/dev/stderr"
        exit 1
      }
      print $2
    }' "$file" | sort -u
}

evaluate() {
  local module="$1" json="$2" allowfile="$3" rc=0
  local found allowed

  found="$(reachable_osvs "$json")"
  allowed="$(allowed_osvs "$module" "$allowfile")"

  # 1. Anything reachable and not allowlisted fails.
  local unexpected
  unexpected="$(comm -23 <(echo "$found") <(echo "$allowed") | grep -v '^$' || true)"
  if [ -n "$unexpected" ]; then
    echo "::error::$module: reachable vulnerability with no reviewed exception:"
    while IFS= read -r id; do printf '    %s\n' "$id"; done <<<"$unexpected"
    echo "    Fix the dependency if a fixed version exists. If it genuinely cannot"
    echo "    be fixed, add a justified line to $allowfile:"
    echo "        $module <OSV-ID> # why this cannot be fixed, and what would fix it"
    rc=1
  fi

  # 2. THE COUNTERWEIGHT, and the reason an allowlist is acceptable here at all.
  #    An entry naming something no longer reachable is a standing permission
  #    nobody is watching — the dependency may have been fixed, dropped, or the
  #    module rewritten. Fail so it gets deleted while the reason is still known.
  local stale
  stale="$(comm -13 <(echo "$found") <(echo "$allowed") | grep -v '^$' || true)"
  if [ -n "$stale" ]; then
    echo "::error::$module: STALE allowlist entry — no longer reachable, so the"
    echo "exception is now silently permitting nothing and hiding nothing:"
    while IFS= read -r id; do printf '    %s\n' "$id"; done <<<"$stale"
    echo "    Delete the matching line(s) from $allowfile."
    rc=1
  fi

  if [ "$rc" -eq 0 ]; then
    local n
    n="$(echo "$allowed" | grep -c . || true)"
    echo "OK: $module — no unreviewed reachable vulnerabilities ($n reviewed exception(s))."
  fi
  return "$rc"
}

# ---------------------------------------------------------------------------
# install_govulncheck: fetch the pinned tool, with retries.
# ---------------------------------------------------------------------------
# `go install <tool>@<version>` is a network fetch through proxy.golang.org plus a
# verification against sum.golang.org, and this line runs ONCE PER WORKSPACE MODULE
# per CI run — one matrix entry each, on that many separate runners, each with a cold
# tool cache. (No count is written here on purpose: the matrix is derived from
# go.work, so the number moves whenever a module joins, and the figure this comment
# used to name had already drifted below the real one.) So a Go-infrastructure blip
# that would be a rounding error against a single fetch gets that many chances to
# land, and when it lands it reds `main` over nothing anybody changed. On 2026-08-27
# sum.golang.org returned INTERNAL_ERROR intermittently and did exactly that three
# times in one day, each
# time costing a person the work of establishing that the tree was fine.
#
# The gate next door already retries for the same reason: the operand image build in
# ci.yml wraps itself in this same three-attempt loop with this same backoff.
#
# 🔴 WHAT THIS DOES NOT BUY. The ko install in ci.yml states the limit better than
# this comment could — "TWO INDEPENDENT SOURCES, BECAUSE RETRYING ONE HOST IS NOT
# RESILIENCE", written after a release host went from truncating bodies to hard 503s
# inside twenty minutes. There is no second Go checksum database to fall back to, so
# a genuine sumdb outage still fails here, and should. Retrying covers the case that
# actually happened — some of the twenty-one fetches succeeding while others did not
# — and deliberately does not pretend to cover more.
#
# Exhausting the attempts is a HARD failure, never a shrug. An unscanned module must
# not report "no vulnerabilities found", which is the same rule GUARD 1 in scan()
# enforces for the scan itself.
install_govulncheck() {
  local attempt
  for attempt in 1 2 3; do
    if go_install_govulncheck; then
      return 0
    fi
    [ "$attempt" -eq 3 ] && break
    echo "::warning::govulncheck install attempt ${attempt}/3 failed (usually a transient proxy.golang.org or sum.golang.org error); retrying..." >&2
    retry_backoff "$attempt"
  done
  echo "::error::could not install govulncheck@${GOVULNCHECK_VERSION} after 3 attempts."
  echo "    Nothing was scanned, so this is NOT 'no vulnerabilities found'."
  echo "    If proxy.golang.org or sum.golang.org is having an incident, re-run once"
  echo "    it clears — do not reach for GOFLAGS=-mod=mod or GONOSUMDB to get past it."
  return 1
}

# These two exist ONLY so the self-test can substitute them. install_govulncheck's
# whole behaviour is "how many times, and does a later success count", and neither
# is observable while the loop body is a real network fetch and a real 45s of sleeps.
go_install_govulncheck() {
  go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"
}

retry_backoff() {
  sleep $(($1 * 15))
}

scan() {
  local module="$1"
  local repo_root allowfile json
  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  allowfile="$repo_root/$ALLOWLIST"
  json="$(mktemp)"
  # shellcheck disable=SC2064 # expand $json now, not at trap time
  trap "rm -f '$json'" RETURN

  command -v jq >/dev/null || { echo "::error::jq is required"; return 1; }

  install_govulncheck || return 1
  local bin
  bin="$(go env GOPATH)/bin/govulncheck"

  # GUARD 1: the scan must actually have run. In JSON mode a non-zero status is
  # the ONLY signal that it did not — the findings list would be empty either way.
  if ! ( cd "$repo_root/$module" && "$bin" -format json ./... ) > "$json"; then
    echo "::error::govulncheck failed to run in $module. This is NOT 'no"
    echo "vulnerabilities found' — nothing was scanned. Refusing to pass."
    sed 's/^/    /' "$json" | head -20
    return 1
  fi

  # GUARD 2: a zero-length or record-less report means the same thing, and would
  # otherwise sail through as "no findings".
  if ! jq -e -s 'any(.[]; .config)' "$json" >/dev/null 2>&1; then
    echo "::error::govulncheck produced no report for $module — the output has no"
    echo "config record, so it did not complete. Refusing to pass."
    return 1
  fi

  evaluate "$module" "$json" "$allowfile"
}

if [ "${1:-}" = "--self-test" ]; then
  echo "==> Self-test: the evaluator must fail on an unreviewed finding, on a stale"
  echo "    exception, and must NOT fail on a reviewed one"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  # A reachable finding (trace[0].function set) and an unreachable one for the
  # same OSV — the informational rows govulncheck also emits.
  cat > "$tmp/reachable.json" <<'EOF'
{"config":{"scanner_name":"govulncheck"}}
{"finding":{"osv":"GO-2026-5932","trace":[{"module":"golang.org/x/crypto","package":"golang.org/x/crypto/openpgp","function":"init"}]}}
{"finding":{"osv":"GO-2026-5932","trace":[{"module":"golang.org/x/crypto"}]}}
EOF
  # Only the module-level row: a require-but-do-not-call vulnerability.
  cat > "$tmp/unreachable.json" <<'EOF'
{"config":{"scanner_name":"govulncheck"}}
{"finding":{"osv":"GO-2026-9999","trace":[{"module":"golang.org/x/crypto"}]}}
EOF

  : > "$tmp/empty-allow.txt"
  printf 'backend/cli GO-2026-5932 # helm v3 init chain; no fixed version exists\n' > "$tmp/allow.txt"

  # 1. Reachable, not allowlisted -> must fail.
  if evaluate backend/cli "$tmp/reachable.json" "$tmp/empty-allow.txt" >/dev/null 2>&1; then
    echo "  FAIL: an unreviewed reachable vulnerability was accepted" >&2; exit 1
  fi
  echo "  ok: an unreviewed reachable vulnerability fails"

  # 2. Reachable, allowlisted -> must pass. (Counterweight for case 1.)
  if ! evaluate backend/cli "$tmp/reachable.json" "$tmp/allow.txt" >/dev/null 2>&1; then
    echo "  FAIL: a reviewed exception was rejected — the gate cries wolf" >&2; exit 1
  fi
  echo "  ok: a reviewed exception passes"

  # 3. The allowlist must be scoped BY MODULE. The same entry must not silence
  #    the same vulnerability in a different module.
  if evaluate backend/core "$tmp/reachable.json" "$tmp/allow.txt" >/dev/null 2>&1; then
    echo "  FAIL: an exception written for backend/cli also silenced backend/core" >&2; exit 1
  fi
  echo "  ok: an exception is scoped to its own module"

  # 4. Allowlisted but no longer reachable -> STALE, must fail.
  if evaluate backend/cli "$tmp/unreachable.json" "$tmp/allow.txt" >/dev/null 2>&1; then
    echo "  FAIL: a stale exception was accepted — exceptions would outlive their reason" >&2; exit 1
  fi
  echo "  ok: a stale exception fails"

  # 5. An unreachable-only finding with no exceptions at all -> must PASS. This
  #    is the everyday case (core requires x/crypto but does not call openpgp);
  #    if it failed, every module in the tree would be red.
  if ! evaluate backend/core "$tmp/unreachable.json" "$tmp/empty-allow.txt" >/dev/null 2>&1; then
    echo "  FAIL: a require-but-do-not-call vulnerability failed the gate" >&2; exit 1
  fi
  echo "  ok: an unreachable vulnerability does not fail the gate"

  # 6. An allowlist entry with no justification is rejected.
  printf 'backend/cli GO-2026-5932\n' > "$tmp/nojust.txt"
  if evaluate backend/cli "$tmp/reachable.json" "$tmp/nojust.txt" >/dev/null 2>&1; then
    echo "  FAIL: an exception with no written justification was accepted" >&2; exit 1
  fi
  echo "  ok: an exception with no justification is rejected"

  echo "==> Self-test: the install retry must retry, must give up, and must stop"
  echo "    retrying once it succeeds"
  retry_backoff() { :; }  # the loop's timing is not what is under test
  attempts="$tmp/attempts"

  # 7. An installer that never succeeds must FAIL, and must have tried three times.
  #    Asserting the count as well as the result is the point: a loop that returned
  #    non-zero after ONE attempt would pass a result-only check while being exactly
  #    the no-retry behaviour this change exists to remove.
  : > "$attempts"
  go_install_govulncheck() { echo x >> "$attempts"; return 1; }
  if install_govulncheck >/dev/null 2>&1; then
    echo "  FAIL: an install that never succeeded was accepted" >&2; exit 1
  fi
  n="$(wc -l < "$attempts")"
  [ "$n" -eq 3 ] || { echo "  FAIL: gave up after $n attempt(s), expected 3" >&2; exit 1; }
  echo "  ok: an install that never succeeds fails, after 3 attempts"

  # 8. THE COUNTERWEIGHT, and the only case that shows the retry is load-bearing:
  #    two failures then a success must PASS. Without this, a loop that ignored its
  #    own success and always failed would satisfy case 7 perfectly.
  : > "$attempts"
  go_install_govulncheck() {
    echo x >> "$attempts"
    [ "$(wc -l < "$attempts")" -ge 3 ]
  }
  if ! install_govulncheck >/dev/null 2>&1; then
    echo "  FAIL: an install that succeeded on the third attempt was rejected" >&2; exit 1
  fi
  echo "  ok: an install that succeeds on a later attempt passes"

  # 9. A first-attempt success must not retry. A loop that always ran three times
  #    would pass 7 and 8 and triple every green CI run's install cost.
  : > "$attempts"
  go_install_govulncheck() { echo x >> "$attempts"; return 0; }
  install_govulncheck >/dev/null 2>&1 || { echo "  FAIL: a working install was rejected" >&2; exit 1; }
  n="$(wc -l < "$attempts")"
  [ "$n" -eq 1 ] || { echo "  FAIL: a first-attempt success still ran $n attempts" >&2; exit 1; }
  echo "  ok: a first-attempt success installs once"

  echo "==> Self-test passed"
  exit 0
fi

[ $# -eq 1 ] || { echo "usage: $0 <module-dir> | --self-test" >&2; exit 2; }
scan "$1"
