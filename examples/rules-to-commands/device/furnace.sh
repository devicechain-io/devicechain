#!/bin/sh
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# A furnace in shell. It heats by HEAT degrees every tick and reports its temperature. On a
# "cool-down" command it switches its cooling on, answers the command (which is what completes it
# on the platform), and cools by COOL degrees per tick until it reaches the command's targetC.
#
# The device knows nothing about the rule. It reports and obeys.
set -eu

: "${MQTT_HOST:?}" "${INSTANCE:?}" "${TENANT:?}" "${DEVICE:?}" "${DEVICE_PASSWORD:?}"
MQTT_PORT="${MQTT_PORT:-31883}"
TICK="${TICK_SECONDS:-5}"
HEAT="${HEAT:-4}"
COOL="${COOL:-8}"

# --insecure skips only the HOST NAME check (the broker's certificate is issued for the instance's
# own names, not the kind node's address); the chain is still verified against the instance CA.
conn="-h $MQTT_HOST -p $MQTT_PORT -V mqttv311 --cafile /pki/instance-ca.crt --insecure -u $TENANT:$DEVICE -P $DEVICE_PASSWORD"
cid="$INSTANCE:$TENANT:$DEVICE"
state=/tmp/cooling-target # present while cooling; holds the target temperature

field() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"; }
num() { sed -n "s/.*targetC[^0-9-]*\(-\{0,1\}[0-9][0-9]*\).*/\1/p"; }

# Commands in. The main client id holds the command subscription, which is also what shows the
# device online.
# shellcheck disable=SC2086
mosquitto_sub $conn -i "$cid" -q 1 -t "$INSTANCE/$TENANT/device-commands/$DEVICE" |
while IFS= read -r msg; do
  token="$(printf '%s' "$msg" | field token)"
  nonce="$(printf '%s' "$msg" | field dispatchNonce)"
  name="$(printf '%s' "$msg" | field name)"
  target="$(printf '%s' "$msg" | num)"
  echo "command $name ($token) target=${target:-55}"
  if [ "$name" = cool-down ]; then
    printf '%s' "${target:-55}" > "$state"
    ok=true; out="cooling to ${target:-55}"
  else
    ok=false; out="unknown command $name"
  fi
  # shellcheck disable=SC2086
  mosquitto_pub $conn -i "$cid:resp" -q 1 -t "$INSTANCE/$TENANT/command-responses/$DEVICE" \
    -m "{\"commandToken\":\"$token\",\"dispatchNonce\":\"$nonce\",\"success\":$ok,\"payload\":\"$out\"}"
  echo "answered $token: $out"
done &

# Telemetry out.
t=60
while :; do
  if [ -f "$state" ]; then
    t=$((t - COOL))
    if [ "$t" -le "$(cat "$state")" ]; then rm -f "$state"; echo "target reached; cooling off"; fi
  else
    t=$((t + HEAT))
  fi
  # shellcheck disable=SC2086
  mosquitto_pub $conn -i "$cid:pub" -q 1 -t "$INSTANCE/$TENANT/devices/$DEVICE/events" \
    -m "{\"device\":\"$DEVICE\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$t\"}}]}}" \
    && echo "temperature=$t" || echo "publish refused"
  sleep "$TICK"
done
