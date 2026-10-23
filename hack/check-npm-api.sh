#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Does any published npm package still export everything it exported at the last release?
#
#   hack/check-npm-api.sh                  # drift check + compare with the nearest stable tag
#   hack/check-npm-api.sh v0.24.0          # baseline: the tag given
#   hack/check-npm-api.sh --update         # rewrite frontend/api/*.api.md from the built packages
#   hack/check-npm-api.sh --self-test      # prove both checks can fail, through this script
#
# Needs the packages BUILT (cd frontend && npm ci && npm run build:packages) and this tool's own
# dependencies (cd hack/api-report && npm ci).
#
# TWO CHECKS, because they answer different questions.
#
#   1. DRIFT. frontend/api/<package>.api.md is the public API of each of the five published
#      packages as the compiler prints it: every name an entry exports, every manifest `exports`
#      subpath, and (for the brand package, which exports stylesheets and tokens rather than code)
#      every custom property and token key. The reports are COMMITTED and the check regenerates
#      them, so an intended change to the API arrives as a diff of a text file a reviewer reads.
#      A change nobody meant fails here, naming the lines.
#   2. COMPATIBILITY. The same reports are rebuilt from the baseline release's sources and
#      compared: a removed export, a removed manifest subpath, a removed line of any declaration
#      (a member, an overload, a union arm, a signature) is a break and fails unless declared in
#      frontend/api/breaks.txt. A member added WITHOUT `?` is reported and never fails: whether
#      that breaks anyone depends on whether the type is read or constructed, which a declaration
#      does not say.
#
# This complements hack/widget-package-proof.sh and `npm run verify:packages`, which prove the
# packed artifact RESOLVES and LOADS; neither notices that an export disappeared.
#
# THE BASELINE is the nearest STABLE tag REACHABLE from HEAD (hack/api-baseline.sh), and no tag is
# a failure, never a skip. Declarations (frontend/api/breaks.txt) expire when the baseline moves
# and fail when stale, like every other surface.
#
# 🔴 THE BASELINE'S DECLARATIONS ARE REBUILT FROM ITS SOURCES, not read from a committed file: the
# first baseline predates these reports, and a frozen baseline report is the one place a hand edit
# could hide a removal. Building a release's declarations is a compiler pass over five small
# source trees (seconds), extracted INSIDE frontend/ so module resolution finds frontend's
# node_modules; the work directory is removed on exit.
#
# Exit 1 is a finding; anything that stops the comparison happening is a different message.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=hack/api-baseline.sh
. "$ROOT/hack/api-baseline.sh"

FRONTEND="$ROOT/frontend"
TOOL="$ROOT/hack/api-report"
REPORTS="$FRONTEND/api"
BREAKS="$REPORTS/breaks.txt"
WORK="$FRONTEND/.api-baseline"

# api_cli <args...>: the tool, with exit 2 (could not run) kept apart from 1 (a finding).
api_cli() { node "$TOOL/cli.mjs" "$@"; }

preflight() {
  command -v node >/dev/null 2>&1 || { echo "::error::node is not available; the npm API check cannot run." >&2; return 1; }
  [ -d "$FRONTEND/node_modules" ] || { echo "::error::frontend/node_modules is missing; run 'npm ci' in frontend/ first." >&2; return 1; }
  [ -f "$TOOL/node_modules/typescript/package.json" ] || { echo "::error::hack/api-report has no node_modules; run 'npm ci' in hack/api-report/ first." >&2; return 1; }
  [ -f "$FRONTEND/packages/client/dist/index.d.ts" ] || { echo "::error::the packages are not built; run 'npm run build:packages' in frontend/ first." >&2; return 1; }
}

# run_cli <what> <args...>: run the tool, turning its exit status into the wrapper's verdict.
run_cli() {
  local what="$1" rc=0
  shift
  api_cli "$@" || rc=$?
  case "$rc" in
    0) return 0 ;;
    1) return 1 ;;
    *)
      echo "::error::the npm API $what could not run (exit $rc); nothing was compared. See the error above." >&2
      return 2
      ;;
  esac
}

self_test() {
  local tmp out rc w
  w="$FRONTEND/.api-selftest"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp" "$w"; trap - RETURN' RETURN
  rm -rf "$w"
  mkdir -p "$w"

  # --- the drift check ---------------------------------------------------------------------
  # 1. The committed reports match the built packages.
  rc=0; out="$(api_cli check --frontend "$FRONTEND" --reports "$REPORTS" 2>&1)" || rc=$?
  check_rc "the committed reports" 0 "$rc" "$out" || return 1

  # 2. A report that drifted - a line removed from the committed file - fails and names it.
  cp -R "$REPORTS" "$tmp/reports"
  local victim
  victim="$(grep -n '^export interface Basemap' "$tmp/reports/client.api.md" | sed -n 1p | cut -d: -f1)"
  [ -n "$victim" ] || { echo "self-test: client.api.md no longer has Basemap; pick another export to drift" >&2; return 1; }
  sed -i "${victim}d" "$tmp/reports/client.api.md"
  rc=0; out="$(api_cli check --frontend "$FRONTEND" --reports "$tmp/reports" 2>&1)" || rc=$?
  check_rc "a drifted report" 1 "$rc" "$out" || return 1
  check_has "a drifted report" "FAIL @devicechain/client: the public API no longer matches" "$out" || return 1
  check_has "a drifted report" "+ export interface Basemap" "$out" || return 1

  # 3. A missing report fails.
  rm "$tmp/reports/brand.api.md"
  rc=0; out="$(api_cli check --frontend "$FRONTEND" --reports "$tmp/reports" 2>&1)" || rc=$?
  check_rc "a missing report" 1 "$rc" "$out" || return 1
  check_has "a missing report" "brand.api.md is missing" "$out" || return 1

  # --- the compatibility check -------------------------------------------------------------
  # The baseline is a COPY of the working tree's packages (their built declarations included),
  # so the only differences are the ones planted here. It lives under frontend/ so that module
  # resolution reaches frontend/node_modules.
  mkdir -p "$w/packages"
  cp -R "$FRONTEND/packages/." "$w/packages/"
  : > "$tmp/none.txt"
  local base_client="$w/packages/client"

  # 4. Identical trees: nothing changed, nothing declared.
  rc=0; out="$(api_cli compat --frontend "$FRONTEND" --base "$w" --baseline v0.0.0 --breaks "$tmp/none.txt" 2>&1)" || rc=$?
  check_rc "identical trees" 0 "$rc" "$out" || return 1
  check_has "identical trees" "0 breaking (0 declared), 0 dangerous, 0 safe; 0 failed" "$out" || return 1

  # 5. The baseline exported a name the working tree does not: removed export; fails naming it.
  echo 'export declare const ZzSelfTestProbe: string;' >> "$base_client/dist/index.d.ts"
  rc=0; out="$(api_cli compat --frontend "$FRONTEND" --base "$w" --baseline v0.0.0 --breaks "$tmp/none.txt" 2>&1)" || rc=$?
  check_rc "a removed export" 1 "$rc" "$out" || return 1
  check_has "a removed export" "FAIL @devicechain/client: .:ZzSelfTestProbe export-removed" "$out" || return 1
  check_has "a removed export" "declare with: v0.0.0 @devicechain/client .:ZzSelfTestProbe export-removed" "$out" || return 1

  # 6. Declared against this baseline: passes and is reported.
  echo "v0.0.0 @devicechain/client .:ZzSelfTestProbe export-removed self-test probe" > "$tmp/declared.txt"
  rc=0; out="$(api_cli compat --frontend "$FRONTEND" --base "$w" --baseline v0.0.0 --breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "a declared removal" 0 "$rc" "$out" || return 1
  check_has "a declared removal" "1 breaking (1 declared)" "$out" || return 1

  # 7. Declared against ANOTHER baseline it is expired: covers nothing, fails again.
  rc=0; out="$(api_cli compat --frontend "$FRONTEND" --base "$w" --baseline v0.0.1 --breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "an expired declaration" 1 "$rc" "$out" || return 1
  check_has "an expired declaration" "expired v0.0.0" "$out" || return 1

  # 8. A removed MEMBER of a kept export is a change, not an addition: fails.
  sed -i '$d' "$base_client/dist/index.d.ts"
  sed -i 's/^    tileUrl?: string | null;$/    tileUrl?: string | null;\n    zzSelfTestMember?: string;/' "$base_client/dist/basemap.d.ts"
  grep -q zzSelfTestMember "$base_client/dist/basemap.d.ts" || { echo "self-test: could not plant a member in basemap.d.ts" >&2; return 1; }
  rc=0; out="$(api_cli compat --frontend "$FRONTEND" --base "$w" --baseline v0.0.0 --breaks "$tmp/none.txt" 2>&1)" || rc=$?
  check_rc "a removed member" 1 "$rc" "$out" || return 1
  check_has "a removed member" "FAIL @devicechain/client: .:Basemap export-changed - zzSelfTestMember?: string;" "$out" || return 1

  # 9. A declaration with no change behind it is stale.
  cp "$FRONTEND/packages/client/dist/basemap.d.ts" "$base_client/dist/basemap.d.ts"
  rc=0; out="$(api_cli compat --frontend "$FRONTEND" --base "$w" --baseline v0.0.0 --breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "a stale declaration" 1 "$rc" "$out" || return 1
  check_has "a stale declaration" "FAIL stale declaration" "$out" || return 1

  # 10. A REQUIRED member added since the baseline is reported and does not fail.
  local decl
  decl="$(grep -rl --include='*.d.ts' 'interface DecodedClaims' "$base_client/dist" | sed -n 1p || true)"
  [ -n "$decl" ] || { echo "self-test: DecodedClaims is gone; pick another interface with a required member" >&2; return 1; }
  sed -i '/^    username: string;$/d' "$decl"
  rc=0; out="$(api_cli compat --frontend "$FRONTEND" --base "$w" --baseline v0.0.0 --breaks "$tmp/none.txt" 2>&1)" || rc=$?
  check_rc "an added required member" 0 "$rc" "$out" || return 1
  check_has "an added required member" "dangerous @devicechain/client .:DecodedClaims member-added + username: string;" "$out" || return 1

  # 11. A removed manifest subpath is a break (the brand package has only stylesheets and tokens).
  cp -R "$FRONTEND/packages/." "$w/packages/"
  node -e '
    const fs = require("fs"); const f = process.argv[1];
    const m = JSON.parse(fs.readFileSync(f, "utf8"));
    m.exports["./css/zz-self-test.css"] = "./css/brand.css";
    fs.writeFileSync(f, JSON.stringify(m, null, 2));
  ' "$w/packages/brand/package.json"
  rc=0; out="$(api_cli compat --frontend "$FRONTEND" --base "$w" --baseline v0.0.0 --breaks "$tmp/none.txt" 2>&1)" || rc=$?
  check_rc "a removed manifest subpath" 1 "$rc" "$out" || return 1
  check_has "a removed manifest subpath" "FAIL @devicechain/brand: exports:./css/zz-self-test.css export-removed" "$out" || return 1

  # 12. The verdict mapping the wrappers use.
  verdict_self_test || return 1

  echo "npm-api self-test: committed reports match; a drifted and a missing report fail; identical trees pass; a removed export, member and manifest subpath fail naming them; declared passes; expired and stale fail; an added required member is reported only"
}

case "${1:-}" in
  --self-test)
    [ $# -eq 1 ] || { echo "usage: $0 --self-test" >&2; exit 2; }
    preflight || exit 1
    self_test
    exit
    ;;
  --update)
    [ $# -eq 1 ] || { echo "usage: $0 --update" >&2; exit 2; }
    preflight || exit 1
    run_cli "report writer" write --frontend "$FRONTEND" --out "$REPORTS" || exit 1
    set -- "$REPORTS"/*.api.md
    echo "rewrote $# reports in frontend/api/. Review the diff and commit it."
    exit 0
    ;;
esac

[ $# -le 1 ] || { echo "usage: $0 [baseline-tag | --update | --self-test]" >&2; exit 2; }
preflight || exit 1
baseline="$(resolve_baseline "${1:-}")" || exit 1
echo "npm API baseline: $baseline"

log="$(mktemp)"
trap 'rm -rf "$WORK" "$log"' EXIT
rc=0

# 1. Drift.
drift_rc=0
api_cli check --frontend "$FRONTEND" --reports "$REPORTS" 2>&1 | tee -a "$log" || drift_rc=${PIPESTATUS[0]}
[ "$drift_rc" -le 1 ] || { echo "::error::the npm API drift check could not run (exit $drift_rc)." >&2; summarize "npm API" "$log"; exit 1; }
[ "$drift_rc" -eq 0 ] || rc=1

# 2. Compatibility with the baseline, built from its sources.
rm -rf "$WORK"
mkdir -p "$WORK/x"
if ! git archive --format=tar "$baseline" frontend/packages | tar -x -C "$WORK/x"; then
  echo "::error::$baseline has no frontend/packages to compare against." >&2
  exit 1
fi
mkdir -p "$WORK"
mv "$WORK/x/frontend/packages" "$WORK/packages"
rm -rf "$WORK/x"
[ -d "$WORK/packages/client" ] || { echo "::error::$baseline has no frontend/packages/client; nothing to compare." >&2; exit 1; }

emit_rc=0
api_cli emit --frontend "$FRONTEND" --base "$WORK" >"$WORK/emit.log" 2>&1 || emit_rc=$?
if [ "$emit_rc" -ne 0 ]; then
  cat "$WORK/emit.log" >&2
  echo "::error::could not build the declarations of $baseline (exit $emit_rc); nothing was compared." >&2
  exit 1
fi

crc=0
api_cli compat --frontend "$FRONTEND" --base "$WORK" --baseline "$baseline" --breaks "$BREAKS" 2>&1 | tee -a "$log" || crc=${PIPESTATUS[0]}
verdict "npm API" "$baseline" "$crc" || rc=1
summarize "npm API (${baseline})" "$log"
exit "$rc"
