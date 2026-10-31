#!/bin/sh
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# A simulated pilot device, "reflashed" for a side-by-side trial. Every tick it takes one reading
# and publishes it to the targets listed in /state/targets:
#
#   legacy  the previous platform: flat JSON, the device name in the topic
#   dc      DeviceChain: the event envelope, over TLS, with the access token as the MQTT username
#
# The targets file is read on every tick, so flipping it is the cutover AND the rollback: it needs
# no restart and no reflash. The readings are a pure function of the tick number, so both platforms
# receive the same values and ./pilot.sh compare can check that they agree.
set -eu

: "${DEVICE:?}" "${KIND:?}" "${ACCESS_TOKEN:?}" "${TENANT:?}" "${INSTANCE:?}" "${DC_HOST:?}" "${LEGACY_HOST:?}"
DC_PORT="${DC_PORT:-31883}"
TICK="${TICK_SECONDS:-3}"
BATTERY="${BATTERY:-87}"

# DeviceChain: the broker's certificate chains to the instance CA (--cafile); --insecure skips only
# the host-name check, because the certificate is issued for the instance's own names and not for
# the kind node's address. The username is {tenant}:{access token}; there is no password.
dc_pub() {
  mosquitto_pub -h "$DC_HOST" -p "$DC_PORT" -V mqttv311 --cafile /pki/instance-ca.crt --insecure \
    -u "$TENANT:$ACCESS_TOKEN" -i "$INSTANCE:$TENANT:$DEVICE:pub" -q 1 \
    -t "$INSTANCE/$TENANT/devices/$DEVICE/events" -m "$1"
}
legacy_pub() {
  mosquitto_pub -h "$LEGACY_HOST" -p 1883 -V mqttv311 -u "$ACCESS_TOKEN" -q 1 \
    -t "legacy/telemetry/$DEVICE" -m "$1"
}

n=0
while :; do
  n=$((n + 1))
  if [ "$KIND" = cold ]; then
    t10=$((40 + (n % 7) * 5))                      # 4.0 .. 7.0 degrees, in tenths
    temp="$((t10 / 10)).$((t10 % 10))"
    hum=$((60 + n % 10))
    door=false; door_n=0; [ $((n % 5)) -eq 0 ] && { door=true; door_n=1; }
    # the previous platform's payload: one flat object, a boolean as JSON true/false, and a text key
    legacy="{\"temperature\":$temp,\"humidity\":$hum,\"battery\":$BATTERY,\"door_open\":$door,\"status_text\":\"ok\"}"
    # DeviceChain's: every reading a JSON string inside payload.entries[].measurements, the boolean
    # as 0 or 1, and NO status_text, because a metric is numeric (see the README)
    m="\"temperature\":\"$temp\",\"humidity\":\"$hum\",\"battery\":\"$BATTERY\",\"door_open\":\"$door_n\""
  else
    flow=$((120 + n % 10)); pres=$((400 + (n % 5) * 10))
    legacy="{\"flow_rate\":$flow,\"pressure\":$pres,\"running\":true}"
    m="\"flow_rate\":\"$flow\",\"pressure\":\"$pres\",\"running\":\"1\""
  fi
  now="$(date -u +%Y-%m-%dT%H:%M:%S.000Z)"
  dc="{\"altId\":\"$DEVICE-$n\",\"occurredTime\":\"$now\",\"device\":\"$DEVICE\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{$m}}]}}"

  targets="$(cat /state/targets 2>/dev/null || echo legacy)"
  case " $targets " in *" legacy "*) legacy_pub "$legacy" && echo "tick $n legacy ok" || echo "tick $n legacy FAILED" ;; esac
  case " $targets " in *" dc "*) dc_pub "$dc" && echo "tick $n dc ok" || echo "tick $n dc FAILED" ;; esac
  sleep "$TICK"
done
