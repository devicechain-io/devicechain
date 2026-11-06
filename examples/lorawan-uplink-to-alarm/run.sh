#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The whole walkthrough in one command, against an instance you already bootstrapped:
# provision the tenant, profile, decoder, rule, device and integrations; send twelve binary uplinks
# as a ChirpStack would; wait for the alarm to be raised and then cleared; show the result.
# Exits non-zero if an uplink is refused or the alarm is not raised and cleared by THIS run.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

# The decoder in this directory must be the one the simulator's frame layout needs.
SIM="${DC_SIMULATOR:-dc-simulator}"
command -v "$SIM" >/dev/null || die "$SIM not found: put dc-simulator on PATH or set DC_SIMULATOR"
diff <("$SIM" --lorawan-print-decoder) decoder.json >/dev/null \
  || die "decoder.json differs from what '$SIM --lorawan-print-decoder' prints: this dc-simulator expects another frame layout"

./provision.sh

# An alarm is one row per alarm key per device, raised again by each run, so a CLEARED row from an
# earlier run proves nothing: remember when this run started and require the alarm to be raised
# and cleared after it.
started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
summary="$(./run-simulator.sh)"
echo "$summary"
jq -e '.errors == 0 and .statuses["202"] == .sent' <<<"$summary" >/dev/null \
  || die "the integration did not accept every uplink (see the statuses above: 401 is a wrong token, 403 a switched-off integration)"

# Detection is asynchronous: wait for the alarm to be raised and cleared (the 12 uplinks climb
# past 25 degrees on the seventh and fall back on the eleventh).
TOK="$(tenant_token "$(login)")"
echo "waiting for the alarm to be raised and cleared..."
cycles=0
for _ in $(seq 1 30); do
  cycles="$(gql "$DM_GQL" "$TOK" 'query($c:AlarmSearchCriteria!){alarms(criteria:$c){results{alarmKey state raisedTime clearedTime}}}' \
    "$(jq -nc --arg d "$DC_DEVICE" '{c:{pageNumber:1,pageSize:5,originatorType:"device",originatorToken:$d}}')" \
    | jq -r --arg s "$started" '[.data.alarms.results[] | select(.alarmKey == "room-too-warm" and .state == "CLEARED" and .raisedTime > $s and .clearedTime > $s)] | length')"
  [ "$cycles" = 1 ] && break
  sleep 2
done
./show-state.sh
[ "$cycles" = 1 ] || die "the alarm was not raised and cleared after this run started; see the output above"
