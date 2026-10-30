#!/bin/sh
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# A certificate device in ~40 lines of shell. It presents a client certificate to the platform
# broker and then does what any device does:
#   - publishes a temperature every PUBLISH_SECONDS,
#   - listens on its own command topic, and
#   - answers every command with the command's own token and the delivery's dispatchNonce, which
#     is what moves the command from SENT to SUCCESSFUL.
#
# What makes it a certificate device is only the connection: the username is "<tenant>:@x509",
# there is no password, and the TLS client certificate is the credential.
set -eu

: "${MQTT_HOST:?}" "${INSTANCE:?}" "${TENANT:?}" "${DEVICE:?}"
MQTT_PORT="${MQTT_PORT:-31883}"
PUBLISH_SECONDS="${PUBLISH_SECONDS:-5}"

# --insecure skips only the HOST NAME check: the broker's certificate is issued for the instance's
# own names, not for the address a local kind node answers on. The chain is still verified against
# the instance CA. Against a broker whose certificate covers the name you connect to, drop it.
tls="--cafile /pki/instance-ca.crt --insecure --cert /pki/device.crt --key /pki/device.key"
conn="-h $MQTT_HOST -p $MQTT_PORT -V mqttv311 $tls -u $TENANT:@x509"
# Each connection needs its own client id: the third segment is the device token, and anything after it is free.
cid="$INSTANCE:$TENANT:$DEVICE"

# field NAME: the string value of a top-level key in the one-line JSON on stdin.
field() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"; }

# Commands in, answers out.
# shellcheck disable=SC2086
mosquitto_sub $conn -i "$cid" -q 1 -t "$INSTANCE/$TENANT/device-commands/$DEVICE" |
while IFS= read -r msg; do
  token="$(printf '%s' "$msg" | field token)"
  nonce="$(printf '%s' "$msg" | field dispatchNonce)"
  name="$(printf '%s' "$msg" | field name)"
  echo "command $name ($token)"
  # shellcheck disable=SC2086
  mosquitto_pub $conn -i "$cid:resp" -q 1 -t "$INSTANCE/$TENANT/command-responses/$DEVICE" \
    -m "{\"commandToken\":\"$token\",\"dispatchNonce\":\"$nonce\",\"success\":true,\"payload\":\"$name done\"}"
  echo "answered $token"
done &

# Telemetry out.
t=20
while :; do
  # shellcheck disable=SC2086
  mosquitto_pub $conn -i "$cid:pub" -q 1 -t "$INSTANCE/$TENANT/devices/$DEVICE/events" \
    -m "{\"device\":\"$DEVICE\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$t\"}}]}}" \
    && echo "published temperature=$t" || echo "publish refused (exit $?)"
  t=$((t % 25 + 1))
  sleep "$PUBLISH_SECONDS"
done
