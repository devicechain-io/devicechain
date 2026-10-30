#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Creates what a certificate device needs: a tenant, a device profile that declares one command, a
# device type, the CA as a trust anchor, a device, and the device's certificate pinned to it.
# Safe to run again: anything that exists is left alone.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh
[ -f pki/device.crt ] || die "run ./pki.sh first"

IDT="$(login)"

tier="$(gql "$ADMIN_GQL" "$IDT" '{tenantTiers{token}}' | jq -er '.data.tenantTiers[0].token')"
if out="$(gql "$ADMIN_GQL" "$IDT" 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
     "$(jq -nc --arg t "$DC_TENANT" --arg tier "$tier" '{r:{token:$t,name:"X.509 demo",tierToken:$tier}}')" 2>&1 >/dev/null)"; then
  echo "created tenant $DC_TENANT (tier $tier)"
elif grep -q CONFLICT <<<"$out"; then
  echo "tenant $DC_TENANT already exists"
else
  echo "$out" >&2; die "could not create tenant $DC_TENANT"
fi

TOK="$(tenant_token "$IDT")"

# create NAME QUERY VARS: run a create mutation, treating "already exists" as done.
create() {
  local name=$1 out
  if out="$(gql "$DM_GQL" "$TOK" "$2" "$3" 2>&1 >/dev/null)"; then
    echo "created $name"
  elif grep -q CONFLICT <<<"$out"; then
    echo "$name already exists"
  else
    echo "$out" >&2; die "could not create $name"
  fi
}

create "device profile thermostat" \
  'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
  '{"r":{"token":"thermostat","name":"Thermostat","category":"thermostat"}}'
create "command reboot" \
  'mutation($r:CommandDefinitionCreateRequest){createCommandDefinition(request:$r){token}}' \
  '{"r":{"token":"thermostat-reboot","deviceProfileToken":"thermostat","commandKey":"reboot","name":"Reboot","impact":"NORMAL"}}'

# Devices use the published version of a profile, so publish after the command is declared.
active="$(gql "$DM_GQL" "$TOK" '{deviceProfilesByToken(tokens:["thermostat"]){activeVersion}}' | jq -r '.data.deviceProfilesByToken[0].activeVersion // empty')"
if [ -n "$active" ]; then
  echo "profile thermostat already published (version $active)"
else
  gql "$DM_GQL" "$TOK" 'mutation{publishDeviceProfile(token:"thermostat",label:"reboot command"){version}}' >/dev/null \
    || die "could not publish the profile"
  echo "published profile thermostat"
fi

create "device type thermostat" \
  'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
  '{"r":{"token":"thermostat","name":"Thermostat","profileToken":"thermostat"}}'

# The CA becomes a trust anchor: the tenant now believes certificates this CA signs.
create "trust anchor example-device-ca" \
  'mutation($r:DeviceTrustAnchorCreateRequest!){createDeviceTrustAnchor(request:$r){token fingerprintSha256}}' \
  "$(jq -nc --rawfile pem pki/ca.crt '{r:{token:"example-device-ca",name:"Example device CA",certificatePem:$pem}}')"

# The device, named by the certificate's common name.
CN="$(openssl x509 -in pki/device.crt -noout -subject -nameopt multiline | sed -n 's/^ *commonName *= *//p')"
create "device $DC_DEVICE (external id $CN)" \
  'mutation($r:DeviceCreateRequest!){createDevice(request:$r){token}}' \
  "$(jq -nc --arg d "$DC_DEVICE" --arg cn "$CN" '{r:{token:$d,externalId:$cn,name:"Thermostat",deviceTypeToken:"thermostat"}}')"

# The pin: the certificate itself, tied to the device. credentialId is left empty because the
# server computes it as the certificate's SHA-256.
create "pinned certificate credential" \
  'mutation($r:DeviceCredentialCreateRequest!){createDeviceCredential(request:$r){token credentialId expiresAt}}' \
  "$(jq -nc --arg d "$DC_DEVICE" --rawfile pem pki/device.crt \
     '{r:{token:($d+"-cert"),deviceToken:$d,credentialType:"X509_CERTIFICATE",credentialId:"",enabled:true,certificatePem:$pem,trustAnchorToken:"example-device-ca"}}')"

# The broker presents a certificate the instance's own CA issued; the device needs that CA to trust it.
ctx=(); [ -z "${KUBE_CONTEXT:-}" ] || ctx=(--context "$KUBE_CONTEXT")
kubectl "${ctx[@]}" -n "dci-$INSTANCE" get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > pki/instance-ca.crt
[ -s pki/instance-ca.crt ] || die "could not read the broker CA from secret dc-nats-tls in dci-$INSTANCE"
echo "wrote pki/instance-ca.crt"

echo "ready: tenant $DC_TENANT, device $DC_DEVICE"
