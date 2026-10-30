#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Creates what the loop needs: a tenant, a "furnace" device profile that declares the temperature
# metric, the cool-down command and ONE detection rule, a device type, and a device with an MQTT
# password credential. Also saves the broker's CA and the device's password for docker compose.
# Safe to run again: anything that exists is left alone.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

IDT="$(login)"

tier="$(gql "$ADMIN_GQL" "$IDT" '{tenantTiers{token}}' | jq -er '.data.tenantTiers[0].token')"
if out="$(gql "$ADMIN_GQL" "$IDT" 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
     "$(jq -nc --arg t "$DC_TENANT" --arg tier "$tier" '{r:{token:$t,name:"Rules demo",tierToken:$tier}}')" 2>&1 >/dev/null)"; then
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

create "device profile furnace" \
  'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
  '{"r":{"token":"furnace","name":"Furnace","category":"furnace"}}'

create "metric temperature" \
  'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){token}}' \
  '{"r":{"token":"furnace-temperature","deviceProfileToken":"furnace","metricKey":"temperature","name":"Temperature","dataType":"DOUBLE","unit":"Cel"}}'

# The command the rule will send. A profile that declares commands accepts only those.
create "command cool-down" \
  'mutation($r:CommandDefinitionCreateRequest){createCommandDefinition(request:$r){token}}' \
  '{"r":{"token":"furnace-cool-down","deviceProfileToken":"furnace","commandKey":"cool-down","name":"Cool down","impact":"NORMAL"}}'

# The rule, as the console's form would save it: when temperature exceeds 80, raise a critical
# alarm AND send cool-down. The rule is edge-triggered, so the alarm clears by itself when the
# reading drops back to 80 or below; nothing authors the clear.
rule="$(jq -c . rule.json)"
create "detection rule furnace-overheat" \
  'mutation($r:DetectionRuleCreateRequest!){createDetectionRule(request:$r){token}}' \
  "$(jq -nc --arg d "$rule" '{r:{token:"furnace-overheat",deviceProfileToken:"furnace",name:"Furnace over temperature",enabled:true,definition:$d}}')"

# Rules reach live telemetry only when the profile is published.
active="$(gql "$DM_GQL" "$TOK" '{deviceProfilesByToken(tokens:["furnace"]){activeVersion}}' | jq -r '.data.deviceProfilesByToken[0].activeVersion // empty')"
if [ -n "$active" ]; then
  echo "profile furnace already published (version $active)"
else
  gql "$DM_GQL" "$TOK" 'mutation{publishDeviceProfile(token:"furnace",label:"overheat rule"){version}}' >/dev/null \
    || die "could not publish the profile (the compiler's reason is above)"
  echo "published profile furnace"
fi

create "device type furnace" \
  'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
  '{"r":{"token":"furnace","name":"Furnace","profileToken":"furnace"}}'
create "device $DC_DEVICE" \
  'mutation($r:DeviceCreateRequest!){createDevice(request:$r){token}}' \
  "$(jq -nc --arg d "$DC_DEVICE" '{r:{token:$d,name:"Furnace 1",deviceTypeToken:"furnace"}}')"

# The device's MQTT login: user "<tenant>:<credentialId>" and a password of at least 16 bytes.
# The password is generated once, kept in .env (mode 0600) and never printed.
if [ ! -f .env ]; then
  umask 077
  echo "DEVICE_PASSWORD=$(openssl rand -hex 16)" > .env
fi
# shellcheck disable=SC1091
. ./.env
create "MQTT credential for $DC_DEVICE" \
  'mutation($r:DeviceCredentialCreateRequest!){createDeviceCredential(request:$r){token}}' \
  "$(jq -nc --arg d "$DC_DEVICE" --arg p "$DEVICE_PASSWORD" \
     '{r:{token:($d+"-mqtt"),deviceToken:$d,credentialType:"MQTT_BASIC",credentialId:$d,credentialValue:$p,enabled:true}}')"

# The broker presents a certificate the instance's own CA issued; the device needs that CA.
mkdir -p pki
ctx=(); [ -z "${KUBE_CONTEXT:-}" ] || ctx=(--context "$KUBE_CONTEXT")
kubectl "${ctx[@]}" -n "dci-$INSTANCE" get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > pki/instance-ca.crt
[ -s pki/instance-ca.crt ] || die "could not read the broker CA from secret dc-nats-tls in dci-$INSTANCE"

echo "ready: tenant $DC_TENANT, device $DC_DEVICE"
