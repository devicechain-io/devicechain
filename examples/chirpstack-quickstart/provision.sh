#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The DeviceChain side of the integration: a tenant, a device type whose profile declares the three
# decoded values with their units, an inbound ChirpStack integration scoped to that type, and one
# device that carries the first sensor's DevEUI as an alias. The second sensor is deliberately
# not created, so it shows up under "unregistered devices". Safe to run again.
#
# The integration's bearer token is shown once, by the server. It is written to
# .integration-token (mode 0600) for ./connect-chirpstack.sh, and nowhere else.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

IDT="$(login)"

tier="$(gql "$ADMIN_GQL" "$IDT" '{tenantTiers{token}}' | jq -er '.data.tenantTiers[0].token')"
if out="$(gql "$ADMIN_GQL" "$IDT" 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
     "$(jq -nc --arg t "$DC_TENANT" --arg tier "$tier" '{r:{token:$t,name:"LoRaWAN demo",tierToken:$tier}}')" 2>&1 >/dev/null)"; then
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

create "device profile lora-sensor" \
  'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
  '{"r":{"token":"lora-sensor","name":"LoRaWAN sensor","category":"sensor"}}'

# One metric per key the ChirpStack codec produces. Keys that are not declared are still stored,
# just without a unit.
metric() {
  create "metric $1" \
    'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){token}}' \
    "$(jq -nc --arg k "$1" --arg u "$2" --arg n "$3" \
       '{r:{token:("lora-sensor-"+$k),deviceProfileToken:"lora-sensor",metricKey:$k,name:$n,dataType:"DOUBLE",unit:$u}}')"
}
metric temperature Cel Temperature
metric humidity % Humidity
metric battery V Battery

# Devices use the published version of a profile, so publish after the metrics are declared.
active="$(gql "$DM_GQL" "$TOK" '{deviceProfilesByToken(tokens:["lora-sensor"]){activeVersion}}' | jq -r '.data.deviceProfilesByToken[0].activeVersion // empty')"
if [ -n "$active" ]; then
  echo "profile lora-sensor already published (version $active)"
else
  gql "$DM_GQL" "$TOK" 'mutation{publishDeviceProfile(token:"lora-sensor",label:"metrics"){version}}' >/dev/null \
    || die "could not publish the profile"
  echo "published profile lora-sensor"
fi

create "device type lora-sensor" \
  'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
  '{"r":{"token":"lora-sensor","name":"LoRaWAN sensor","profileToken":"lora-sensor"}}'

# The first sensor, known by its DevEUI.
create "device boiler-room-1 (DevEUI 70B3D57ED0012345)" \
  'mutation($r:DeviceCreateRequest!){createDevice(request:$r){token}}' \
  '{"r":{"token":"boiler-room-1","name":"Boiler room 1","deviceTypeToken":"lora-sensor","aliases":[{"scheme":"lorawan.deveui","value":"70b3d57ed0012345"}]}}'

# The integration. Its token comes back once, so a re-run that finds it existing cannot recover it.
if [ "$(gql "$DM_GQL" "$TOK" '{integration(token:"chirpstack"){token}}' | jq -r '.data.integration.token // empty')" = chirpstack ]; then
  [ -s .integration-token ] || die "integration chirpstack exists but .integration-token is gone: rotate it with mutation{rotateIntegrationToken(token:\"chirpstack\"){issuedToken}} and save the result"
  echo "integration chirpstack already exists (token in .integration-token)"
else
  res="$(gql "$DM_GQL" "$TOK" 'mutation($r:IntegrationCreateRequest!){createIntegration(request:$r){integration{token type identity} issuedToken}}' \
    '{"r":{"token":"chirpstack","name":"ChirpStack","type":"CHIRPSTACK_V4","enabled":true,"deviceTypeTokens":["lora-sensor"],"identity":"DEV_EUI","linkMetrics":true}}')" \
    || die "could not create the integration (to start over, delete it: mutation{deleteIntegration(token:\"chirpstack\")})"
  umask 077
  jq -er '.data.createIntegration.issuedToken' <<<"$res" > .integration-token
  echo "created integration chirpstack; its bearer token is in .integration-token"
fi

echo "ready: tenant $DC_TENANT, integration URL path /api/integrations/$DC_TENANT/chirpstack"
