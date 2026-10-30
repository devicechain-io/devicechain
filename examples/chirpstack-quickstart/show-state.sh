#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# What DeviceChain has made of the uplinks: the registered sensor's alias and latest readings,
# and the DevEUIs that were heard but belong to no device.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

TOK="$(tenant_token "$(login)")"
STATE_GQL="$DC_URL/api/device-state/graphql"

echo "== boiler-room-1 =="
gql "$DM_GQL" "$TOK" '{devicesByToken(tokens:["boiler-room-1"]){token aliases{scheme value}}}' \
  | jq -r '.data.devicesByToken[0] | "alias: " + (.aliases | map(.scheme + " " + .value) | join(", "))'
gql "$STATE_GQL" "$TOK" '{latestMeasurements(deviceToken:"boiler-room-1"){name value unit}}' \
  | jq -r '.data.latestMeasurements[] | "    \(.name) = \(.value) \(.unit // "")"'

echo "== unregistered devices seen by the integration =="
gql "$DM_GQL" "$TOK" '{unregisteredIntegrationDevices(criteria:{pageNumber:1,pageSize:20,integration:"chirpstack"}){results{scheme value count lastSeen}}}' \
  | jq -r '.data.unregisteredIntegrationDevices.results[] | "    \(.scheme) \(.value)  seen \(.count) times, last \(.lastSeen)"'
