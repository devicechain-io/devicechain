#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# End-to-end rig for inbound integrations: a LoRaWAN network server (the dc-simulator in
# integration mode) posting to the PUBLIC route of a real instance on a local kind cluster.
#
#   hack/integrations-rig.sh up      # build images from this tree, bootstrap a throwaway instance
#   hack/integrations-rig.sh test    # create the integration + devices, drive the simulator, assert
#   hack/integrations-rig.sh down    # destroy the instance (the cluster stays)
#   hack/integrations-rig.sh all     # up, test, down (down runs even when test fails)
#
# WHAT IT PROVES, through the ingress and across every service, not against a unit double:
#
#   1. ChirpStack v4 and The Things Stack v3 uplinks reach the stored measurements of the device
#      that carries the DevEUI alias (written in lower case, stored and matched upper case), with
#      the declared metric's unit, nested keys flattened to dot paths and a boolean stored as 1/0.
#   2. Link metrics (lorawan.rssi, lorawan.snr, lorawan.gateways) are stored beside them.
#   3. A resend of every uplink (a network server retrying) leaves the stored count EXACTLY as it
#      was: one event per uplink, not two.
#   4. A DevEUI no device carries is listed as an unregistered sighting, not dropped silently and
#      not stored; clearing the list empties it.
#   5. A wrong bearer token is answered 401 and stores nothing; a message type that is not an
#      uplink is acknowledged 204 and stores nothing.
#
# NEGATIVE CONTROL. A check that cannot fail proves nothing, so before the assertions the rig
# disables the integration and shows the same simulator run is then refused (403) and adds
# nothing to the stored count. The assertions above are only worth anything because that
# refusal is visible in the same counters.
#
# THE CLUSTER. It reuses the kind cluster `dcctl install local` prepared (DC_INT_CLUSTER, default
# devicechain) and puts its own instance, tenant and ingress host (DC_INT_HOST) beside whatever else
# lives there. It never switches the kubectl current-context: every kubectl call names its
# context, and a caller that wants a private view exports its own KUBECONFIG first. `down` removes
# only the instance it made, and only when the ownership marker `up` wrote says so.
#
# Requires: kind, kubectl, docker, jq, curl, a Go toolchain, ko, and `tofu` or `terraform`.
# It needs the cluster's ingress on a host port (DC_INT_INGRESS_PORT, default 80, the kind config's mapping) and `*.localhost`
# to resolve to loopback, which it does on current Windows, macOS and Linux resolvers. NOT RUN IN
# CI: it needs a real cluster and builds every image.
#
# The bootstrap prints the superuser's seed password once; this rig filters it out of its own
# output, so a teed log of a run does not hold it.

# GraphQL documents are single-quoted on purpose: their $variables are GraphQL's, not the shell's.
# shellcheck disable=SC2016
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="${DC_INT_CLUSTER:-devicechain}"
instance="${DC_INT_INSTANCE:-intrig}"
host="${DC_INT_HOST:-$instance.localhost}"
# The host port the cluster's ingress is published on. 80 for a cluster made from deploy/local/kind-cluster.yaml;
# set it when that mapping was moved because another cluster holds 80.
port="${DC_INT_INGRESS_PORT:-80}"
kube_context="kind-$cluster"
namespace="dci-$instance"
exe="$(go env GOEXE)"
dcctl="$repo_root/backend/cli/build/dcctl$exe"

# The rig talks to the ingress at the loopback address and names the instance's host in the Host header,
# because a name under .localhost resolves in curl but not in every resolver (Go's does not).
api="http://localhost:$port"
host_header=(-H "Host: $host")
um="$api/api/user-management"
dm="$api/api/device-management"
em="$api/api/event-management"

# A fresh tenant per run keeps the exact stored counts below honest: set DC_INT_TENANT to run again on the same instance.
tenant="${DC_INT_TENANT:-intrig}"
member_email="int-admin-$tenant@$instance.example"
profile="lora-sensor"
dtype="lora-sensor"
cs_int="chirpstack-main"
tts_int="tts-main"
cs_dev="lora-cs-001"
tts_dev="lora-tts-001"
cs_eui="70B3D57ED0012345"
tts_eui="70B3D57ED0054321"
ghost_eui="70B3D57ED00FFFFF"
uplinks=5

rig_tmp="$(mktemp -d "${TMPDIR:-/tmp}/dc-int-rig.XXXXXX")"
trap 'rm -rf "$rig_tmp"' EXIT

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '\033[0;37m    %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }

[[ "$instance" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] ||
  fail "DC_INT_INSTANCE must be a lower-case DNS label, got '$instance'"

k() { kubectl --context "$kube_context" "$@"; }

# OWNERSHIP, as in hack/sso-rig.sh: `up` records that THIS rig made <instance> on THIS cluster,
# and `down` destroys it only when the record says so.
owner_marker="${HOME:?}/.devicechain/integrations-rig-owners/$instance"
owned_by_rig() { [[ -f "$owner_marker" && "$(cat "$owner_marker")" == "$cluster" ]]; }

redact_secrets() { sed -u -E 's/^([[:space:]]*password:[[:space:]]*).+$/\1[redacted by hack\/integrations-rig.sh]/'; }

# ---------------------------------------------------------------------------
# up / down
# ---------------------------------------------------------------------------

build_dcctl() {
  say "building dcctl"
  make -C "$repo_root/backend/cli" build >/dev/null
  [[ -x "$dcctl" ]] || fail "dcctl was not built at $dcctl"
}

cmd_up() {
  need kind; need kubectl; need docker; need ko
  grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)" ||
    fail "kind cluster $cluster does not exist; run 'dcctl install local' first"
  if k get ns "$namespace" >/dev/null 2>&1; then
    owned_by_rig || fail "namespace $namespace exists and this rig did not create it; pick another DC_INT_INSTANCE"
    say "instance $instance already exists; reusing it"
    return
  fi
  build_dcctl
  # A unique tag: the chart pulls IfNotPresent, so a reused tag serves a node's cached layer.
  local tag
  tag="int-$(git -C "$repo_root" rev-parse --short=8 HEAD)-$(date -u +%Y%m%dT%H%M%SZ)"
  say "bootstrapping instance $instance on $kube_context with images $tag built from this tree"
  mkdir -p "$(dirname "$owner_marker")"
  printf '%s' "$cluster" >"$owner_marker"
  (cd "$repo_root" && DOCKER_BUILD_NET="${DOCKER_BUILD_NET:-default}" "$dcctl" bootstrap local "$instance" \
    --cluster "$cluster" --build --version "$tag" --host "$host" --no-tls --no-escrow --yes 2>&1 | redact_secrets)
}

cmd_down() {
  owned_by_rig || { note "instance $instance was not made by this rig; leaving it alone"; return 0; }
  say "destroying instance $instance"
  build_dcctl
  "$dcctl" destroy local "$instance" --yes 2>&1 | redact_secrets || true
  rm -f "$owner_marker"
}

# ---------------------------------------------------------------------------
# API helpers
# ---------------------------------------------------------------------------

# gql <url> <token|-> <query> [variables-json] prints .data; a GraphQL error prints to stderr, returns 1.
gql() {
  local url="$1" token="$2" query="$3" vars="${4:-}" body resp
  [[ -n "$vars" ]] || vars='{}'
  body="$(jq -n --arg q "$query" --argjson v "$vars" '{query: $q, variables: $v}')"
  if [[ "$token" == "-" ]]; then
    resp="$(curl -s "${host_header[@]}" -X POST "$url" -H 'Content-Type: application/json' -d "$body")"
  else
    resp="$(curl -s "${host_header[@]}" -X POST "$url" -H 'Content-Type: application/json' -H "Authorization: Bearer $token" -d "$body")"
  fi
  if [[ -z "$resp" ]] || ! jq -e . >/dev/null 2>&1 <<<"$resp"; then
    echo "transport: no GraphQL response from $url: ${resp:0:200}" >&2
    return 1
  fi
  if jq -e '.errors' >/dev/null 2>&1 <<<"$resp"; then
    jq -c '.errors' <<<"$resp" >&2
    return 1
  fi
  jq -c '.data' <<<"$resp"
}

# tolerate_exists runs a command and treats an "already exists" conflict as success, so a re-run
# over a half-built rig does not stop at the first thing the last run did.
tolerate_exists() {
  local out
  if out="$("$@" 2>&1)"; then printf '%s\n' "$out"; return 0; fi
  if grep -qi 'conflict\|already' <<<"$out"; then note "already present; continuing"; return 0; fi
  printf '%s\n' "$out" >&2
  return 1
}

admin_token=""
tenant_token=""
member_password=""

admin_login() {
  local pw
  pw="$(k -n "$namespace" get secret "dci-$instance-superuser" -o jsonpath='{.data.password}' | base64 -d)"
  [[ -n "$pw" ]] || fail "Secret $namespace/dci-$instance-superuser holds no superuser password"
  admin_token="$(gql "$um/graphql" - \
    'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -n --arg e "superuser@devicechain.local" --arg p "$pw" '{e:$e,p:$p}')" | jq -r '.login.identityToken')"
  [[ -n "$admin_token" && "$admin_token" != null ]] || fail "could not sign in as the superuser"
}

adm() { gql "$um/admin/graphql" "$admin_token" "$@"; }

tenant_login() {
  local ident
  ident="$(gql "$um/graphql" - \
    'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -n --arg e "$member_email" --arg p "$member_password" '{e:$e,p:$p}')" | jq -r '.login.identityToken')"
  tenant_token="$(gql "$um/graphql" - \
    'mutation($i:String!,$t:String!){selectTenant(identityToken:$i,tenant:$t){accessToken}}' \
    "$(jq -n --arg i "$ident" --arg t "$tenant" '{i:$i,t:$t}')" | jq -r '.selectTenant.accessToken')"
  [[ -n "$tenant_token" && "$tenant_token" != null ]] || fail "could not enter tenant $tenant"
}

dmq() { gql "$dm/graphql" "$tenant_token" "$@"; }
emq() { gql "$em/graphql" "$tenant_token" "$@"; }

# ---------------------------------------------------------------------------
# Setup: tenant, member, profile, type, integrations, devices
# ---------------------------------------------------------------------------

setup_tenant() {
  say "creating tenant $tenant and a member"
  admin_login
  local tier
  tier="$(adm '{tenantTiers{token}}' | jq -r '.tenantTiers[0].token')"
  [[ -n "$tier" && "$tier" != null ]] || fail "the instance has no tenant tier"
  tolerate_exists adm 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
    "$(jq -n --arg t "$tenant" --arg tier "$tier" '{r:{token:$t,name:"Integrations rig",tierToken:$tier}}')" >/dev/null
  # The password is made here and lives only in this process.
  member_password="$(openssl rand -base64 24 | tr -d '/+=')Aa1"
  if ! out="$(adm 'mutation($e:String!,$p:String!){createIdentity(request:{email:$e,password:$p,enabled:true,systemRoles:[]}){email}}' \
    "$(jq -n --arg e "$member_email" --arg p "$member_password" '{e:$e,p:$p}')" 2>&1)"; then
    grep -qi 'conflict\|in use' <<<"$out" || fail "could not create the member: $out"
    adm 'mutation($e:String!,$p:String!){setPassword(email:$e,password:$p){email}}' \
      "$(jq -n --arg e "$member_email" --arg p "$member_password" '{e:$e,p:$p}')" >/dev/null ||
      fail "the member exists and its password could not be reset"
  fi
  tolerate_exists adm 'mutation($e:String!,$t:String!){addMembership(email:$e,tenant:$t,roleTokens:["tenant-admin"]){email}}' \
    "$(jq -n --arg e "$member_email" --arg t "$tenant" '{e:$e,t:$t}')" >/dev/null
  tenant_login
}

setup_model() {
  say "creating the device profile (declared metrics), the device type and the devices"
  tolerate_exists dmq 'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
    "$(jq -n --arg t "$profile" '{r:{token:$t,name:"LoRaWAN sensor"}}')" >/dev/null
  local m
  # Declared: temperature (with a unit), battery.voltage (the nested key flattens to this) and the door
  # boolean. Humidity is deliberately NOT declared, to show an undeclared key is stored without a unit.
  for m in "temperature|DOUBLE|C" "battery.voltage|DOUBLE|V" "door|BOOLEAN|"; do
    IFS='|' read -r key typ unit <<<"$m"
    tolerate_exists dmq 'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){token}}' \
      "$(jq -n --arg t "$profile-${key//./-}" --arg p "$profile" --arg k "$key" --arg d "$typ" --arg u "$unit" \
        '{r:({token:$t,deviceProfileToken:$p,metricKey:$k,name:$k,dataType:$d} + (if $u == "" then {} else {unit:$u} end))}')" >/dev/null
  done
  dmq 'mutation($t:String!){publishDeviceProfile(token:$t){version}}' "$(jq -n --arg t "$profile" '{t:$t}')" >/dev/null ||
    note "profile already published"
  tolerate_exists dmq 'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
    "$(jq -n --arg t "$dtype" --arg p "$profile" '{r:{token:$t,name:"LoRaWAN sensor",profileToken:$p}}')" >/dev/null
  # The alias is written in lower case on purpose: it is stored, and matched, in its canonical upper-case form.
  tolerate_exists dmq 'mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}' \
    "$(jq -n --arg t "$cs_dev" --arg ty "$dtype" --arg v "${cs_eui,,}" \
      '{r:{token:$t,name:"ChirpStack sensor",deviceTypeToken:$ty,aliases:[{scheme:"lorawan.deveui",value:$v}]}}')" >/dev/null
  tolerate_exists dmq 'mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}' \
    "$(jq -n --arg t "$tts_dev" --arg ty "$dtype" --arg v "$tts_eui" \
      '{r:{token:$t,name:"The Things Stack sensor",deviceTypeToken:$ty,aliases:[{scheme:"lorawan.deveui",value:$v}]}}')" >/dev/null
  local got
  got="$(dmq 'query($t:[String!]!){devicesByToken(tokens:$t){token aliases{scheme value}}}' \
    "$(jq -n --arg a "$cs_dev" '{t:[$a]}')" | jq -r '.devicesByToken[0].aliases[0].value')"
  [[ "$got" == "$cs_eui" ]] || fail "the alias was not stored in canonical form: wanted $cs_eui, got '$got'"
  note "alias stored as $got (written as ${cs_eui,,})"
}

# create_integration <token> <type> prints the bearer token on stdout, once.
create_integration() {
  local token="$1" type="$2" out
  if ! out="$(dmq 'mutation($r:IntegrationCreateRequest!){createIntegration(request:$r){issuedToken}}' \
    "$(jq -n --arg t "$token" --arg ty "$type" --arg dt "$dtype" \
      '{r:{token:$t,name:$t,type:$ty,enabled:true,deviceTypeTokens:[$dt],linkMetrics:true}}')" 2>&1)"; then
    grep -qi 'conflict\|already' <<<"$out" || fail "could not create integration $token: $out"
    out="$(dmq 'mutation($t:String!){rotateIntegrationToken(token:$t){issuedToken}}' "$(jq -n --arg t "$token" '{t:$t}')")" ||
      fail "integration $token exists and its token could not be rotated"
    jq -r '.rotateIntegrationToken.issuedToken' <<<"$out"
    return
  fi
  jq -r '.createIntegration.issuedToken' <<<"$out"
}

# ---------------------------------------------------------------------------
# Driving and reading
# ---------------------------------------------------------------------------

sim=""
build_sim() {
  say "building dc-simulator"
  sim="$rig_tmp/dc-simulator$exe"
  (cd "$repo_root/backend/sims/dc-simulator" && go build -o "$sim" .)
}

# run_sim <ns> <integration> <token> <euis> [extra flags] prints the simulator's one-line summary.
run_sim() {
  local ns="$1" int="$2" token="$3" euis="$4"
  shift 4
  DC_SIM_INTEGRATION_TOKEN="$token" "$sim" --lorawan-ns "$ns" --integration-host "$host" \
    --integration-url "$api/api/integrations/$tenant/$int" --deveui "$euis" --uplinks "$uplinks" "$@"
}

# names_of <device> prints "<name> <value> <unit|null>" lines of the device's stored measurements.
measurements_of() {
  emq 'query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{name value unit}}}' \
    "$(jq -n --arg d "$1" '{c:{pageNumber:1,pageSize:500,deviceToken:$d,eventTypes:[2]}}')" |
    jq -r '.measurementEvents.results[] | "\(.name) \(.value) \(.unit // "null")"'
}

count_of() { measurements_of "$1" | wc -l | tr -d ' '; }

# wait_count <device> <want> waits for the stored count to reach <want>, then for it to hold there
# for a further settling period, so a duplicate that arrives late is still caught.
wait_count() {
  local dev="$1" want="$2" n=0
  for _ in $(seq 1 60); do
    n="$(count_of "$dev")"
    [[ "$n" -ge "$want" ]] && break
    sleep 2
  done
  sleep 12
  n="$(count_of "$dev")"
  [[ "$n" == "$want" ]] || fail "$dev holds $n stored measurements, wanted exactly $want"
  note "$dev holds exactly $n measurements"
}

status_of() { jq -r --arg s "$2" '.statuses[$s] // 0' <<<"$1"; }

expect_status() {
  local summary="$1" code="$2" want="$3" got
  got="$(status_of "$summary" "$code")"
  [[ "$got" == "$want" ]] || fail "expected $want response(s) with status $code, got $got: $summary"
}

assert_stored() {
  local dev="$1" label="$2" body temp
  body="$(measurements_of "$dev")"
  local n
  for n in temperature humidity battery.voltage door lorawan.rssi lorawan.snr lorawan.gateways; do
    [[ "$(grep -c "^$n " <<<"$body")" == "$uplinks" ]] || fail "$label: wanted $uplinks '$n' measurements, got: $(grep -c "^$n " <<<"$body")"
  done
  temp="$(grep '^temperature ' <<<"$body" | head -n1)"
  [[ "$temp" == *" C" ]] || fail "$label: the declared metric's unit was not applied: $temp"
  grep '^humidity ' <<<"$body" | grep -q ' null$' || fail "$label: an undeclared key should carry no unit"
  grep '^door ' <<<"$body" | awk '{print $2}' | grep -qvE '^(0|1)$' && fail "$label: a boolean must be stored as 0 or 1"
  [[ "$(grep -c '^door 1' <<<"$body")" == "2" ]] || fail "$label: door is open on the odd uplinks (2 of $uplinks)"
  grep '^lorawan.gateways ' <<<"$body" | awk '{print $2}' | grep -qvE '^2$' && fail "$label: two gateways reported every uplink"
  grep -q '^status ' <<<"$body" && fail "$label: a string leaf must not be stored"
  note "$label: all 7 keys stored $uplinks times, unit and 1/0 booleans as declared"
}

cmd_test() {
  need curl; need jq; need kubectl; need go
  k get ns "$namespace" >/dev/null 2>&1 || fail "instance $instance is not up; run 'up' first"
  build_sim
  setup_tenant
  setup_model

  say "creating the integrations"
  local cs_tok tts_tok
  cs_tok="$(create_integration "$cs_int" CHIRPSTACK_V4)"
  tts_tok="$(create_integration "$tts_int" TTS_V3)"
  [[ ${#cs_tok} -ge 40 && ${#tts_tok} -ge 40 ]] || fail "an issued token is shorter than 40 characters"

  # A route that is not yet published answers 404 from the ingress, not 401: wait for it.
  local code
  for _ in $(seq 1 30); do
    code="$(curl -s "${host_header[@]}" -o /dev/null -w '%{http_code}' -X POST -H 'Authorization: Bearer x' "$api/api/integrations/$tenant/$cs_int?event=up")"
    [[ "$code" == 401 ]] && break
    sleep 2
  done
  [[ "$code" == 401 ]] || fail "the public route never answered 401 to a wrong token (last: $code)"

  say "NEGATIVE CONTROL: a disabled integration refuses the simulator and stores nothing"
  dmq 'mutation($t:String!,$r:IntegrationUpdateRequest!){updateIntegration(token:$t,request:$r){enabled}}' \
    "$(jq -n --arg t "$cs_int" '{t:$t,r:{enabled:false}}')" >/dev/null
  local s
  s="$(run_sim chirpstack "$cs_int" "$cs_tok" "$cs_eui" --uplinks 2)"
  note "$s"
  expect_status "$s" 403 2
  sleep 6
  [[ "$(count_of "$cs_dev")" == 0 ]] || fail "a disabled integration stored measurements"
  dmq 'mutation($t:String!,$r:IntegrationUpdateRequest!){updateIntegration(token:$t,request:$r){enabled}}' \
    "$(jq -n --arg t "$cs_int" '{t:$t,r:{enabled:true}}')" >/dev/null
  # The front door remembers what it learned about an integration for up to 60 seconds, so a switch
  # back on is not seen at once. (A switch OFF is seen at once by the part that stores the data.)
  note "waiting 65 s for the endpoint's cached view of the integration to expire"
  sleep 65

  say "ChirpStack: $uplinks uplinks per device, each sent twice, one DevEUI with no device"
  s="$(run_sim chirpstack "$cs_int" "$cs_tok" "$cs_eui,$ghost_eui" --resend)"
  note "$s"
  expect_status "$s" 202 $((uplinks * 2 * 2))
  wait_count "$cs_dev" $((uplinks * 7))
  assert_stored "$cs_dev" "ChirpStack"

  say "The Things Stack: $uplinks uplinks, each sent twice, on the uplink-message path"
  s="$(run_sim tts "$tts_int" "$tts_tok" "$tts_eui" --resend)"
  note "$s"
  expect_status "$s" 202 $((uplinks * 2))
  wait_count "$tts_dev" $((uplinks * 7))
  assert_stored "$tts_dev" "The Things Stack"

  say "CONTROL: a genuinely new uplink IS stored, so the exact counts above can move"
  s="$(run_sim chirpstack "$cs_int" "$cs_tok" "$cs_eui" --uplinks 1)"
  note "$s"
  expect_status "$s" 202 1
  wait_count "$cs_dev" $((uplinks * 7 + 7))

  say "unregistered sighting, then clear"
  local seen
  seen="$(dmq 'query($c:UnregisteredIntegrationDeviceSearchCriteria!){unregisteredIntegrationDevices(criteria:$c){results{scheme value count}}}' \
    "$(jq -n --arg i "$cs_int" '{c:{integration:$i,pageNumber:1,pageSize:50}}')")"
  jq -e --arg v "$ghost_eui" '.unregisteredIntegrationDevices.results | map(select(.scheme == "lorawan.deveui" and .value == $v)) | length == 1' \
    >/dev/null <<<"$seen" || fail "the unknown DevEUI $ghost_eui is not listed as an unregistered sighting: $seen"
  jq -e --arg v "$cs_eui" '.unregisteredIntegrationDevices.results | map(select(.value == $v)) | length == 0' \
    >/dev/null <<<"$seen" || fail "a registered DevEUI was listed as unregistered"
  note "$ghost_eui listed once; $cs_eui (registered) is not"
  local cleared
  cleared="$(dmq 'mutation($i:String!){clearUnregisteredIntegrationDevices(integration:$i)}' "$(jq -n --arg i "$cs_int" '{i:$i}')" |
    jq -r '.clearUnregisteredIntegrationDevices')"
  [[ "$cleared" -ge 1 ]] || fail "clear removed $cleared rows"
  note "cleared $cleared row(s)"

  say "a wrong bearer token is refused 401 and stores nothing"
  local before after
  before="$(count_of "$cs_dev")"
  s="$(run_sim chirpstack "$cs_int" "not-the-token-not-the-token-not-the-token" "$cs_eui" --uplinks 2)"
  note "$s"
  expect_status "$s" 401 2
  sleep 6
  after="$(count_of "$cs_dev")"
  [[ "$before" == "$after" ]] || fail "a refused request changed the stored count ($before -> $after)"

  say "a message type that is not an uplink is acknowledged 204 and stores nothing"
  code="$(curl -s "${host_header[@]}" -o /dev/null -w '%{http_code}' -X POST "$api/api/integrations/$tenant/$tts_int/join-accept" \
    -H "Authorization: Bearer $tts_tok" -H 'Content-Type: application/json' \
    -d "{\"end_device_ids\":{\"dev_eui\":\"$tts_eui\"},\"join_accept\":{}}")"
  [[ "$code" == 204 ]] || fail "a join-accept message answered $code, wanted 204"
  code="$(curl -s "${host_header[@]}" -o /dev/null -w '%{http_code}' "$api/api/integrations/$tenant/$tts_int/uplink-message")"
  [[ "$code" == 405 ]] || fail "a GET answered $code, wanted 405"

  say "PASS: stored measurements, link metrics, dedup, unregistered list, 401, 403, 204 and 405"
}

case "${1:-}" in
  up) cmd_up ;;
  test) cmd_test ;;
  down) cmd_down ;;
  all)
    cmd_up
    rc=0
    cmd_test || rc=$?
    cmd_down || true
    exit "$rc"
    ;;
  *) fail "usage: $0 up|test|down|all" ;;
esac
