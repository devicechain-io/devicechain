#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The reconciliation report. Two independent checks, and it exits non-zero unless both agree:
#
#   1. ACCOUNTING   every item in the export is either created, already there, or skipped with a
#                   reason, so created + existing + skipped must equal what the export held.
#   2. READ BACK    what DeviceChain now holds, counted by asking it, must equal what the ledger
#                   says was created or already there.
#
# Reads ./out/model.jsonl and ./out/devices.jsonl (written by migrate.sh) and writes
# ./out/reconciliation.md.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

SRC=source
[ -s "$OUT/model.jsonl" ] && [ -s "$OUT/devices.jsonl" ] || die "run ./migrate.sh model and ./migrate.sh import first"
LEDGER="$(cat "$OUT/model.jsonl" "$OUT/devices.jsonl")"

rows="$(tr -d '\r' < "$SRC/devices.csv" | tail -n +2 | grep -c .)"
declare -A SRCN=(
  [profile]="$(jq length "$SRC/device-types.json")"
  [deviceType]="$(jq length "$SRC/device-types.json")"
  [metric]="$(jq '[.[].telemetryKeys | length] | add' "$SRC/device-types.json")"
  [rule]="$(jq length "$SRC/rules.json")"
  [dashboard]=1
  [device]="$rows"
  [credential]="$rows"
  [attribute]="$rows"
)
KINDS=(profile deviceType metric rule dashboard device credential attribute)

count() { jq -s --arg k "$1" --arg o "$2" '[.[] | select(.kind == $k and .outcome == $o)] | length' <<<"$LEDGER"; }

session
q() { gql "$1" "$TOK" "$2" | jq -r "$3"; }

# --- READ BACK -----------------------------------------------------------------------------
live_devices=0; live_metrics=0; live_rules=0; live_types=0
for p in $(jq -r '.types[].profile' mapping.json); do
  live_metrics=$((live_metrics + $(q "$DM_GQL" "{metricDefinitions(criteria:{pageNumber:1,pageSize:1,deviceProfile:\"$p\"}){pagination{totalRecords}}}" '.data.metricDefinitions.pagination.totalRecords')))
  live_rules=$((live_rules + $(q "$DM_GQL" "{detectionRules(criteria:{pageNumber:1,pageSize:1,deviceProfile:\"$p\"}){pagination{totalRecords}}}" '.data.detectionRules.pagination.totalRecords')))
done
for t in $(jq -r '.types[].deviceType' mapping.json); do
  live_devices=$((live_devices + $(q "$DM_GQL" "{devices(criteria:{pageNumber:1,pageSize:1,deviceType:\"$t\"}){pagination{totalRecords}}}" '.data.devices.pagination.totalRecords')))
  live_types=$((live_types + 1))
done
live_creds=$(q "$DM_GQL" '{deviceCredentials(criteria:{pageNumber:1,pageSize:1}){pagination{totalRecords}}}' '.data.deviceCredentials.pagination.totalRecords')
live_attrs=$(q "$DM_GQL" '{entityAttributes(criteria:{pageNumber:1,pageSize:1,entityType:"device",scope:"SERVER",attrKeys:["site"]}){pagination{totalRecords}}}' '.data.entityAttributes.pagination.totalRecords')

declare -A LIVE=([metric]="$live_metrics" [rule]="$live_rules" [device]="$live_devices" [credential]="$live_creds" [attribute]="$live_attrs")

# --- REPORT --------------------------------------------------------------------------------
rc=0
R="$OUT/reconciliation.md"
{
  echo "# Reconciliation report"
  echo
  echo "Source: \`examples/migration-pilot/source\` (sanitized export of a previous platform)."
  echo "Target: tenant \`$DC_TENANT\`."
  echo
  echo "## 1. Accounting: everything in the export is accounted for"
  echo
  echo "| Item | In export | Created | Already there | Skipped | Accounted | Match |"
  echo "| --- | ---: | ---: | ---: | ---: | ---: | :---: |"
  for k in "${KINDS[@]}"; do
    c=$(count "$k" created); e=$(count "$k" exists); s=$(count "$k" skipped)
    # an invalid / conflicting device row is a skip with a reason, whatever the server called it
    for o in invalid conflict refused; do s=$((s + $(count "$k" "$o"))); done
    n=${SRCN[$k]}; acc=$((c + e + s)); ok=yes; [ "$acc" -eq "$n" ] || { ok=NO; rc=1; }
    echo "| $k | $n | $c | $e | $s | $acc | $ok |"
  done
  echo
  echo "## 2. Read back: DeviceChain holds what the ledger says"
  echo
  echo "| Item | Ledger (created + already there) | Counted in DeviceChain | Match |"
  echo "| --- | ---: | ---: | :---: |"
  for k in metric rule device credential attribute; do
    want=$(( $(count "$k" created) + $(count "$k" exists) ))
    got=${LIVE[$k]}; ok=yes; [ "$want" -eq "$got" ] || { ok=NO; rc=1; }
    echo "| $k | $want | $got | $ok |"
  done
  echo
  echo "## 3. Skipped, and why"
  echo
  echo "| Item | Source | Reason |"
  echo "| --- | --- | --- |"
  jq -rs '.[] | select(.outcome != "created" and .outcome != "exists") | "| \(.kind) | \(.source) | \(.reason // .outcome) |"' <<<"$LEDGER"
  echo
  if [ "$rc" -eq 0 ]; then echo "**Result: reconciled.**"; else echo "**Result: DOES NOT RECONCILE.**"; fi
} > "$R"   # not piped to tee: a pipeline would run this block in a subshell and lose rc
cat "$R"
exit "$rc"
