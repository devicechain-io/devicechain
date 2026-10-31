#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Shared helpers for the example's scripts. Source it; do not run it.
#
#   DC_URL     where the instance's ingress is reached (default http://localhost)
#   DC_TENANT  the tenant the example uses (default migration-pilot)
#   DC_ADMIN_PASSWORD   the superuser's password; when unset it is read from the Secret
#              dcctl bootstrap created, using KUBECONFIG / KUBE_CONTEXT
#   INSTANCE   the dcctl instance name (default pilot), which names its namespace dci-<instance>
#   OUT        where the scripts write what they produce (default ./out, git-ignored)

# shellcheck disable=SC2034  # the endpoint variables are used by the scripts that source this file
DC_URL="${DC_URL:-http://localhost}"
DC_TENANT="${DC_TENANT:-migration-pilot}"
INSTANCE="${INSTANCE:-pilot}"
DC_ADMIN_EMAIL="${DC_ADMIN_EMAIL:-superuser@devicechain.local}"

die() { echo "error: $*" >&2; exit 1; }
command -v jq >/dev/null || die "jq is required"

# jq.exe on Windows ends every output line with CRLF, and a "\r" left on a value read into a shell
# variable silently breaks the next lookup. Strip it; a no-op everywhere else. jq's own exit status
# is kept (jq -e depends on it), whether or not the caller set pipefail.
jq() {
  local out rc=0
  out="$(command jq "$@")" || rc=$?
  [ -z "$out" ] || printf '%s\n' "${out//$'\r'/}"
  return "$rc"
}
command -v curl >/dev/null || die "curl is required"

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

USER_GQL="$DC_URL/api/user-management/graphql"
ADMIN_GQL="$DC_URL/api/user-management/admin/graphql"
DM_GQL="$DC_URL/api/device-management/graphql"
EVT_GQL="$DC_URL/api/event-management/graphql"
STATE_GQL="$DC_URL/api/device-state/graphql"

OUT="${OUT:-./out}"
mkdir -p "$OUT"

# admin_password: DC_ADMIN_PASSWORD, else the Secret dcctl bootstrap created.
admin_password() {
  if [ -n "${DC_ADMIN_PASSWORD:-}" ]; then printf '%s' "$DC_ADMIN_PASSWORD"; return; fi
  local -a ctx=()
  [ -z "${KUBE_CONTEXT:-}" ] || ctx=(--context "$KUBE_CONTEXT")
  kubectl "${ctx[@]}" -n "dci-$INSTANCE" get secret "dci-$INSTANCE-superuser" -o jsonpath='{.data.password}' \
    | base64 -d
}

# login: print an identity token for the superuser.
login() {
  local pw r
  pw="$(admin_password)" || die "could not read the superuser password; set DC_ADMIN_PASSWORD"
  r="$(gql "$USER_GQL" "" 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -nc --arg e "$DC_ADMIN_EMAIL" --arg p "$pw" '{e:$e,p:$p}')")" || die "login failed"
  jq -er '.data.login.identityToken' <<<"$r"
}

# tenant_token IDENTITY_TOKEN: print a tenant-scoped access token for $DC_TENANT.
tenant_token() {
  local r
  r="$(gql "$USER_GQL" "" 'mutation($t:String!,$n:String!){selectTenant(identityToken:$t,tenant:$n){accessToken}}' \
    "$(jq -nc --arg t "$1" --arg n "$DC_TENANT" '{t:$t,n:$n}')")" || die "could not enter tenant $DC_TENANT"
  jq -er '.data.selectTenant.accessToken' <<<"$r"
}

# session: sign in once and set TOK to a tenant access token (the tenant must exist).
session() {
  local idt
  idt="$(login)"
  TOK="$(tenant_token "$idt")"
}

# create DOC-URL NAME QUERY VARS: run a create mutation. Prints "created", "exists" or fails.
# CONFLICT means the token is already in use, which is what a re-run is expected to hit.
create() {
  local url=$1 name=$2 out
  if out="$(gql "$url" "$TOK" "$3" "$4" 2>&1 >/dev/null)"; then
    echo created
  elif grep -q CONFLICT <<<"$out"; then
    echo exists
  else
    echo "$out" >&2; die "could not create $name"
  fi
}
