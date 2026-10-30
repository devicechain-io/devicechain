#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Creates a device for a DevEUI the integration has heard but that belongs to none: the same thing
# the console's "Create a device with this alias" button does.
#
#   ./add-device.sh cold-store 70b3d57ed0067890
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh
[ $# -eq 2 ] || die "usage: $0 <device-token> <dev-eui>"

TOK="$(tenant_token "$(login)")"
gql "$DM_GQL" "$TOK" 'mutation($r:DeviceCreateRequest!){createDevice(request:$r){token aliases{scheme value}}}' \
  "$(jq -nc --arg t "$1" --arg e "$2" '{r:{token:$t,name:$t,deviceTypeToken:"lora-sensor",aliases:[{scheme:"lorawan.deveui",value:$e}]}}')" \
  | jq -r '.data.createDevice | "created \(.token) with alias \(.aliases[0].scheme) \(.aliases[0].value)"'
echo "uplinks from $2 are stored on $1 from the next one (within about a minute)"
