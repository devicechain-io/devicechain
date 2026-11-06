#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# What the platform made of the uplinks: the sensor's latest readings and its alarms.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

TOK="$(tenant_token "$(login)")"

echo "== $DC_DEVICE: latest readings =="
gql "$STATE_GQL" "$TOK" 'query($d:String!){latestMeasurements(deviceToken:$d){name value unit}}' \
  "$(jq -nc --arg d "$DC_DEVICE" '{d:$d}')" \
  | jq -r '.data.latestMeasurements[] | "    \(.name) = \(.value) \(.unit // "")"'

echo "== alarms =="
gql "$DM_GQL" "$TOK" 'query($c:AlarmSearchCriteria!){alarms(criteria:$c){results{alarmKey severity state raisedTime clearedTime lastValue}}}' \
  "$(jq -nc --arg d "$DC_DEVICE" '{c:{pageNumber:1,pageSize:5,originatorType:"device",originatorToken:$d}}')" \
  | jq -r '.data.alarms.results[] | "    \(.alarmKey)  \(.severity)  \(.state)  raised \(.raisedTime // "-")  cleared \(.clearedTime // "-")  last value \(.lastValue // "-")"'
