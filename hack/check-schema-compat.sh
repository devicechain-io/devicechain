#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Do the device wire contract and the topic layout still match the last release?
#
#   hack/check-schema-compat.sh                 # every surface, baseline = nearest stable tag
#   hack/check-schema-compat.sh contract        # one surface: contract | topics | reports
#   hack/check-schema-compat.sh contract v0.24.0
#   hack/check-schema-compat.sh --self-test     # prove each surface can fail (or, for reports, cannot)
#
#   contract  backend/services/*/contract/*.schema.json   FAILS on a break not declared in
#             backend/tools/docsgql/apicompat/jsonschema/breaks.txt: a removed property, a
#             newly required one, a narrowed type or enum, a closed object that was open,
#             a tightened bound, a removed file.
#   topics    the MQTT topics one device may publish to and subscribe to, read from the
#             broker grant builder of the baseline release and of the working tree; FAILS on
#             a topic that is no longer granted, unless declared in apicompat/topics/breaks.txt.
#   reports   deploy/helm/devicechain/values.schema.json and the Instance CRDs. The versioning
#             policy does not cover them, so their changes are PRINTED and never fail; each
#             belongs in the release highlights.
#
# Exit 1 is a break (or a stale or invalid declaration); anything that stops the comparison
# from happening - no tag, no baseline, an export that fails - is a different message.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=hack/api-baseline.sh
. "$ROOT/hack/api-baseline.sh"

BIN_DIR="$(mktemp -d)"
trap 'rm -rf "$BIN_DIR"' EXIT
build_apicompat "$BIN_DIR"
APICOMPAT="$BIN_DIR/apicompat"

CONTRACT_DIR=backend/services/event-sources/contract
CONTRACT=measurement-payload.schema.json
VALUES=deploy/helm/devicechain/values.schema.json
CRD=backend/k8s/config/crd/bases/core.devicechain.io_instances.yaml

# plant_property <file> <name>: add a string property to the document's first "properties"
# object, the way a release that had one and a working tree that dropped it differ.
plant_property() {
  sed -i '0,/"properties": {/s//"properties": {\n    "'"$2"'": { "type": "string" },/' "$1"
  grep -q "\"$2\"" "$1" || { echo "self-test: could not plant $2 in $1" >&2; return 1; }
}

self_test_contract() {
  local tmp out rc c
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"; trap - RETURN' RETURN
  mkdir -p "$tmp/base"
  copy_workspace "$tmp/base"
  : > "$tmp/none.txt"
  c="$tmp/base/$CONTRACT_DIR/$CONTRACT"

  # 1. Identical trees pass with zero changes.
  rc=0; out="$("$APICOMPAT" contract -head "$ROOT" -baseline v0.0.0 -base "$tmp/base" -breaks "$tmp/none.txt" 2>&1)" || rc=$?
  check_rc "contract: identical trees" 0 "$rc" "$out" || return 1
  check_has "contract: identical trees" ' 0 breaking (0 declared), 0 dangerous, 0 safe; 0 failed' "$out" || return 1

  # 2. The baseline had a REQUIRED property the working tree does not, did not require one
  #    the working tree does, and had a whole contract file the working tree does not.
  plant_property "$c" zzSelfTestProbe || return 1
  sed -i 's/"required": \["entries"\]/"required": ["zzSelfTestProbe"]/' "$c"
  grep -q '"required": \["zzSelfTestProbe"\]' "$c" || { echo "self-test: could not plant the required list" >&2; return 1; }
  echo '{ "type": "object" }' > "$tmp/base/$CONTRACT_DIR/zz-selftest.schema.json"
  rc=0; out="$("$APICOMPAT" contract -head "$ROOT" -baseline v0.0.0 -base "$tmp/base" -breaks "$tmp/none.txt" 2>&1)" || rc=$?
  check_rc "contract: planted breaks" 1 "$rc" "$out" || return 1
  check_has "contract: removed property" "$CONTRACT: \$.zzSelfTestProbe property-removed" "$out" || return 1
  check_has "contract: newly required property" "$CONTRACT: \$.entries required-added" "$out" || return 1
  check_has "contract: removed file" 'zz-selftest.schema.json: $ schema-removed' "$out" || return 1
  check_has "contract: a relaxed requirement is only reported" "dangerous $CONTRACT: \$.zzSelfTestProbe required-removed" "$out" || return 1

  # 3. Declared against this baseline: passes.
  cat > "$tmp/declared.txt" <<TXT
v0.0.0 $CONTRACT \$.zzSelfTestProbe property-removed self-test probe
v0.0.0 $CONTRACT \$.entries required-added self-test probe
v0.0.0 zz-selftest.schema.json \$ schema-removed self-test probe
TXT
  rc=0; out="$("$APICOMPAT" contract -head "$ROOT" -baseline v0.0.0 -base "$tmp/base" -breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "contract: declared breaks" 0 "$rc" "$out" || return 1
  check_has "contract: declared breaks" '3 breaking (3 declared)' "$out" || return 1

  # 4. Declared against another baseline: expired, so the breaks fail again.
  rc=0; out="$("$APICOMPAT" contract -head "$ROOT" -baseline v0.0.1 -base "$tmp/base" -breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "contract: expired declarations" 1 "$rc" "$out" || return 1
  check_has "contract: expired declarations" 'expired' "$out" || return 1

  # 5. Declarations with nothing behind them are stale.
  rm -rf "$tmp/clean" && mkdir -p "$tmp/clean" && copy_workspace "$tmp/clean"
  rc=0; out="$("$APICOMPAT" contract -head "$ROOT" -baseline v0.0.0 -base "$tmp/clean" -breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "contract: stale declarations" 1 "$rc" "$out" || return 1
  check_has "contract: stale declarations" 'but that change is not in the device wire contract' "$out" || return 1
}

self_test_topics() {
  local tmp out rc
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"; trap - RETURN' RETURN
  : > "$tmp/none.txt"
  echo "self-test: exporting the working tree's topic layout ..."
  "$APICOMPAT" export-topics -tree "$ROOT" -out "$tmp/head.json" || return 1
  grep -q 'devices/{device}/events' "$tmp/head.json" || { echo "self-test: the layout holds no device events topic; it read nothing" >&2; return 1; }

  rc=0; out="$("$APICOMPAT" topics -baseline v0.0.0 -base-json "$tmp/head.json" -head-json "$tmp/head.json" -breaks "$tmp/none.txt" 2>&1)" || rc=$?
  check_rc "topics: identical layouts" 0 "$rc" "$out" || return 1
  check_has "topics: identical layouts" ' 0 breaking (0 declared), 0 dangerous, 0 safe; 0 failed' "$out" || return 1

  # The baseline spelled the events topic differently: the working tree changed a topic's
  # shape, which is one topic no longer granted (the break) and one newly granted.
  sed 's#devices/{device}/events#devices/{device}/event#' "$tmp/head.json" > "$tmp/base.json"
  rc=0; out="$("$APICOMPAT" topics -baseline v0.0.0 -base-json "$tmp/base.json" -head-json "$tmp/head.json" -breaks "$tmp/none.txt" 2>&1)" || rc=$?
  check_rc "topics: a changed shape" 1 "$rc" "$out" || return 1
  check_has "topics: a changed shape" 'topics: publish:{instance}/{tenant}/devices/{device}/event topic-removed' "$out" || return 1

  echo 'v0.0.0 topics publish:{instance}/{tenant}/devices/{device}/event topic-removed self-test probe' > "$tmp/declared.txt"
  rc=0; out="$("$APICOMPAT" topics -baseline v0.0.0 -base-json "$tmp/base.json" -head-json "$tmp/head.json" -breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "topics: declared" 0 "$rc" "$out" || return 1
  check_has "topics: declared" '1 breaking (1 declared)' "$out" || return 1

  rc=0; out="$("$APICOMPAT" topics -baseline v0.0.1 -base-json "$tmp/base.json" -head-json "$tmp/head.json" -breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "topics: expired" 1 "$rc" "$out" || return 1

  rc=0; out="$("$APICOMPAT" topics -baseline v0.0.0 -base-json "$tmp/head.json" -head-json "$tmp/head.json" -breaks "$tmp/declared.txt" 2>&1)" || rc=$?
  check_rc "topics: stale" 1 "$rc" "$out" || return 1
  check_has "topics: stale" 'but that change is not in the device topic layout' "$out" || return 1
}

# The report-only surfaces are the OPPOSITE negative control: a planted break must be
# PRINTED, naming the element, and the exit status must stay 0.
self_test_reports() {
  local tmp out rc
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"; trap - RETURN' RETURN
  mkdir -p "$tmp/base"
  copy_workspace "$tmp/base"

  rc=0; out="$("$APICOMPAT" reports -head "$ROOT" -base "$tmp/base" 2>&1)" || rc=$?
  check_rc "reports: identical trees" 0 "$rc" "$out" || return 1
  check_has "reports: identical trees" '0 change(s) to mention' "$out" || return 1

  plant_property "$tmp/base/$VALUES" zzSelfTestValue || return 1
  sed -i '0,/^        properties:$/s//&\n          zzSelfTestField:\n            type: string/' "$tmp/base/$CRD"
  grep -q zzSelfTestField "$tmp/base/$CRD" || { echo "self-test: could not plant the CRD field" >&2; return 1; }
  rc=0; out="$("$APICOMPAT" reports -head "$ROOT" -base "$tmp/base" 2>&1)" || rc=$?
  check_rc "reports: planted removals (must print, never fail)" 0 "$rc" "$out" || return 1
  check_has "reports: a removed values key" '$.zzSelfTestValue property-removed' "$out" || return 1
  check_has "reports: a removed CRD field" 'zzSelfTestField property-removed' "$out" || return 1
}

self_test() {
  self_test_contract || return 1
  self_test_topics || return 1
  self_test_reports || return 1
  verdict_self_test || return 1
  echo "schema-compat self-test: contract (removed property, newly required, removed file) and topics (changed shape) fail naming the element; declared passes; expired and stale fail; values/CRD removals print and exit 0; exit 1 and exit 2 both fail the wrapper"
}

case "${1:-}" in
  --self-test)
    [ $# -eq 1 ] || { echo "usage: $0 --self-test" >&2; exit 2; }
    self_test
    exit
    ;;
esac

surface="${1:-all}"
case "$surface" in
  all | contract | topics | reports) ;;
  *) echo "usage: $0 [all|contract|topics|reports] [baseline-tag | --self-test]" >&2; exit 2 ;;
esac
if [ $# -gt 2 ]; then
  echo "usage: $0 [all|contract|topics|reports] [baseline-tag | --self-test]" >&2
  exit 2
fi

baseline="$(resolve_baseline "${2:-}")" || exit 1
echo "schema compatibility baseline: $baseline (surface: $surface)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp" "$BIN_DIR"' EXIT
extract_baseline "$baseline" "$tmp" || exit 1

status=0
run() {
  local name="$1" rc=0 log
  shift
  log="$(mktemp -p "$tmp")"
  "$APICOMPAT" "$@" >"$log" 2>&1 || rc=$?
  cat "$log"
  summarize "$name against $baseline" "$log"
  verdict "$name" "$baseline" "$rc" || status=1
}

want() { [ "$surface" = all ] || [ "$surface" = "$1" ]; }
if want contract; then run "device wire contract" contract -head "$ROOT" -baseline "$baseline" -base "$tmp"; fi
if want topics; then run "device topic layout" topics -head "$ROOT" -baseline "$baseline" -base "$tmp"; fi
if want reports; then run "values and CRD report" reports -head "$ROOT" -base "$tmp"; fi
exit "$status"
