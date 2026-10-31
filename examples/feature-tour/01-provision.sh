#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Chapters 1-4 of the tour: the tenant, the device type and its profile (metrics, a command, one
# detection rule, published), two devices with MQTT credentials, a customer, and the assignment of
# the first device to that customer. Everything comes from the site file.
#
# Safe to run again: anything that exists is left alone.
#
#   ./01-provision.sh sites/site-a.env
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

IDT="$(admin_identity_token)"

# 1. The tenant (instance-level administration: the superuser, the instance admin API).
tier="$(gql "$ADMIN_GQL" "$IDT" '{tenantTiers{token}}' | jq -er '.data.tenantTiers[0].token')"
if out="$(gql "$ADMIN_GQL" "$IDT" 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
     "$(jq -nc --arg t "$TOUR_TENANT" --arg n "$TOUR_TENANT_NAME" --arg tier "$tier" '{r:{token:$t,name:$n,tierToken:$tier}}')" 2>&1 >/dev/null)"; then
  echo "created tenant $TOUR_TENANT (tier $tier)"
elif grep -q CONFLICT <<<"$out"; then
  echo "tenant $TOUR_TENANT already exists"
else
  echo "$out" >&2; die "could not create tenant $TOUR_TENANT"
fi

# The tenant's own administrator: an identity that is a member of this tenant only, with the
# tenant-admin role. This is who signs in to the console for the rest of the tour, and who runs the
# rest of this script. (The superuser's console is the instance admin console; it has no tenant
# pages.) `dcctl sim create` makes the same kind of identity.
if out="$(gql "$ADMIN_GQL" "$IDT" 'mutation($e:String!,$p:String!){createIdentity(request:{email:$e,password:$p,enabled:true,systemRoles:[]}){email}}' \
     "$(jq -nc --arg e "$TOUR_OPERATOR_EMAIL" --arg p "$(operator_password)" '{e:$e,p:$p}')" 2>&1 >/dev/null)"; then
  echo "created operator identity $TOUR_OPERATOR_EMAIL (password in $STATE_DIR/operator-password)"
elif grep -q CONFLICT <<<"$out"; then
  echo "operator identity $TOUR_OPERATOR_EMAIL already exists"
else
  echo "$out" >&2; die "could not create the operator identity"
fi
if out="$(gql "$ADMIN_GQL" "$IDT" 'mutation($e:String!,$t:String!){addMembership(email:$e,tenant:$t,roleTokens:["tenant-admin"]){email}}' \
     "$(jq -nc --arg e "$TOUR_OPERATOR_EMAIL" --arg t "$TOUR_TENANT" '{e:$e,t:$t}')" 2>&1 >/dev/null)"; then
  echo "made $TOUR_OPERATOR_EMAIL a tenant-admin of $TOUR_TENANT"
elif grep -q CONFLICT <<<"$out"; then
  echo "$TOUR_OPERATOR_EMAIL is already a member of $TOUR_TENANT"
else
  echo "$out" >&2; die "could not add the operator to the tenant"
fi

TOK="$(operator_token)"
dm() { create "$1" "$DM_GQL" "$TOK" "$2" "$3"; }

# 2. The profile: what a cold room reports and what it can be told. A profile that declares
#    commands accepts only those.
dm "device profile cold-room" \
  'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
  '{"r":{"token":"cold-room","name":"Cold room","category":"cold-room"}}'
dm "metric temperature" \
  'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){token}}' \
  '{"r":{"token":"cold-room-temperature","deviceProfileToken":"cold-room","metricKey":"temperature","name":"Temperature","dataType":"DOUBLE","unit":"Cel"}}'
dm "metric humidity" \
  'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){token}}' \
  '{"r":{"token":"cold-room-humidity","deviceProfileToken":"cold-room","metricKey":"humidity","name":"Humidity","dataType":"DOUBLE","unit":"%"}}'
dm "command boost-cooling" \
  'mutation($r:CommandDefinitionCreateRequest){createCommandDefinition(request:$r){token}}' \
  '{"r":{"token":"cold-room-boost-cooling","deviceProfileToken":"cold-room","commandKey":"boost-cooling","name":"Boost cooling","impact":"NORMAL"}}'

# 3. The rule, as the console's form would save it: above the site's line, raise an alarm AND send
#    boost-cooling. Edge-triggered: the alarm clears by itself when the reading drops back.
rule="$(jq -c --argjson above "$TOUR_ALARM_ABOVE" --argjson target "$TOUR_TARGET_C" \
  '.when.threshold = $above | .actions[1].sendCommand.payload = ({targetC:$target}|tojson)' rule.json)"
dm "detection rule cold-room-too-warm" \
  'mutation($r:DetectionRuleCreateRequest!){createDetectionRule(request:$r){token}}' \
  "$(jq -nc --arg d "$rule" '{r:{token:"cold-room-too-warm",deviceProfileToken:"cold-room",name:"Cold room too warm",enabled:true,definition:$d}}')"

# Rules reach live telemetry only when the profile is published; the compiler checks the rule then.
active="$(gql "$DM_GQL" "$TOK" '{deviceProfilesByToken(tokens:["cold-room"]){activeVersion}}' | jq -r '.data.deviceProfilesByToken[0].activeVersion // empty')"
if [ -n "$active" ]; then
  echo "profile cold-room already published (version $active)"
else
  gql "$DM_GQL" "$TOK" 'mutation{publishDeviceProfile(token:"cold-room",label:"tour"){version}}' >/dev/null \
    || die "could not publish the profile (the compiler's reason is above)"
  echo "published profile cold-room"
fi

# 4. The device type and the two devices, each with an MQTT login.
dm "device type cold-room" \
  'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
  '{"r":{"token":"cold-room","name":"Cold room","profileToken":"cold-room"}}'
pw="$(device_password)"
for d in "$TOUR_DEVICE_1" "$TOUR_DEVICE_2"; do
  dm "device $d" \
    'mutation($r:DeviceCreateRequest!){createDevice(request:$r){token}}' \
    "$(jq -nc --arg d "$d" '{r:{token:$d,name:$d,deviceTypeToken:"cold-room"}}')"
  # MQTT login: user "<tenant>:<credentialId>", password of at least 16 bytes (kept in .state, never printed).
  dm "MQTT credential for $d" \
    'mutation($r:DeviceCredentialCreateRequest!){createDeviceCredential(request:$r){token}}' \
    "$(jq -nc --arg d "$d" --arg p "$pw" '{r:{token:($d+"-mqtt"),deviceToken:$d,credentialType:"MQTT_BASIC",credentialId:$d,credentialValue:$p,enabled:true}}')"
done

# 5. The customer, and the assignment that makes device 1 theirs.
dm "customer type" \
  'mutation($r:CustomerTypeCreateRequest){createCustomerType(request:$r){token}}' \
  '{"r":{"token":"company","name":"Company"}}'
dm "customer $TOUR_CUSTOMER" \
  'mutation($r:CustomerCreateRequest){createCustomer(request:$r){token}}' \
  "$(jq -nc --arg c "$TOUR_CUSTOMER" --arg n "$TOUR_CUSTOMER_NAME" '{r:{token:$c,name:$n,customerTypeToken:"company"}}')"
dm "assignment $TOUR_DEVICE_1 -> $TOUR_CUSTOMER" \
  'mutation($r:EntityRelationshipCreateRequest!){createEntityRelationship(request:$r){token}}' \
  "$(jq -nc --arg d "$TOUR_DEVICE_1" --arg c "$TOUR_CUSTOMER" \
     '{r:{token:($d+"-assigned-"+$c),sourceType:"device",source:$d,targetType:"customer",target:$c,relationshipType:"assigned"}}')"

# 6. The broker's CA: the device verifies the broker against it.
mkdir -p pki
kc -n "dci-$INSTANCE" get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > pki/instance-ca.crt
[ -s pki/instance-ca.crt ] || die "could not read the broker CA from secret dc-nats-tls in dci-$INSTANCE"

echo "ready: tenant $TOUR_TENANT, devices $TOUR_DEVICE_1 (customer $TOUR_CUSTOMER) and $TOUR_DEVICE_2"
