#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The whole loop in one screen: the furnace's latest reading, its alarms, and the commands the
# rule sent it, newest first.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

TOK="$(tenant_token "$(login)")"

echo "== $DC_DEVICE =="
gql "$DC_URL/api/device-state/graphql" "$TOK" "$(jq -nc --arg d "$DC_DEVICE" '"{latestMeasurements(deviceToken:\"" + $d + "\"){name value unit}}"' | jq -r .)" \
  | jq -r '.data.latestMeasurements[] | "    \(.name) = \(.value) \(.unit // "")"'

echo "== alarms =="
gql "$DM_GQL" "$TOK" 'query($c:AlarmSearchCriteria!){alarms(criteria:$c){results{alarmKey severity state acknowledged raisedTime clearedTime lastValue}}}' \
  "$(jq -nc --arg d "$DC_DEVICE" '{c:{pageNumber:1,pageSize:5,originatorType:"device",originator:$d}}')" \
  | jq -r '.data.alarms.results[] | "    \(.alarmKey)  \(.severity)  \(.state)  raised \(.raisedTime // "-")  cleared \(.clearedTime // "-")  last value \(.lastValue // "-")"'

echo "== commands sent by the rule =="
gql "$CMD_GQL" "$TOK" 'query($c:CommandSearchCriteria!){commands(criteria:$c){results{name status outcome queuedTime respondedTime responsePayload}}}' \
  "$(jq -nc --arg d "$DC_DEVICE" '{c:{pageNumber:1,pageSize:5,deviceToken:$d}}')" \
  | jq -r '.data.commands.results[] | "    \(.name)  \(.status)  outcome \(.outcome // "-")  queued \(.queuedTime // "-")  answered \(.respondedTime // "-")  \(.responsePayload // "")"'
