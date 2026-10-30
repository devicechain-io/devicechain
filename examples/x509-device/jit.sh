#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Register-on-first-connect. A certificate profile on the trust anchor lets a device that does not
# exist yet appear the first time it connects with a certificate the CA issued. This script
# creates the profile, mints a second certificate for a device nobody registered (CN SN-000456),
# connects once, and shows the device the platform created.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh
[ -f pki/ca.key ] || die "run ./pki.sh and ./provision.sh first"
export MSYS_NO_PATHCONV=1
CN="${JIT_CN:-SN-000456}"
IMAGE="eclipse-mosquitto:2.0.22@sha256:09b62ff32c2e7b64d4b431b9a89236073289b68db273de2b089edeb9b0d92b5c"
MQTT_HOST="${MQTT_HOST:-devicechain-control-plane}"
MQTT_PORT="${MQTT_PORT:-31883}"

TOK="$(tenant_token "$(login)")"

# 1. The certificate profile: ALLOW_NEW on the anchor, capped so a mistake has a ceiling.
if out="$(gql "$DM_GQL" "$TOK" 'mutation($r:ProvisioningProfileCreateRequest!){createProvisioningProfile(request:$r){profile{token}}}' \
    '{"r":{"token":"example-certs","provisionKey":"example-certs","strategy":"ALLOW_NEW","deviceTypeToken":"thermostat","enabled":true,"maxDevices":50,"trustAnchorToken":"example-device-ca"}}' 2>&1 >/dev/null)"; then
  echo "created certificate profile example-certs"
elif grep -q CONFLICT <<<"$out"; then echo "certificate profile example-certs already exists"
else echo "$out" >&2; die "could not create the profile"; fi

# 2. A certificate for a device the platform has never heard of.
mkdir -p pki/jit
openssl ecparam -name prime256v1 -genkey -noout -out "pki/jit/$CN.key"
openssl req -new -key "pki/jit/$CN.key" -subj "/CN=$CN" -out "pki/jit/$CN.csr"
printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=clientAuth\n' > pki/jit/ext
openssl x509 -req -in "pki/jit/$CN.csr" -CA pki/ca.crt -CAkey pki/ca.key -CAcreateserial \
  -sha256 -days 90 -extfile pki/jit/ext -out "pki/jit/$CN.crt" 2>/dev/null

# 3. The device derives its own token from the common name: pv-<slug>-<first 12 hex of sha256>.
slug=$(printf '%s' "$CN" | sed -E 's/[^A-Za-z0-9_-]/-/g; s/^[-_]+//; s/[-_]+$//')
hash=$(printf '%s' "$CN" | openssl dgst -sha256 -hex | sed 's/^.*= //' | cut -c1-12)
dev="pv-$slug-$hash"
echo "connecting as $dev"

# A profile created a moment ago can take a few seconds to reach the broker's auth callout, and a
# refused first connect is exactly what a device sees until then. A real device retries; so does this.
for attempt in 1 2 3 4 5 6; do
  if docker run --rm --network kind -v "$PWD/pki:/pki:ro" --entrypoint mosquitto_pub "$IMAGE"     -h "$MQTT_HOST" -p "$MQTT_PORT" -V mqttv311 --cafile /pki/instance-ca.crt --insecure     --cert "/pki/jit/$CN.crt" --key "/pki/jit/$CN.key"     -i "$INSTANCE:$DC_TENANT:$dev" -u "$DC_TENANT:@x509" -q 1     -t "$INSTANCE/$DC_TENANT/devices/$dev/events"     -m "{\"device\":\"$dev\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"18.5\"}}]}}"; then
    break
  fi
  echo "refused (attempt $attempt); retrying in 5s"
  [ "$attempt" -lt 6 ] || die "the platform kept refusing the certificate"
  sleep 5
done

gql "$DM_GQL" "$TOK" 'query($t:[String!]!){devicesByToken(tokens:$t){token externalId}}' \
  "$(jq -nc --arg t "$dev" '{t:[$t]}')" | jq -r '.data.devicesByToken[] | "registered: \(.token)  external id \(.externalId)"'
