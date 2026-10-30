#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The negative control: a certificate from a CA the tenant has NOT anchored, carrying the real
# device's common name. It must be refused at CONNACK. Nothing is stored and nothing is created.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh
export MSYS_NO_PATHCONV=1
IMAGE="eclipse-mosquitto:2.0.22@sha256:09b62ff32c2e7b64d4b431b9a89236073289b68db273de2b089edeb9b0d92b5c"
MQTT_HOST="${MQTT_HOST:-devicechain-control-plane}"
MQTT_PORT="${MQTT_PORT:-31883}"
[ -f pki/instance-ca.crt ] || die "run ./pki.sh and ./provision.sh first"

mkdir -p pki/rogue
cd pki/rogue
openssl ecparam -name prime256v1 -genkey -noout -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 30 -subj "/CN=Rogue CA" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign" -out ca.crt
openssl ecparam -name prime256v1 -genkey -noout -out device.key
openssl req -new -key device.key -subj "/CN=SN-000123" -out device.csr
printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=clientAuth\n' > ext
openssl x509 -req -in device.csr -CA ca.crt -CAkey ca.key -CAcreateserial -sha256 -days 30 \
  -extfile ext -out device.crt 2>/dev/null
cd ../..

set +e
docker run --rm --network kind -v "$PWD/pki:/pki:ro" --entrypoint mosquitto_pub "$IMAGE" \
  -h "$MQTT_HOST" -p "$MQTT_PORT" -V mqttv311 --cafile /pki/instance-ca.crt --insecure \
  --cert /pki/rogue/device.crt --key /pki/rogue/device.key \
  -i "$INSTANCE:$DC_TENANT:$DC_DEVICE" -u "$DC_TENANT:@x509" -q 1 \
  -t "$INSTANCE/$DC_TENANT/devices/$DC_DEVICE/events" \
  -m "{\"device\":\"$DC_DEVICE\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"999\"}}]}}"
rc=$?
set -e
if [ $rc -eq 0 ]; then echo "UNEXPECTED: the broker accepted a certificate from an unanchored CA"; exit 1; fi
echo "refused as expected (mosquitto_pub exit $rc)"
