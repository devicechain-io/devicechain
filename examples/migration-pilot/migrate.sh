#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Moves the example "previous platform" export in ./source into DeviceChain, using the public
# GraphQL API only. Every step is safe to run again.
#
#   ./migrate.sh plan      what the export would become (reads files only, touches nothing)
#   ./migrate.sh model     tenant, device profiles, metrics, detection rules, device types
#   ./migrate.sh check     DRY RUN of the device import: decides every row, writes nothing
#   ./migrate.sh import    the real device import (with factory credentials), then attributes
#
# What each step found is written to ./out/*.jsonl; ./reconcile.sh turns it into the report.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

SRC=source
MAP=mapping.json
STEP="${1:-}"

csv_rows() {
  tr -d '\r' < "$SRC/devices.csv" |
    jq -Rn '[inputs | split(",")] | .[0] as $h | .[1:] | map([$h, .] | transpose | map({(.[0]): .[1]}) | add)'
}

# import_rows: the export's devices as importDevices rows. The credential secret rides in these
# rows, so they are only ever written to ./out (git-ignored, mode 0600), never printed.
import_rows() {
  csv_rows | jq --slurpfile m "$MAP" '
    $m[0] as $m
    | map({
        token: (.serial | ascii_downcase),
        deviceTypeToken: ($m.types[.type].deviceType // .type),
        externalId: .serial,
        name: .name,
        metadata: ({site, firmware, previousName: .name} | tojson),
        credentialType: ($m.credentialTypes[.credentialType] // .credentialType),
        credentialId: (if .credentialType == "BASIC" then (.serial | ascii_downcase) else .credential end)
      } + (if .credentialType == "BASIC" then {credentialValue: .credential} else {} end))'
}

# rule_json RULE-INDEX: the source rule as a DeviceChain detection rule definition.
rule_json() {
  jq -c --argjson i "$1" --slurpfile m "$MAP" '
    $m[0] as $m | .[$i] as $r
    | ($r.name | ascii_downcase | gsub("[^a-z0-9]+"; "-") | sub("-$"; "")) as $tok
    | {name: $r.name,
       type: (if $r.forMinutes > 0 then "duration" else "threshold" end),
       severity: $m.severities[$r.severity],
       when: {metric: $r.condition.key, op: $m.operators[$r.condition.operator], threshold: $r.condition.value}}
      + (if $r.forMinutes > 0 then {hold: "\($r.forMinutes)m"} else {} end)
      + {actions: [{type: "raiseAlarm", raiseAlarm: {alarmKey: $tok}}]}' "$SRC/rules.json"
}
rule_token() { jq -r --argjson i "$1" '.[$i].name | ascii_downcase | gsub("[^a-z0-9]+"; "-") | sub("-$"; "")' "$SRC/rules.json"; }

# led FILE KIND SOURCE TARGET OUTCOME [REASON]: one line of the ledger the report is built from.
led() {
  jq -nc --arg k "$2" --arg s "$3" --arg t "$4" --arg o "$5" --arg r "${6:-}" \
    '{kind:$k, source:$s, target:$t, outcome:$o} + (if $r == "" then {} else {reason:$r} end)' >> "$1"
}

plan() {
  echo "== device types -> profiles, metrics =="
  jq -r --slurpfile m "$MAP" '
    $m[0] as $m | .[] | . as $t | $m.types[.name] as $mt
    | "\(.name)  ->  profile \($mt.profile), device type \($mt.deviceType)",
      (.telemetryKeys[] | if $m.dataTypes[.type] then "    metric \(.key)  \($m.dataTypes[.type])  \(.unit // "-")"
         else "    SKIPPED \(.key) (\(.type)): \($m.unsupportedDataTypes[.type] // "unsupported type")" end)' "$SRC/device-types.json"
  echo
  echo "== rules =="
  local n i
  n="$(jq length "$SRC/rules.json")"
  for ((i = 0; i < n; i++)); do
    jq -r --argjson i "$i" --slurpfile m "$MAP" '
      $m[0] as $m | .[$i] | if $m.ruleKinds[.kind] and (.kind != "script-chain")
        then "\(.name)  ->  \($m.ruleKinds[.kind])"
        else "\(.name)  ->  SKIPPED: \($m.ruleKinds[.kind] // "unknown rule kind")" end' "$SRC/rules.json"
    jq -r --argjson i "$i" '.[$i].kind' "$SRC/rules.json" | grep -qx threshold && echo "    $(rule_json "$i")"
  done
  echo
  echo "== dashboard =="
  jq -r '"\(.title)  ->  SKIPPED: dashboards are rebuilt by hand in the canvas editor (\(.widgets | length) widgets to place)"' "$SRC/dashboard.json"
  echo
  echo "== devices =="
  csv_rows | jq -r --slurpfile m "$MAP" '
    $m[0] as $m
    | "\(length) rows in the export",
      (group_by(.type) | .[] | "    \(.[0].type): \(length)"),
      (group_by(.credentialType) | .[] | "    credential \(.[0].credentialType) -> \($m.credentialTypes[.[0].credentialType] // "UNMAPPED"): \(length)")'
  echo
  echo "(the server decides each device row; run './migrate.sh check' after 'model' for the dry run)"
}

ensure_tenant() {
  local idt tier out
  idt="$(login)"
  tier="$(gql "$ADMIN_GQL" "$idt" '{tenantTiers{token}}' | jq -er '.data.tenantTiers[0].token')"
  if out="$(gql "$ADMIN_GQL" "$idt" 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
       "$(jq -nc --arg t "$DC_TENANT" --arg tier "$tier" '{r:{token:$t,name:"Migration pilot",tierToken:$tier}}')" 2>&1 >/dev/null)"; then
    echo "tenant $DC_TENANT: created"
  elif grep -q CONFLICT <<<"$out"; then
    echo "tenant $DC_TENANT: exists"
  else
    echo "$out" >&2; die "could not create tenant $DC_TENANT"
  fi
}

model() {
  ensure_tenant
  session
  local L="$OUT/model.jsonl" o
  : > "$L"
  local types t profile dtype cat
  mapfile -t types < <(jq -r '.[].name' "$SRC/device-types.json")
  for t in "${types[@]}"; do
    profile="$(jq -r --arg t "$t" '.types[$t].profile' "$MAP")"
    dtype="$(jq -r --arg t "$t" '.types[$t].deviceType' "$MAP")"
    cat="$(jq -r --arg t "$t" '.types[$t].category' "$MAP")"
    o="$(create "$DM_GQL" "profile $profile" 'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
      "$(jq -nc --arg p "$profile" --arg n "$t" --arg c "$cat" '{r:{token:$p,name:$n,category:$c}}')")"
    led "$L" profile "$t" "$profile" "$o"; echo "profile $profile: $o"

    # one metric per numeric/boolean telemetry key; a string key is recorded as skipped, with why
    local keys k
    mapfile -t keys < <(jq -c --arg t "$t" '.[] | select(.name == $t) | .telemetryKeys[]' "$SRC/device-types.json")
    for k in "${keys[@]}"; do
      local key typ unit dt
      key="$(jq -r .key <<<"$k")"; typ="$(jq -r .type <<<"$k")"; unit="$(jq -r '.unit // empty' <<<"$k")"
      dt="$(jq -r --arg x "$typ" '.dataTypes[$x] // empty' "$MAP")"
      if [ -z "$dt" ]; then
        led "$L" metric "$t.$key" "" skipped "$(jq -r --arg x "$typ" '.unsupportedDataTypes[$x] // "unsupported type"' "$MAP")"
        echo "metric $key: skipped ($typ)"; continue
      fi
      o="$(create "$DM_GQL" "metric $key" 'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){token}}' \
        "$(jq -nc --arg p "$profile" --arg k "$key" --arg dt "$dt" --arg u "$unit" \
          '{r:({token:($p+"-"+$k),deviceProfileToken:$p,metricKey:$k,name:$k,dataType:$dt} + (if $u == "" then {} else {unit:$u} end))}')")"
      led "$L" metric "$t.$key" "$profile/$key" "$o"; echo "metric $profile/$key: $o"
    done

    # the source rules that apply to this type
    local nr i
    nr="$(jq length "$SRC/rules.json")"
    for ((i = 0; i < nr; i++)); do
      [ "$(jq -r --argjson i "$i" '.[$i].appliesToType' "$SRC/rules.json")" = "$t" ] || continue
      local rname kind
      rname="$(jq -r --argjson i "$i" '.[$i].name' "$SRC/rules.json")"
      kind="$(jq -r --argjson i "$i" '.[$i].kind' "$SRC/rules.json")"
      if [ "$kind" != threshold ]; then
        led "$L" rule "$rname" "" skipped "$(jq -r --arg k "$kind" '.ruleKinds[$k] // "unknown rule kind"' "$MAP")"
        echo "rule $rname: skipped ($kind)"; continue
      fi
      local rtok def
      rtok="$(rule_token "$i")"; def="$(rule_json "$i")"
      o="$(create "$DM_GQL" "rule $rtok" 'mutation($r:DetectionRuleCreateRequest!){createDetectionRule(request:$r){token}}' \
        "$(jq -nc --arg t "$rtok" --arg p "$profile" --arg n "$rname" --arg d "$def" '{r:{token:$t,deviceProfileToken:$p,name:$n,enabled:true,definition:$d}}')")"
      led "$L" rule "$rname" "$profile/$rtok" "$o"; echo "rule $rtok: $o"
    done

    # rules reach live telemetry only when the profile is published; the compiler checks them then
    local active
    active="$(gql "$DM_GQL" "$TOK" "$(printf '{deviceProfilesByToken(tokens:["%s"]){activeVersion}}' "$profile")" \
      | jq -r '.data.deviceProfilesByToken[0].activeVersion // empty')"
    if [ -n "$active" ]; then
      echo "profile $profile: already published (version $active)"
    else
      gql "$DM_GQL" "$TOK" "$(printf 'mutation{publishDeviceProfile(token:"%s",label:"migrated"){version}}' "$profile")" >/dev/null \
        || die "could not publish $profile (the compiler's reason is above)"
      echo "profile $profile: published"
    fi

    o="$(create "$DM_GQL" "device type $dtype" 'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
      "$(jq -nc --arg d "$dtype" --arg p "$profile" --arg n "$t" '{r:{token:$d,name:$n,profileToken:$p}}')")"
    led "$L" deviceType "$t" "$dtype" "$o"; echo "device type $dtype: $o"
  done

  led "$L" dashboard "$(jq -r .title "$SRC/dashboard.json")" "" skipped "dashboards are rebuilt by hand in the canvas editor"
  echo "dashboard: skipped (rebuild by hand)"
}

# run_import DRYRUN(true|false): send the rows, save the answer to ./out/import-<mode>.json
run_import() {
  local dry=$1 rows n resp
  rows="$OUT/import-rows.json"
  ( umask 077; import_rows > "$rows" )
  n="$(jq length "$rows")"
  [ "$n" -le 1000 ] || die "$n rows: a call takes at most 1000; split the file and send the parts one after another"
  session
  resp="$(gql "$DM_GQL" "$TOK" '
    mutation($rows:[DeviceImportRow!]!, $dry:Boolean!) {
      importDevices(rows:$rows, mode:FACTORY_CREDENTIALS, dryRun:$dry) {
        dryRun created exists conflict invalid refused
        rows { row token outcome code message }
      }
    }' "$(jq -c --argjson dry "$dry" '{rows:., dry:$dry}' "$rows")")" || die "importDevices failed"
  local f
  f="$OUT/import-$([ "$dry" = true ] && echo dry-run || echo real).json"
  jq '.data.importDevices' <<<"$resp" > "$f"
  jq -r '"dryRun=\(.dryRun)  created=\(.created) exists=\(.exists) conflict=\(.conflict) invalid=\(.invalid) refused=\(.refused)"' "$f"
  jq -r '.rows[] | select(.outcome != "CREATED" and .outcome != "EXISTS") | "  row \(.row) \(.token): \(.outcome) \(.code): \(.message)"' "$f"
}

import_real() {
  run_import false
  local L="$OUT/devices.jsonl" r
  : > "$L"
  # ledger: join each answer back to its source row (same position)
  jq -c --slurpfile res "$OUT/import-real.json" --argjson src "$(csv_rows)" '
    . as $rows | range(0; length) as $i
    | {serial: $src[$i].serial, name: $src[$i].name, token: $rows[$i].token, credType: $rows[$i].credentialType,
       outcome: ($res[0].rows[$i].outcome | ascii_downcase), code: $res[0].rows[$i].code, message: $res[0].rows[$i].message}' \
    "$OUT/import-rows.json" |
  while IFS= read -r r; do
    jq -c '{kind:"device", source:(.serial+" ("+.name+")"), target:.token, outcome:.outcome}
           + (if .message then {reason:(.code+": "+.message)} else {} end)' <<<"$r" >> "$L"
    # the credential travels in the same row and the same transaction as the device
    case "$(jq -r .outcome <<<"$r")" in
      created) jq -c '{kind:"credential", source:.serial, target:.token, outcome:"created", reason:("type "+.credType)}' <<<"$r" >> "$L" ;;
      exists) jq -c '{kind:"credential", source:.serial, target:.token, outcome:"exists", reason:("type "+.credType)}' <<<"$r" >> "$L" ;;
      *) jq -c '{kind:"credential", source:.serial, target:.token, outcome:"skipped", reason:"its device was not imported"}' <<<"$r" >> "$L" ;;
    esac
  done

  # attributes: the source's server-side "site" becomes a SERVER attribute of the device
  while IFS=$'\t' read -r tok site; do
    gql "$DM_GQL" "$TOK" 'mutation($r:EntityAttributeSetRequest!){setEntityAttribute(request:$r){attrKey}}' \
      "$(jq -nc --arg d "$tok" --arg v "$site" '{r:{entityType:"device",entity:$d,scope:"SERVER",attrKey:"site",valueType:"STRING",value:$v}}')" >/dev/null \
      || die "could not set the site attribute of $tok"
    jq -nc --arg d "$tok" '{kind:"attribute", source:($d+" site"), target:($d+"/site"), outcome:"created"}' >> "$L"
  done < <(jq -r --slurpfile res "$OUT/import-real.json" --argjson src "$(csv_rows)" '
    range(0; length) as $i | select($res[0].rows[$i].outcome == "CREATED" or $res[0].rows[$i].outcome == "EXISTS")
    | "\(.[$i].token)\t\($src[$i].site)"' "$OUT/import-rows.json" 2>/dev/null || true)
  # a device that was not imported has nothing to hang an attribute on
  jq -r --slurpfile res "$OUT/import-real.json" '
    range(0; length) as $i | select($res[0].rows[$i].outcome != "CREATED" and $res[0].rows[$i].outcome != "EXISTS")
    | .[$i].token' "$OUT/import-rows.json" |
  while IFS= read -r tok; do
    jq -nc --arg d "$tok" '{kind:"attribute", source:($d+" site"), target:"", outcome:"skipped", reason:"its device was not imported"}' >> "$L"
  done
  echo "attributes set for the imported devices"
}

case "$STEP" in
  plan) plan ;;
  model) model ;;
  check) run_import true ;;
  import) import_real ;;
  *) echo "usage: $0 plan|model|check|import" >&2; exit 2 ;;
esac
