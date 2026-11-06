#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Everything the walkthrough builds on the DeviceChain side: a tenant; a "room-sensor" device
# profile that declares four metrics, carries the payload decoder (decoder.json) and the detection
# rule (rule.json), and is PUBLISHED; a device type; a device that carries the sensor's DevEUI as an
# alias; and two inbound integrations (ChirpStack v4 and The Things Stack v3) scoped to that type.
# Safe to run again: anything that exists is left alone.
#
# The integrations' bearer tokens are shown once, by the server. They are written to
# .token-chirpstack and .token-tts (mode 0600, git-ignored) for ./run-simulator.sh.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

IDT="$(login)"

tier="$(gql "$ADMIN_GQL" "$IDT" '{tenantTiers{token}}' | jq -er '.data.tenantTiers[0].token')"
if out="$(gql "$ADMIN_GQL" "$IDT" 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
     "$(jq -nc --arg t "$DC_TENANT" --arg tier "$tier" '{r:{token:$t,name:"LoRaWAN uplink to alarm",tierToken:$tier}}')" 2>&1 >/dev/null)"; then
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

create "device profile room-sensor" \
  'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
  '{"r":{"token":"room-sensor","name":"Room sensor","category":"sensor"}}'

# One metric per key the decoder writes. The decoder's rows are checked against these.
metric() { # KEY TYPE UNIT NAME
  create "metric $1" \
    'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){token}}' \
    "$(jq -nc --arg k "$1" --arg t "$2" --arg u "$3" --arg n "$4" \
       '{r:({token:("room-sensor-"+($k|gsub("[.]";"-"))),deviceProfileToken:"room-sensor",metricKey:$k,name:$n,dataType:$t} + (if $u == "" then {} else {unit:$u} end))}')"
}
metric temperature DOUBLE Cel Temperature
metric humidity DOUBLE % Humidity
metric battery.voltage DOUBLE V "Battery voltage"
metric door BOOLEAN "" "Door open"

# The decoder goes on the profile's draft. Publishing freezes it into the version, and refuses the
# publish if it does not compile against the metrics or its saved vector fails.
active="$(gql "$DM_GQL" "$TOK" '{deviceProfilesByToken(tokens:["room-sensor"]){activeVersion}}' | jq -r '.data.deviceProfilesByToken[0].activeVersion // empty')"
if [ -n "$active" ]; then
  echo "profile room-sensor already published (version $active); decoder and rule left as they are"
else
  gql "$DM_GQL" "$TOK" 'mutation($t:String!,$d:String!){setDeviceProfileDecoder(profileToken:$t,document:$d){token}}' \
    "$(jq -nc --rawfile d decoder.json '{t:"room-sensor",d:$d}')" >/dev/null \
    || die "could not save the decoder (the compiler's reason is above)"
  echo "saved the decoder on the draft of room-sensor"

  rule="$(jq -c . rule.json)"
  create "detection rule room-too-warm" \
    'mutation($r:DetectionRuleCreateRequest!){createDetectionRule(request:$r){token}}' \
    "$(jq -nc --arg d "$rule" '{r:{token:"room-too-warm",deviceProfileToken:"room-sensor",name:"Room too warm",enabled:true,definition:$d}}')"

  gql "$DM_GQL" "$TOK" 'mutation{publishDeviceProfile(token:"room-sensor",label:"decoder and rule"){version}}' >/dev/null \
    || die "could not publish the profile (the compiler's reason is above)"
  echo "published profile room-sensor"
fi

create "device type room-sensor" \
  'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
  '{"r":{"token":"room-sensor","name":"Room sensor","profileToken":"room-sensor"}}'

create "device $DC_DEVICE (DevEUI $DC_DEVEUI)" \
  'mutation($r:DeviceCreateRequest!){createDevice(request:$r){token}}' \
  "$(jq -nc --arg d "$DC_DEVICE" --arg e "$DC_DEVEUI" '{r:{token:$d,name:"Room 4",deviceTypeToken:"room-sensor",aliases:[{scheme:"lorawan.deveui",value:$e}]}}')"

# integration TOKEN TYPE: create it and keep its bearer token. The token comes back once, so a
# re-run that finds the integration existing cannot recover it.
integration() {
  local token=$1 type=$2 file=".token-$1" found res
  found="$(gql "$DM_GQL" "$TOK" 'query($t:String!){integration(token:$t){token}}' "$(jq -nc --arg t "$token" '{t:$t}')" \
    | jq -r '.data.integration.token // empty')"
  if [ "$found" = "$token" ]; then
    [ -s "$file" ] || die "integration $token exists but $file is gone: rotate it with mutation{rotateIntegrationToken(token:\"$token\"){issuedToken}} and save the result"
    echo "integration $token already exists (token in $file)"
    return
  fi
  res="$(gql "$DM_GQL" "$TOK" 'mutation($r:IntegrationCreateRequest!){createIntegration(request:$r){integration{token type identity} issuedToken}}' \
    "$(jq -nc --arg t "$token" --arg y "$type" '{r:{token:$t,name:$t,type:$y,enabled:true,deviceTypeTokens:["room-sensor"],identity:"DEV_EUI",linkMetrics:true}}')")" \
    || die "could not create integration $token (to start over, delete it: mutation{deleteIntegration(token:\"$token\")})"
  ( umask 077; jq -er '.data.createIntegration.issuedToken' <<<"$res" > "$file" )
  echo "created integration $token ($type); its bearer token is in $file"
}
integration chirpstack CHIRPSTACK_V4
integration tts TTS_V3

echo "ready: tenant $DC_TENANT, URL path /api/integrations/$DC_TENANT/{chirpstack,tts}"
