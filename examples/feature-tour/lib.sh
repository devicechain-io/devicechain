#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Shared helpers for the tour's scripts. Source it; do not run it.
# The endpoint and state-file variables are read by the scripts that source this file.
# shellcheck disable=SC2034
#
# A tour is one script set and one SITE FILE of parameters (sites/site-a.env, sites/site-b.env).
# Pass the site file as the first argument of any script, or export SITE. Anything a site file
# sets can be overridden from the environment, which is how the same files run against a
# non-default cluster:
#
#   DC_URL      where the instance's ingress is reached (default http://localhost)
#   INSTANCE    the dcctl instance name, which names its namespace dci-<instance> (default tour)
#   KUBE_CONTEXT  the kubectl context of the cluster (default: the current one)
#   MQTT_HOST   the kind node's container name, <cluster>-control-plane (default devicechain-control-plane)
#   MQTT_PORT   the broker's node port (default 31883)
#   DC_ADMIN_PASSWORD  the superuser's password; when unset it is read from the Secret dcctl
#                      bootstrap created

die() { echo "error: $*" >&2; exit 1; }
command -v jq >/dev/null || die "jq is required"
command -v curl >/dev/null || die "curl is required"

TOUR_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SITE="${1:-${SITE:-}}"
[ -n "$SITE" ] || die "name a site file: $0 sites/site-a.env"
[ -f "$SITE" ] || SITE="$TOUR_DIR/$SITE"
[ -f "$SITE" ] || die "site file not found: ${1:-$SITE}"

# Environment first, then the site file's values for anything not already set.
_keep() { local v; for v in "$@"; do [ -z "${!v+x}" ] || eval "_keep_$v=\${$v}"; done; }
_keep DC_URL INSTANCE KUBE_CONTEXT MQTT_HOST MQTT_PORT
# shellcheck disable=SC1090
. "$SITE"
for _v in DC_URL INSTANCE KUBE_CONTEXT MQTT_HOST MQTT_PORT; do
  _k="_keep_$_v"; [ -z "${!_k+x}" ] || eval "$_v=\${$_k}"
done

DC_URL="${DC_URL:-http://localhost}"
INSTANCE="${INSTANCE:-tour}"
MQTT_HOST="${MQTT_HOST:-devicechain-control-plane}"
MQTT_PORT="${MQTT_PORT:-31883}"
DC_ADMIN_EMAIL="${DC_ADMIN_EMAIL:-superuser@devicechain.local}"

for _v in TOUR_TENANT TOUR_TENANT_NAME TOUR_OPERATOR_EMAIL TOUR_CUSTOMER TOUR_CUSTOMER_NAME TOUR_CUSTOMER_EMAIL \
          TOUR_DEVICE_1 TOUR_DEVICE_2 TOUR_ALARM_ABOVE TOUR_START_C TOUR_TARGET_C; do
  [ -n "${!_v:-}" ] || die "$SITE does not set $_v"
done

# Per-site state (device, operator and customer passwords): git-ignored, mode 0600, never printed.
STATE_DIR="$TOUR_DIR/.state/$TOUR_TENANT"
mkdir -p "$STATE_DIR"

USER_GQL="$DC_URL/api/user-management/graphql"
ADMIN_GQL="$DC_URL/api/user-management/admin/graphql"
DM_GQL="$DC_URL/api/device-management/graphql"
CMD_GQL="$DC_URL/api/command-delivery/graphql"
DS_GQL="$DC_URL/api/device-state/graphql"
DASH_GQL="$DC_URL/api/dashboard-management/graphql"

# gql URL TOKEN QUERY [VARIABLES-JSON]: POST a GraphQL document and print the response.
# Returns non-zero, with the error on stderr, when the response carries GraphQL errors.
gql() {
  local url=$1 tok=$2 query=$3 vars=${4:-"{}"} body resp
  local -a auth=()
  [ -z "$tok" ] || auth=(-H "Authorization: Bearer $tok")
  body="$(jq -nc --arg q "$query" --argjson v "$vars" '{query:$q,variables:$v}')"
  resp="$(curl -sS -m 60 -H 'Content-Type: application/json' "${auth[@]}" --data-binary "$body" "$url")" \
    || die "could not reach $url"
  if jq -e '.errors' >/dev/null 2>&1 <<<"$resp"; then
    jq -r '"graphql error: " + (.errors[0].extensions.code // "-") + ": " + .errors[0].message' <<<"$resp" >&2
    return 1
  fi
  printf '%s' "$resp"
}

kc() {
  local -a ctx=()
  [ -z "${KUBE_CONTEXT:-}" ] || ctx=(--context "$KUBE_CONTEXT")
  kubectl "${ctx[@]}" "$@"
}

# create NAME URL TOKEN QUERY VARS: run a create mutation, treating "already exists" as done.
create() {
  local name=$1 out
  if out="$(gql "$2" "$3" "$4" "$5" 2>&1 >/dev/null)"; then
    echo "created $name"
  elif grep -q CONFLICT <<<"$out"; then
    echo "$name already exists"
  else
    echo "$out" >&2; die "could not create $name"
  fi
}

admin_password() {
  if [ -n "${DC_ADMIN_PASSWORD:-}" ]; then printf '%s' "$DC_ADMIN_PASSWORD"; return; fi
  kc -n "dci-$INSTANCE" get secret "dci-$INSTANCE-superuser" -o jsonpath='{.data.password}' | base64 -d
}

# login EMAIL PASSWORD: print an identity token.
login() {
  local r
  r="$(gql "$USER_GQL" "" 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -nc --arg e "$1" --arg p "$2" '{e:$e,p:$p}')")" || return 1
  jq -er '.data.login.identityToken' <<<"$r"
}

# select_tenant IDENTITY_TOKEN: print a tenant-scoped access token for $TOUR_TENANT.
select_tenant() {
  local r
  r="$(gql "$USER_GQL" "" 'mutation($t:String!,$n:String!){selectTenant(identityToken:$t,tenant:$n){accessToken}}' \
    "$(jq -nc --arg t "$1" --arg n "$TOUR_TENANT" '{t:$t,n:$n}')")" || return 1
  jq -er '.data.selectTenant.accessToken' <<<"$r"
}

# admin_identity_token: the superuser's identity token (instance-level work: creating tenants).
admin_identity_token() { login "$DC_ADMIN_EMAIL" "$(admin_password)" || die "superuser login failed"; }

# operator_token: a tenant access token for the site's operator, a tenant administrator who exists
# only in this tenant (01-provision.sh creates it). Everything after the tenant itself is created
# runs as this person, the same way you would sign in to the console.
operator_token() {
  local idt
  idt="$(login "$TOUR_OPERATOR_EMAIL" "$(operator_password)")" || die "the operator $TOUR_OPERATOR_EMAIL cannot sign in; run ./01-provision.sh"
  select_tenant "$idt" || die "could not enter tenant $TOUR_TENANT"
}

# secret_file NAME BYTES: a random secret kept in the site's state dir (0600), created once.
secret_file() {
  local f="$STATE_DIR/$1"
  if [ ! -s "$f" ]; then ( umask 077; openssl rand -hex "${2:-16}" > "$f" ); fi
  printf '%s' "$(cat "$f")"
}

CUSTOMER_PASSWORD_FILE="$STATE_DIR/customer-password"

# The customer user's login: the email is a parameter; the password is generated once into the state dir.
customer_password() { secret_file customer-password 12; }
operator_password() { secret_file operator-password 12; }
device_password() { secret_file device-password 16; }
