#!/bin/sh
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# A cold room in shell. Its temperature creeps up by RISE degrees every tick (a door left open) and
# it reports temperature and humidity. On a "boost-cooling" command it switches the compressor to
# full, ANSWERS the command (which is what completes it on the platform), and cools by COOL degrees
# per tick until it reaches the command's targetC.
#
# The device knows nothing about the rule, the alarm or the dashboard. It reports and obeys.
set -eu

: "${MQTT_HOST:?}" "${INSTANCE:?}" "${TENANT:?}" "${DEVICE:?}" "${DEVICE_PASSWORD:?}"
MQTT_PORT="${MQTT_PORT:-31883}"
TICK="${TICK_SECONDS:-4}"
RISE="${RISE:-1}"
COOL="${COOL:-2}"
START="${START_C:-5}"
TARGET_DEFAULT="${TARGET_C:-4}"

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
  echo "command $name ($token) target=${target:-$TARGET_DEFAULT}"
  if [ "$name" = boost-cooling ]; then
    printf '%s' "${target:-$TARGET_DEFAULT}" > "$state"
    ok=true; out="cooling to ${target:-$TARGET_DEFAULT}"
  else
    ok=false; out="unknown command $name"
  fi
  # shellcheck disable=SC2086
  mosquitto_pub $conn -i "$cid:resp" -q 1 -t "$INSTANCE/$TENANT/command-responses/$DEVICE" \
    -m "{\"commandToken\":\"$token\",\"dispatchNonce\":\"$nonce\",\"success\":$ok,\"payload\":\"$out\"}"
  echo "answered $token: $out"
done &

# Telemetry out.
t="$START"
while :; do
  if [ -f "$state" ]; then
    t=$((t - COOL))
    if [ "$t" -le "$(cat "$state")" ]; then rm -f "$state"; echo "target reached; compressor back to normal"; fi
  else
    t=$((t + RISE))
  fi
  h=$((60 + t))
  # shellcheck disable=SC2086
  mosquitto_pub $conn -i "$cid:pub" -q 1 -t "$INSTANCE/$TENANT/devices/$DEVICE/events" \
    -m "{\"device\":\"$DEVICE\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$t\",\"humidity\":\"$h\"}}]}}" \
    && echo "temperature=$t humidity=$h" || echo "publish refused"
  sleep "$TICK"
done
