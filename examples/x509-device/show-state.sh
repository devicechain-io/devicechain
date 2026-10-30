#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Prints the device's presence, its latest measurement and its credentials.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

TOK="$(tenant_token "$(login)")"
STATE_GQL="$DC_URL/api/device-state/graphql"

st="$(gql "$STATE_GQL" "$TOK" 'query($d:[String!]!){deviceStatesByDeviceToken(deviceTokens:$d){deviceToken active lastConnectTime}}' \
  "$(jq -nc --arg d "$DC_DEVICE" '{d:[$d]}')" | jq -c '.data.deviceStatesByDeviceToken[0] // empty')"
if [ -z "$st" ]; then echo "$DC_DEVICE: not seen yet"; exit 0; fi
echo "$DC_DEVICE  online=$(jq -r .active <<<"$st")  connected=$(jq -r '.lastConnectTime // "-"' <<<"$st")"
gql "$STATE_GQL" "$TOK" 'query($d:String!){latestMeasurements(deviceToken:$d){name value}}' \
  "$(jq -nc --arg d "$DC_DEVICE" '{d:$d}')" | jq -r '.data.latestMeasurements[] | "    \(.name) = \(.value)"'
gql "$DM_GQL" "$TOK" 'query($c:DeviceCredentialSearchCriteria!){deviceCredentials(criteria:$c){results{credentialType credentialId enabled expiresAt}}}' \
  "$(jq -nc --arg d "$DC_DEVICE" '{c:{pageNumber:1,pageSize:10,device:$d}}')" \
  | jq -r '.data.deviceCredentials.results[] | "    credential \(.credentialType) \(.credentialId[0:16])... enabled=\(.enabled) expires=\(.expiresAt)"'
