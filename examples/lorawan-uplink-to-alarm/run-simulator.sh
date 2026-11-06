#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Plays the network server: posts BINARY LoRaWAN uplinks (the raw six-byte frame on port 2, no
# decoded object) to an integration, the way a ChirpStack HTTP integration or a The Things Stack
# webhook does for a device with no codec or formatter. What is stored is what the room-sensor
# profile's decoder makes of the frame.
#
#   ./run-simulator.sh                 ChirpStack, 12 uplinks, 2 seconds apart
#   NS=tts ./run-simulator.sh          The Things Stack
#   UPLINKS=3 EMIT_INTERVAL=1s ./run-simulator.sh
#
# Needs dc-simulator on PATH (or DC_SIMULATOR=/path/to/dc-simulator). It is attached to every
# release from v1.2, beside dcctl. Set DC_INTEGRATION_HOST when the ingress host does not resolve
# from here and DC_URL points at an address that does.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

NS="${NS:-chirpstack}"
case "$NS" in chirpstack | tts) ;; *) die "NS must be chirpstack or tts" ;; esac
[ -s ".token-$NS" ] || die ".token-$NS is missing: run ./provision.sh first"
SIM="${DC_SIMULATOR:-dc-simulator}"
command -v "$SIM" >/dev/null || die "$SIM not found: put dc-simulator on PATH or set DC_SIMULATOR"

# The token goes in through the environment so it stays out of the process list.
DC_SIM_INTEGRATION_TOKEN="$(cat ".token-$NS")" "$SIM" \
  --lorawan-ns "$NS" \
  --lorawan-binary \
  --integration-url "$DC_URL/api/integrations/$DC_TENANT/$NS" \
  ${DC_INTEGRATION_HOST:+--integration-host "$DC_INTEGRATION_HOST"} \
  --deveui "$DC_DEVEUI" \
  --uplinks "${UPLINKS:-12}" \
  --emit-interval "${EMIT_INTERVAL:-2s}"
