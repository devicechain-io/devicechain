#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Creates what DeviceChain needs before the Sparkplug source connects: a tenant, and a device
# profile that declares the commands the example sends, published, with a device type that
# uses it. Safe to run again: anything that exists is left alone.
set -euo pipefail
cd "$(dirname "$0")"
. ./lib.sh

IDT="$(login)"

# The tenant. A fresh instance has none; the superuser may enter any tenant it creates.
tier="$(gql "$ADMIN_GQL" "$IDT" '{tenantTiers{token}}' | jq -er '.data.tenantTiers[0].token')"
if out="$(gql "$ADMIN_GQL" "$IDT" 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
     "$(jq -nc --arg t "$DC_TENANT" --arg tier "$tier" '{r:{token:$t,name:"Sparkplug demo",tierToken:$tier}}')" 2>&1 >/dev/null)"; then
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

create "device profile pump" \
  'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
  '{"r":{"token":"pump","name":"Pump","category":"pump"}}'

# The Sparkplug command keys. The console's "Add Sparkplug standard commands" button declares
# the same ones, plus reboot, which this example leaves off.
create "command sparkplug.write" \
  'mutation($r:CommandDefinitionCreateRequest){createCommandDefinition(request:$r){token}}' \
  '{"r":{"token":"pump-sparkplug-write","deviceProfileToken":"pump","commandKey":"sparkplug.write","name":"Write metric","impact":"NORMAL"}}'
create "command sparkplug.rebirth" \
  'mutation($r:CommandDefinitionCreateRequest){createCommandDefinition(request:$r){token}}' \
  '{"r":{"token":"pump-sparkplug-rebirth","deviceProfileToken":"pump","commandKey":"sparkplug.rebirth","name":"Rebirth","impact":"NORMAL"}}'

# Devices use the published version of a profile, so publish after the commands are declared.
active="$(gql "$DM_GQL" "$TOK" '{deviceProfilesByToken(tokens:["pump"]){activeVersion}}' | jq -r '.data.deviceProfilesByToken[0].activeVersion // empty')"
if [ -n "$active" ]; then
  echo "profile pump already published (version $active)"
else
  gql "$DM_GQL" "$TOK" 'mutation{publishDeviceProfile(token:"pump",label:"sparkplug commands"){version}}' >/dev/null \
    || die "could not publish the profile"
  echo "published profile pump"
fi

create "device type sparkplug-pump" \
  'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
  '{"r":{"token":"sparkplug-pump","name":"Sparkplug pump","profileToken":"pump"}}'

echo "ready: tenant $DC_TENANT, device type sparkplug-pump"
