#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Prints the node's and the pump's presence and the last value of each numeric metric.
#
# Only numeric metrics become measurements. Outputs/Run and Outputs/Interlock are booleans, so
# they never appear here; the command that writes one is confirmed from the node's report,
# not from a stored measurement.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

TOK="$(tenant_token "$(login)")"
STATE_GQL="$DC_URL/api/device-state/graphql"

for ext in plant-a/pump-station plant-a/pump-station/pump-1; do
  st="$(gql "$STATE_GQL" "$TOK" 'query($e:[String!]!){deviceStatesByExternalId(externalIds:$e){deviceToken active lastConnectTime lastDisconnectTime}}' \
    "$(jq -nc --arg e "$ext" '{e:[$e]}')" | jq -c '.data.deviceStatesByExternalId[0] // empty')"
  if [ -z "$st" ]; then echo "$ext: not seen yet"; continue; fi
  tok="$(jq -r .deviceToken <<<"$st")"
  echo "$ext  online=$(jq -r .active <<<"$st")  connected=$(jq -r '.lastConnectTime // "-"' <<<"$st")  disconnected=$(jq -r '.lastDisconnectTime // "-"' <<<"$st")"
  gql "$STATE_GQL" "$TOK" 'query($d:String!){latestMeasurements(deviceToken:$d){name value}}' \
    "$(jq -nc --arg d "$tok" '{d:$d}')" | jq -r '.data.latestMeasurements[] | "    \(.name) = \(.value)"'
done
