#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# End-to-end rig for payload decoders: a LoRaWAN network server WITHOUT a codec (the dc-simulator in
# integration mode with --lorawan-binary) posting raw frames to the PUBLIC route of a real instance on
# a kind cluster of this rig's own.
#
#   hack/decoder-rig.sh up         # cluster + images built from this tree + a throwaway instance
#   hack/decoder-rig.sh test       # decode, store, count; the paired broken decoder; the control
#   hack/decoder-rig.sh mixed      # the rolling-upgrade window: no reader of the uplink stream, then drain
#   hack/decoder-rig.sh mqtt       # a device's own payload over its MQTT connection (upgrades the instance)
#   hack/decoder-rig.sh down       # destroy the instance and DELETE the cluster
#   hack/decoder-rig.sh all        # up, test, mixed, mqtt, down (down runs even when a step fails)
#   hack/decoder-rig.sh selftest   # offline: each assertion refuses a planted bad value
#
# WHAT `test` PROVES, through the ingress and across every service, not against a unit double:
#
#   1. A raw six-byte frame, with no decoded object, is decoded by the profile's tenant decoder and
#      stored: the same seven keys, units and 1/0 booleans a codec-carrying uplink stores, with the
#      link metrics beside them, EXACTLY once per uplink.
#   2. The decoder's health, read through the API, counts those uplinks as decoded and nothing else.
#   3. THE PAIRED BROKEN RUN. A second device type whose decoder cannot read the same frame (its
#      section wants another port) stores NOTHING, records one DECODE_FAILED failed event per uplink
#      with the reason named, counts every one as failed in the decoder's health, and keeps samples
#      that, replayed through the test door against the corrected decoder, decode.
#   4. NEGATIVE CONTROL. A decoder planted broken on the GOOD profile makes the stored-count check go
#      red, the failure is recorded, and restoring the decoder makes it green again. A check that
#      cannot fail proves nothing; this one is shown failing before it is trusted.
#
# WHAT `mixed` PROVES. The window of a rolling upgrade in which no device-management reads the
# integration-uplinks stream: an uplink for a decoder's device type is accepted, parked on the stream,
# and NEITHER stored nor recorded as a failure meanwhile; once device-management is back the parked
# uplinks are decoded and stored exactly once. (The previous release's image cannot be run beside the
# new one to make that window; see the note above cmd_mixed.)
#
# The cluster is this rig's own (DC_DEC_RIG_CLUSTER, default dc-decrig), with its own kubeconfig; it
# never touches the shared developer cluster and never switches a kube-context. NOT RUN IN CI: it
# needs a real cluster and builds every image. Requires kind, kubectl, docker, jq, curl, go, ko.

# GraphQL documents are single-quoted on purpose: their $variables are GraphQL's, not the shell's.
# shellcheck disable=SC2016
set -euo pipefail

repo_root="${DC_DEC_RIG_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cluster="${DC_DEC_RIG_CLUSTER:-dc-decrig}"
kube_context="kind-$cluster"
instance="${DC_DEC_RIG_INSTANCE:-decrig}"
namespace="dci-$instance"
host="${DC_DEC_RIG_HOST:-$instance.localhost}"
http_port="${DC_DEC_RIG_HTTP_PORT:-18701}"
state_dir="${DC_DEC_RIG_STATE:-$repo_root/.decoder-rig}"
min_free_gb="${DC_DEC_RIG_MIN_FREE_GB:-60}"
deadman_hours="${DC_DEC_RIG_DEADMAN_HOURS:-4}"
tenant="${DC_DEC_RIG_TENANT:-decrig}"
member_email="dec-admin-$tenant@$instance.example"
exe="$(go env GOEXE 2>/dev/null || true)"
dcctl="$repo_root/backend/cli/build/dcctl$exe"

winpath() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }
mkdir -p "$state_dir"
kubeconfig="$(winpath "$state_dir/kubeconfig")"
export KUBECONFIG="$kubeconfig"

[[ "$cluster" != devicechain ]] || { echo "refusing: DC_DEC_RIG_CLUSTER=devicechain is the shared developer cluster" >&2; exit 2; }

api="http://localhost:$http_port"
host_header=(-H "Host: $host")
um="$api/api/user-management"
dm="$api/api/device-management"
em="$api/api/event-management"

profile="dec-sensor"; dtype="dec-sensor"; dev="dec-001"; eui="70B3D57ED0DEC001"
bprofile="dec-broken"; btype="dec-broken"; bdev="dec-bad-001"; beui="70B3D57ED0DEC002"
int="chirpstack-dec"
uplinks=5

rig_tmp="$(mktemp -d "${TMPDIR:-/tmp}/dc-dec-rig.XXXXXX")"
rig_cmd="${1:-?}"
run_log="$state_dir/run-$(date -u +%Y%m%dT%H%M%SZ).log"
exec > >(tee -a "$run_log") 2>&1
# cleanup is the ONE EXIT trap. Phases that need a teardown register it here (a variable, never a
# new trap) so `all` and `mixed` compose: replacing the trap would drop the other's teardown.
dm_restore_to=""  # set by `mixed` while device-management is scaled to zero
down_on_exit=0    # set by `all`: tear the cluster down however the run ends
cleanup() {
  local rc=$?
  trap - EXIT
  set +e
  if [[ -n "$dm_restore_to" ]]; then k -n "$namespace" scale deploy/device-management --replicas="$dm_restore_to" >/dev/null 2>&1; fi
  if [[ "${dm_hung:-0}" == 1 ]]; then dm_hang_off; fi
  if [[ -n "${planted_wt:-}" ]]; then remove_planted_wt; fi
  if [[ -n "${es_image_restore:-}" ]]; then
    k -n "$namespace" set image deploy/event-sources "$(k -n "$namespace" get deploy event-sources -o jsonpath='{.spec.template.spec.containers[0].name}')=$es_image_restore" >/dev/null 2>&1
  fi
  if [[ "$down_on_exit" == 1 ]]; then (cmd_down) || true; fi
  rm -rf "$rig_tmp"; printf 'decoder-rig: %s exited with status %d\n' "$rig_cmd" "$rc" >>"$run_log"
  exit "$rc"
}
trap cleanup EXIT

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
red() { printf '\033[1;33m    RED (as designed) %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }
redact() { sed -u -E 's/^([[:space:]]*password:[[:space:]]*).+$/\1[redacted]/'; }
# jq on Windows writes CRLF unless told not to, and "true\r" is not "true". -b is that switch.
if command jq -b -n 1 >/dev/null 2>&1; then jq() { command jq -b "$@"; }; fi

k() { kubectl --kubeconfig "$kubeconfig" --context "$kube_context" "$@"; }

# ---------------------------------------------------------------------------
# up / down
# ---------------------------------------------------------------------------

cluster_exists() { grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; }

# THE DEAD-MAN. A rig cluster left behind by a crashed or abandoned run holds a node's worth of
# memory and disk until someone notices, so `up` arms a detached timer, as soon as the cluster
# exists, that deletes THIS cluster (by name, with this rig's kubeconfig) after
# DC_DEC_RIG_DEADMAN_HOURS (0 disarms it). `down` disarms it, so a later cluster of the same name is
# never deleted by an earlier run's timer.
deadman_pid_file="$state_dir/deadman.pid"
disarm_deadman() {
  local pid child
  [[ -f "$deadman_pid_file" ]] || return 0
  pid="$(<"$deadman_pid_file")"
  # The sleep is the timer's child: end it too, so nothing outlives the disarm.
  for child in $(ps -o pid=,ppid= 2>/dev/null | awk -v p="$pid" '$2 == p { print $1 }'); do kill "$child" 2>/dev/null || true; done
  kill "$pid" 2>/dev/null || true
  rm -f "$deadman_pid_file"
}
arm_deadman() {
  disarm_deadman
  [[ "$deadman_hours" != 0 ]] || { note "dead-man disarmed (DC_DEC_RIG_DEADMAN_HOURS=0)"; return 0; }
  nohup bash -c "sleep $((deadman_hours * 3600)) && kind delete cluster --name '$cluster' --kubeconfig '$kubeconfig'" \
    >"$state_dir/deadman.log" 2>&1 </dev/null &
  printf '%s\n' "$!" >"$deadman_pid_file"
  disown || true
  note "dead-man armed: cluster $cluster is deleted in ${deadman_hours}h unless 'down' runs first (pid $(<"$deadman_pid_file"))"
}

build_dcctl() {
  say "building dcctl"
  make -C "$repo_root/backend/cli" build >/dev/null
  [[ -x "$dcctl" ]] || fail "dcctl was not built at $dcctl"
}

cmd_up() {
  need kind; need kubectl; need docker; need jq; need ko; need go
  local free
  free="$(df -Pk "$HOME" | awk 'NR==2 {printf "%d", $4/1024/1024}')"
  [[ "$free" -ge "$min_free_gb" ]] || fail "only ${free} GB free; this rig needs $min_free_gb (DC_DEC_RIG_MIN_FREE_GB)"
  if ! cluster_exists; then
    say "creating kind cluster $cluster (kubeconfig $kubeconfig, http on 127.0.0.1:$http_port)"
    cat >"$rig_tmp/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry.mirrors."localhost:5000"]
      endpoint = ["http://kind-registry:5000"]
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "ingress-ready=true"
    extraPortMappings:
      - containerPort: 80
        hostPort: $http_port
        listenAddress: "127.0.0.1"
        protocol: TCP
EOF
    kind create cluster --name "$cluster" --kubeconfig "$kubeconfig" --config "$(winpath "$rig_tmp/kind.yaml")" --wait 120s
    arm_deadman
  else
    k get ns kube-system >/dev/null 2>&1 || fail "kind cluster $cluster exists but is not in $kubeconfig: not this rig's"
  fi
  if k get ns "$namespace" >/dev/null 2>&1; then note "instance $instance already exists; reusing it"; return; fi
  build_dcctl
  local tag
  tag="dec-$(git -C "$repo_root" rev-parse --short=8 HEAD)-$(date -u +%Y%m%dT%H%M%SZ)"
  printf '%s\n' "$tag" >"$state_dir/image-tag"
  say "installing the cluster prerequisites and bootstrapping $instance with images $tag built from this tree"
  "$dcctl" install local --yes --cluster "$cluster" --no-monitoring --build --version "$tag" 2>&1 | redact
  (cd "$repo_root" && "$dcctl" bootstrap local "$instance" --yes --no-escrow --no-tls --cluster "$cluster" --host "$host" --build --version "$tag" 2>&1 | redact)
  say "UP COMPLETE"
}

cmd_down() {
  need kind; need kubectl
  if cluster_exists; then
    local uid
    uid="$(k get ns kube-system -o jsonpath='{.metadata.uid}' 2>/dev/null)" ||
      fail "kind cluster $cluster exists but $kubeconfig does not reach it: not this rig's; refusing to delete it"
    local rec="${HOME:?}/.devicechain/instances/$instance/instance.json" recctx
    if [[ -f "$rec" ]]; then
      recctx="$(jq -r '.kubeContext // empty' "$rec")"
      [[ "$recctx" == "$kube_context" ]] || fail "$rec names '$recctx', not $kube_context: not this rig's; refusing to destroy it"
      [[ -x "$dcctl" ]] || build_dcctl
      say "destroying instance $instance"
      "$dcctl" destroy local "$instance" --yes 2>&1 | redact || note "destroy exited non-zero; deleting the cluster anyway"
    fi
    say "deleting kind cluster $cluster"
    kind delete cluster --name "$cluster" --kubeconfig "$kubeconfig"
    local dir clusters="${HOME:?}/.devicechain/clusters"
    [[ -z "$uid" ]] || rm -rf "${clusters:?}/$uid"
    for dir in "$clusters"/*/; do
      [[ -f "$dir/cluster.json" ]] || continue
      [[ "$(jq -r '.kubeContext // empty' "$dir/cluster.json" 2>/dev/null || true)" != "$kube_context" ]] || rm -rf "${dir:?}"
    done
  fi
  disarm_deadman
  rm -f "$state_dir/kubeconfig" "$state_dir/image-tag"
}

# ---------------------------------------------------------------------------
# API helpers
# ---------------------------------------------------------------------------

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
  if jq -e '.errors' >/dev/null 2>&1 <<<"$resp"; then jq -c '.errors' <<<"$resp" >&2; return 1; fi
  jq -c '.data' <<<"$resp"
}

tolerate_exists() {
  local out
  if out="$("$@" 2>&1)"; then printf '%s\n' "$out"; return 0; fi
  if grep -qi 'conflict\|already' <<<"$out"; then note "already present; continuing"; return 0; fi
  printf '%s\n' "$out" >&2
  return 1
}

admin_token=""; tenant_token=""; member_password=""
adm() { gql "$um/admin/graphql" "$admin_token" "$@"; }
dmq() { tenant_gql "$dm/graphql" "$@"; }
emq() { tenant_gql "$em/graphql" "$@"; }

# tenant_gql is gql with the member's token, signed in again when it has expired. A run of `mqtt` lasts
# longer than an access token, and most calls run in a subshell, so the renewal happens where the
# expiry is met rather than on a schedule a subshell could not update.
tenant_gql() {
  local url="$1" out err
  shift
  err="$(mktemp "$rig_tmp/gql.XXXXXX")"
  if out="$(gql "$url" "$tenant_token" "$@" 2>"$err")"; then rm -f "$err"; printf '%s\n' "$out"; return 0; fi
  if [[ -n "$member_password" ]] && grep -q 'expired' "$err"; then
    rm -f "$err"
    tenant_login
    gql "$url" "$tenant_token" "$@"
    return
  fi
  cat "$err" >&2; rm -f "$err"
  return 1
}

admin_login() {
  local pw
  pw="$(k -n "$namespace" get secret "dci-$instance-superuser" -o jsonpath='{.data.password}' | base64 -d)"
  [[ -n "$pw" ]] || fail "Secret $namespace/dci-$instance-superuser holds no superuser password"
  admin_token="$(gql "$um/graphql" - 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -n --arg e "superuser@devicechain.local" --arg p "$pw" '{e:$e,p:$p}')" | jq -r '.login.identityToken')"
  [[ -n "$admin_token" && "$admin_token" != null ]] || fail "could not sign in as the superuser"
}

tenant_login() {
  local ident
  ident="$(gql "$um/graphql" - 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -n --arg e "$member_email" --arg p "$member_password" '{e:$e,p:$p}')" | jq -r '.login.identityToken')"
  tenant_token="$(gql "$um/graphql" - 'mutation($i:String!,$t:String!){selectTenant(identityToken:$i,tenant:$t){accessToken}}' \
    "$(jq -n --arg i "$ident" --arg t "$tenant" '{i:$i,t:$t}')" | jq -r '.selectTenant.accessToken')"
  [[ -n "$tenant_token" && "$tenant_token" != null ]] || fail "could not enter tenant $tenant"
}

setup_tenant() {
  say "creating tenant $tenant and a member"
  admin_login
  local tier out
  tier="$(adm '{tenantTiers{token}}' | jq -r '.tenantTiers[0].token')"
  [[ -n "$tier" && "$tier" != null ]] || fail "the instance has no tenant tier"
  tolerate_exists adm 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
    "$(jq -n --arg t "$tenant" --arg tier "$tier" '{r:{token:$t,name:"Decoder rig",tierToken:$tier}}')" >/dev/null
  member_password="$(openssl rand -base64 24 | tr -d '/+=')Aa1"
  if ! out="$(adm 'mutation($e:String!,$p:String!){createIdentity(request:{email:$e,password:$p,enabled:true,systemRoles:[]}){email}}' \
    "$(jq -n --arg e "$member_email" --arg p "$member_password" '{e:$e,p:$p}')" 2>&1)"; then
    grep -qi 'conflict\|in use' <<<"$out" || fail "could not create the member: $out"
    adm 'mutation($e:String!,$p:String!){setPassword(email:$e,password:$p){email}}' \
      "$(jq -n --arg e "$member_email" --arg p "$member_password" '{e:$e,p:$p}')" >/dev/null || fail "member password reset failed"
  fi
  tolerate_exists adm 'mutation($e:String!,$t:String!){addMembership(email:$e,tenant:$t,roleTokens:["tenant-admin"]){email}}' \
    "$(jq -n --arg e "$member_email" --arg t "$tenant" '{e:$e,t:$t}')" >/dev/null
  tenant_login
}

# ---------------------------------------------------------------------------
# The model: one profile with the simulator's decoder, a paired one whose decoder cannot read the
# frame
# ---------------------------------------------------------------------------

sim=""
build_sim() {
  say "building dc-simulator"
  sim="$rig_tmp/dc-simulator$exe"
  (cd "$repo_root/backend/sims/dc-simulator" && go build -o "$sim" .)
}

good_doc=""
broken_doc=""
make_docs() {
  good_doc="$("$sim" --lorawan-print-decoder)"
  jq -e '.kind == "CEL"' >/dev/null <<<"$good_doc" || fail "the simulator printed no decoder document"
  # Compiles, but its only section wants port 9, and the simulator sends port 2: every uplink is
  # "no_section". No vectors, so publishing it is not gated on the layout.
  broken_doc="$(jq -c '.sections[0].when = "fPort == 9" | .vectors = []' <<<"$good_doc")"
}

create_profile() {
  local p="$1" doc="$2" m key typ unit
  tolerate_exists dmq 'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
    "$(jq -n --arg t "$p" '{r:{token:$t,name:$t}}')" >/dev/null
  # Declared: every key the decoder outputs. (Output keys must be declared, measured metrics.)
  for m in "temperature|DOUBLE|C" "humidity|DOUBLE|" "battery.voltage|DOUBLE|V" "door|BOOLEAN|"; do
    IFS='|' read -r key typ unit <<<"$m"
    tolerate_exists dmq 'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){token}}' \
      "$(jq -n --arg t "$p-${key//./-}" --arg p "$p" --arg k "$key" --arg d "$typ" --arg u "$unit" \
        '{r:({token:$t,deviceProfileToken:$p,metricKey:$k,name:$k,dataType:$d} + (if $u == "" then {} else {unit:$u} end))}')" >/dev/null
  done
  set_decoder "$p" "$doc"
}

set_decoder() {
  dmq 'mutation($p:String!,$d:String!){setDeviceProfileDecoder(profileToken:$p,document:$d){token}}' \
    "$(jq -n --arg p "$1" --arg d "$2" '{p:$p,d:$d}')" >/dev/null || fail "could not set the decoder of $1"
}

publish() {
  dmq 'mutation($t:String!){publishDeviceProfile(token:$t){version}}' "$(jq -n --arg t "$1" '{t:$t}')" | jq -r '.publishDeviceProfile.version' ||
    fail "could not publish $1"
}

create_type_and_device() {
  local ty="$1" p="$2" d="$3" e="$4"
  tolerate_exists dmq 'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
    "$(jq -n --arg t "$ty" --arg p "$p" '{r:{token:$t,name:$t,profileToken:$p}}')" >/dev/null
  tolerate_exists dmq 'mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}' \
    "$(jq -n --arg t "$d" --arg ty "$ty" --arg v "$e" '{r:{token:$t,name:$t,deviceTypeToken:$ty,aliases:[{scheme:"lorawan.deveui",value:$v}]}}')" >/dev/null
}

int_token=""
setup_model() {
  say "creating the profiles (decoders), device types, devices and the integration"
  create_profile "$profile" "$good_doc";    publish "$profile" >/dev/null
  create_profile "$bprofile" "$broken_doc"; publish "$bprofile" >/dev/null
  create_type_and_device "$dtype" "$profile" "$dev" "$eui"
  create_type_and_device "$btype" "$bprofile" "$bdev" "$beui"
  local out
  if ! out="$(dmq 'mutation($r:IntegrationCreateRequest!){createIntegration(request:$r){issuedToken}}' \
    "$(jq -n --arg t "$int" --argjson dts "$(jq -n --arg a "$dtype" --arg b "$btype" '[$a,$b]')" \
      '{r:{token:$t,name:$t,type:"CHIRPSTACK_V4",enabled:true,deviceTypeTokens:$dts,linkMetrics:true}}')" 2>&1)"; then
    grep -qi 'conflict\|already' <<<"$out" || fail "could not create the integration: $out"
    out="$(dmq 'mutation($t:String!){rotateIntegrationToken(token:$t){issuedToken}}' "$(jq -n --arg t "$int" '{t:$t}')")" || fail "token rotation failed"
    int_token="$(jq -r '.rotateIntegrationToken.issuedToken' <<<"$out")"
  else
    int_token="$(jq -r '.createIntegration.issuedToken' <<<"$out")"
  fi
  [[ ${#int_token} -ge 40 ]] || fail "the issued token is shorter than 40 characters"
  local code
  for _ in $(seq 1 30); do
    code="$(curl -s "${host_header[@]}" -o /dev/null -w '%{http_code}' -X POST -H 'Authorization: Bearer x' "$api/api/integrations/$tenant/$int?event=up")"
    [[ "$code" == 401 ]] && break
    sleep 2
  done
  [[ "$code" == 401 ]] || fail "the public route never answered 401 to a wrong token (last: $code)"
}

# ---------------------------------------------------------------------------
# Driving and reading
# ---------------------------------------------------------------------------

# send <eui> <uplinks> prints the simulator's one-line summary. Binary frames: no decoded object.
send() {
  DC_SIM_INTEGRATION_TOKEN="$int_token" "$sim" --lorawan-ns chirpstack --lorawan-binary --integration-host "$host" \
    --integration-url "$api/api/integrations/$tenant/$int" --deveui "$1" --uplinks "$2"
}

status_of() { jq -r --arg s "$2" '.statuses[$s] // 0' <<<"$1"; }
expect_status() {
  local got
  got="$(status_of "$1" "$2")"
  [[ "$got" == "$3" ]] || fail "expected $3 response(s) with status $2, got $got: $1"
}

measurements_of() {
  emq 'query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{name value unit}}}' \
    "$(jq -n --arg d "$1" '{c:{pageNumber:1,pageSize:500,deviceToken:$d,eventTypes:[2]}}')" |
    jq -r '.measurementEvents.results[] | "\(.name) \(.value) \(.unit // "null")"'
}
count_of() { measurements_of "$1" | wc -l | tr -d ' '; }

# reaches <device> <want> <seconds> succeeds when the stored count reaches <want> in time.
reaches() {
  local n=0 i
  for ((i = 0; i < $3; i += 2)); do
    n="$(count_of "$1")"
    [[ "$n" -ge "$2" ]] && return 0
    sleep 2
  done
  return 1
}

# wait_count waits for <want>, then for a settling period, so a duplicate that arrives late is caught.
wait_count() {
  local d="$1" want="$2" n
  reaches "$d" "$want" 120 || fail "$d never reached $want stored measurements (has $(count_of "$d"))"
  sleep 12
  n="$(count_of "$d")"
  [[ "$n" == "$want" ]] || fail "$d holds $n stored measurements, wanted exactly $want"
  note "$d holds exactly $n measurements"
}

# check_body is the content assertion of hack/integrations-rig.sh: the decoder must store what a
# codec-carrying uplink stores.
check_body() {
  local label="$1" body="$2" n c first bad
  for n in temperature humidity battery.voltage door lorawan.rssi lorawan.snr lorawan.gateways; do
    c="$(awk -v n="$n" '$1 == n { c++ } END { print c + 0 }' <<<"$body")"
    [[ "$c" == "$uplinks" ]] || fail "$label: wanted $uplinks '$n' measurements, got: $c"
  done
  first="$(sed -n '/^temperature /{p;q;}' <<<"$body")"
  [[ "$first" == *" C" ]] || fail "$label: the declared metric's unit was not applied: $first"
  # The simulator's value space is fixed (ReadingsOf and the two simulated gateways), so every
  # stored number must be a decimal inside it, with the unit its metric declares. A decoder that
  # scales wrongly, drops a sign or loses a unit lands outside.
  check_range "$label" "$body" temperature 15 29.5 C
  check_range "$label" "$body" humidity 40 73 null
  check_range "$label" "$body" battery.voltage 3.0 3.4 V
  check_range "$label" "$body" lorawan.rssi -87 -80 null
  check_range "$label" "$body" lorawan.snr 6 8.5 null
  bad="$(awk '$1 == "door" && $2 != "0" && $2 != "1"' <<<"$body")"
  [[ -z "$bad" ]] || fail "$label: a boolean must be stored as 0 or 1: $bad"
  bad="$(awk '$1 == "door" && $3 != "null"' <<<"$body")"
  [[ -z "$bad" ]] || fail "$label: a boolean carries no unit: $bad"
  c="$(awk '$1 == "door" && $2 == "1" { c++ } END { print c + 0 }' <<<"$body")"
  [[ "$c" == 2 ]] || fail "$label: door is open on the odd uplinks (2 of $uplinks), got $c"
  bad="$(awk '$1 == "lorawan.gateways" && ($2 != "2" || $3 != "null")' <<<"$body")"
  [[ -z "$bad" ]] || fail "$label: two gateways reported every uplink: $bad"
  note "$label: all 7 keys stored $uplinks times, values in range, units and 1/0 booleans as declared"
}

# check_range <label> <body> <key> <min> <max> <unit>: every line for <key> is a plain decimal in
# [min, max] and carries exactly <unit> ("null" for none).
check_range() {
  local bad
  bad="$(awk -v k="$3" -v lo="$4" -v hi="$5" -v u="$6" '
    $1 == k && !($2 ~ /^-?[0-9]+(\.[0-9]+)?$/ && $2 + 0 >= lo + 0 && $2 + 0 <= hi + 0 && $3 == u) { print }' <<<"$2")"
  [[ -z "$bad" ]] || fail "$1: '$3' outside [$4, $5] unit $6: $(head -n1 <<<"$bad")"
}

good_body() {
  local i
  for i in $(seq 0 $((uplinks - 1))); do
    printf 'temperature %s C
humidity %s null
battery.voltage 3.1 V
door %s null
lorawan.rssi -80 null
lorawan.snr 8.5 null
lorawan.gateways 2 null
' "$((20 + i)).5" $((50 + i)) $((i % 2))
  done
}

# health <profile> prints "decoded ignored failed suppressed" for the last hour.
health() {
  local payload
  payload="$(dmq 'query($p:String!){payloadDecoderHealth(profileToken:$p,minutes:60){decoded ignored failed suppressed}}' "$(jq -n --arg p "$1" '{p:$p}')")" || return 1
  jq -r '.payloadDecoderHealth | "\(.decoded|floor) \(.ignored|floor) \(.failed|floor) \(.suppressed|floor)"' <<<"$payload"
}

# check_health <label> "<d i f s>" <d> <i> <f> <s>: the four buckets are exclusive, so each is exact.
check_health() {
  [[ "$2" == "$3 $4 $5 $6" ]] || fail "$1: decoder health is '$2' (decoded ignored failed suppressed), wanted '$3 $4 $5 $6'"
  note "$1: health decoded=$3 ignored=$4 failed=$5 suppressed=$6"
}

# health_settles <profile> <d> <i> <f> <s> polls (the replicas flush about once a minute) until it matches.
health_settles() {
  local p="$1" want="$2 $3 $4 $5" got="" i
  for ((i = 0; i < 180; i += 5)); do
    got="$(health "$p" || true)"
    [[ "$got" == "$want" ]] && break
    sleep 5
  done
  check_health "$p" "$got" "$2" "$3" "$4" "$5"
}

# decode_failed_rows <device> prints the device's DECODE_FAILED failed-event errors, one per line.
decode_failed_rows() {
  emq 'query($c:FailedEventSearchCriteria!){failedEvents(criteria:$c){results{reason error deviceToken}}}' \
    "$(jq -n --arg d "$1" '{c:{pageNumber:1,pageSize:100,reason:"DECODE_FAILED",deviceToken:$d}}')" |
    jq -r '.failedEvents.results[] | .error'
}
all_failed_count() {
  emq 'query($c:FailedEventSearchCriteria!){failedEvents(criteria:$c){pagination{totalRecords}}}' \
    "$(jq -n --arg d "$1" '{c:{pageNumber:1,pageSize:1,deviceToken:$d}}')" | jq -r '.failedEvents.pagination.totalRecords'
}

# check_failed_rows <label> <rows> <want>: exactly <want> rows, each naming the reason.
check_failed_rows() {
  local n bad
  n="$(grep -c . <<<"$2" || true)"
  [[ "$n" == "$3" ]] || fail "$1: $n DECODE_FAILED record(s), wanted $3"
  bad="$(grep -v 'no_section' <<<"$2" || true)"
  [[ -z "$bad" ]] || fail "$1: a record does not name no_section: $bad"
  note "$1: $n DECODE_FAILED records, each naming no_section"
}

cmd_selftest() {
  local good name rows
  good="$(good_body)"
  (check_body "good body" "$good") >/dev/null || fail "selftest: the good body was refused"
  declare -A planted=(
    ["a missing key"]="$(sed '0,/^lorawan.snr /{//d;}' <<<"$good")"
    ["a wrong unit"]="${good/temperature 21.5 C/temperature 21.5 F}"
    ["a boolean stored as 2"]="${good/door 1 null/door 2 null}"
    ["a gateway count of 3"]="${good/lorawan.gateways 2 null/lorawan.gateways 3 null}"
    ["a temperature of 0"]="${good/temperature 20.5 C/temperature 0 C}"
    ["a temperature scaled by 10"]="${good/temperature 21.5 C/temperature 215 C}"
    ["a humidity of 5"]="${good/humidity 50 null/humidity 5 null}"
    ["a non-numeric humidity"]="${good/humidity 51 null/humidity NaN null}"
    ["a battery of 31"]="${good/battery.voltage 3.1 V/battery.voltage 31 V}"
    ["a battery without its unit"]="${good/battery.voltage 3.1 V/battery.voltage 3.1 null}"
    ["a humidity given a unit"]="${good/humidity 52 null/humidity 52 C}"
    ["a positive rssi"]="${good/lorawan.rssi -80 null/lorawan.rssi 80 null}"
    ["a snr of 85"]="${good/lorawan.snr 8.5 null/lorawan.snr 85 null}"
    ["a door with a unit"]="${good/door 1 null/door 1 V}"
    ["no readings at all"]=""
  )
  for name in "${!planted[@]}"; do
    if (check_body "planted: $name" "${planted[$name]}") >/dev/null 2>&1; then fail "selftest: check_body accepted $name"; fi
    note "refused: $name"
  done
  (check_health "ok" "5 0 0 0" 5 0 0 0) >/dev/null || fail "selftest: a matching health was refused"
  for name in "4 0 1 0" "5 0 0 1" "0 0 5 0" "6 0 0 0"; do
    if (check_health "planted" "$name" 5 0 0 0) >/dev/null 2>&1; then fail "selftest: check_health accepted '$name'"; fi
    note "refused: health '$name'"
  done
  rows=$'decode_failed: no_section: x\ndecode_failed: no_section: x'
  (check_failed_rows "ok" "$rows" 2) >/dev/null || fail "selftest: matching failed rows were refused"
  if (check_failed_rows "p" "$rows" 3) >/dev/null 2>&1; then fail "selftest: a short count of failed rows was accepted"; fi
  if (check_failed_rows "p" "" 2) >/dev/null 2>&1; then fail "selftest: no failed rows was accepted"; fi
  if (check_failed_rows "p" $'decode_failed: eval_error: x\ndecode_failed: no_section: x' 2) >/dev/null 2>&1; then fail "selftest: a wrong reason was accepted"; fi
  say "selftest: the good inputs pass and every planted fault is refused"
}

# ---------------------------------------------------------------------------
# test
# ---------------------------------------------------------------------------

prepare() {
  need curl; need jq; need kubectl; need go
  k get ns "$namespace" >/dev/null 2>&1 || fail "instance $instance is not up; run 'up' first"
  build_sim
  make_docs
  setup_tenant
  setup_model
}

cmd_test() {
  prepare
  local s

  say "THE GOOD RUN: $uplinks raw frames, no decoded object, decoded by the tenant's decoder"
  s="$(send "$eui" "$uplinks")"; note "$s"
  expect_status "$s" 202 "$uplinks"
  wait_count "$dev" $((uplinks * 7))
  check_body "decoded uplinks" "$(measurements_of "$dev")"
  note "waiting for the decoder's health to flush (about a minute)"
  health_settles "$profile" "$uplinks" 0 0 0

  say "THE PAIRED BROKEN RUN: a decoder that cannot read the same frame"
  s="$(send "$beui" "$uplinks")"; note "$s"
  expect_status "$s" 202 "$uplinks"
  local rows=""
  for _ in $(seq 1 45); do
    rows="$(decode_failed_rows "$bdev")"
    [[ "$(grep -c . <<<"$rows" || true)" -ge "$uplinks" ]] && break
    sleep 2
  done
  check_failed_rows "broken decoder" "$rows" "$uplinks"
  [[ "$(count_of "$bdev")" == 0 ]] || fail "the broken decoder stored measurements"
  health_settles "$bprofile" 0 0 "$uplinks" 0

  say "the kept failures replay through the test door against the corrected decoder"
  local samples sid out
  samples="$(dmq 'query($p:String!){payloadDecoderSamples(profileToken:$p){id reason deviceToken}}' "$(jq -n --arg p "$bprofile" '{p:$p}')")"
  [[ "$(jq '.payloadDecoderSamples | length' <<<"$samples")" -ge 1 ]] || fail "no failure sample was kept: $samples"
  jq -e --arg d "$bdev" 'all(.payloadDecoderSamples[]; .reason == "no_section" and .deviceToken == $d)' >/dev/null <<<"$samples" ||
    fail "a sample does not name no_section for $bdev: $samples"
  sid="$(jq -r '.payloadDecoderSamples[0].id' <<<"$samples")"
  out="$(dmq 'query($i:PayloadDecoderTestInput!){testPayloadDecoder(input:$i){ok readings{key value}}}' \
    "$(jq -n --arg p "$bprofile" --arg d "$good_doc" --arg s "$sid" '{i:{profileToken:$p,document:$d,sampleId:$s}}')")" || fail "the test door refused the sample"
  jq -e '.testPayloadDecoder.ok == true and (.testPayloadDecoder.readings | length) == 4' >/dev/null <<<"$out" ||
    fail "the corrected decoder did not decode the kept sample: $out"
  note "sample $sid decodes to 4 readings under the corrected decoder"
  out="$(dmq 'query($i:PayloadDecoderTestInput!){testPayloadDecoder(input:$i){ok error{reason}}}' \
    "$(jq -n --arg p "$bprofile" --arg s "$sid" '{i:{profileToken:$p,sampleId:$s}}')")" || fail "the test door refused the sample"
  jq -e '.testPayloadDecoder.ok == false and .testPayloadDecoder.error.reason == "no_section"' >/dev/null <<<"$out" ||
    fail "the draft (broken) decoder did not fail the sample as it did live: $out"
  note "and under the profile's own decoder it fails with no_section, as it did live"

  say "NEGATIVE CONTROL: a decoder planted broken on the GOOD profile turns the stored-count check red"
  local before_failed after
  before_failed="$(all_failed_count "$dev")"
  set_decoder "$profile" "$broken_doc"; publish "$profile" >/dev/null
  note "waiting 65 s for event-sources' cached view of the decoder to expire"
  sleep 65
  s="$(send "$eui" 2)"; note "$s"
  expect_status "$s" 202 2
  if reaches "$dev" $((uplinks * 7 + 14)) 40; then fail "CONTROL FAILED: a broken decoder still stored readings"; fi
  red "the stored count did not rise under the broken decoder"
  after=0
  for _ in $(seq 1 20); do
    after="$(all_failed_count "$dev")"
    [[ "$after" -ge $((before_failed + 2)) ]] && break
    sleep 2
  done
  [[ "$after" -ge $((before_failed + 2)) ]] || fail "CONTROL FAILED: the broken decoder's failures were not recorded ($before_failed -> $after)"
  red "and its 2 failures were recorded as failed events"
  say "the control's other half: restore the decoder and the same check goes green"
  set_decoder "$profile" "$good_doc"; publish "$profile" >/dev/null
  note "waiting 65 s for the cached view to expire"
  sleep 65
  s="$(send "$eui" 2)"; note "$s"
  expect_status "$s" 202 2
  wait_count "$dev" $((uplinks * 7 + 14))
  say "PASS: decoded and stored once, health exact, paired broken run recorded, control red then green"
}

# ---------------------------------------------------------------------------
# mixed: the device-management OUTAGE window (not a mixed-version roll)
# ---------------------------------------------------------------------------
#
# What this proves is an OUTAGE, and only that: uplinks accepted while nothing reads the stream are
# parked and drained exactly once, never failed. It does not prove an old pod and a new pod can
# overlap, and a minor upgrade can pass it while an old pod restarting mid-roll cannot start (see
# below for why that half is not built).
#
# During a rolling upgrade there are moments with new event-sources and NO device-management that
# knows the integration-uplinks stream. This phase makes that moment exactly: device-management is
# scaled to zero while event-sources, which already learned that the type has a decoder, keeps
# accepting uplinks.
#
# It does NOT run the previous release's device-management image beside the new one, and that is
# deliberate. The previous image was tried: it exits at startup because the broker's streams are
# administered by the chart's pre-upgrade hook (a service only VERIFIES them) and the hook has
# already added the integration-uplinks subjects to `max-deliveries`, so a pod of the old release
# that RESTARTS after the hook refuses to start. Pods already running keep running; one that
# restarts mid-rollout does not. That is the design, and it is why an old pod can never be handed
# the uplink stream by accident; it also means the old-image half of this test cannot be built
# without disarming the very check it would be testing.

dm_replicas() { k -n "$namespace" get deploy device-management -o jsonpath='{.spec.replicas}'; }

cmd_mixed() {
  prepare
  local replicas s base failed_base
  say "before the outage: the decoder works, and event-sources has learned that the type has one"
  base="$(count_of "$dev")"
  s="$(send "$eui" 1)"; note "$s"; expect_status "$s" 202 1
  # `test` may or may not have run: only a RISE of 7 is claimed, never an absolute count.
  reaches "$dev" $((base + 7)) 120 || fail "the decoder did not store the warm-up uplink"
  sleep 12
  base="$(count_of "$dev")"
  failed_base="$(all_failed_count "$dev")"

  replicas="$(dm_replicas)"
  # The selector must match something, or "no pods left" below would be true of any label.
  [[ "$(k -n "$namespace" get pods -l devicechain.io/functional-area=device-management -o name | grep -c . || true)" -ge 1 ]] ||
    fail "the device-management pod selector matches no pod"
  say "scaling device-management ($replicas replica(s)) to zero: nothing reads the integration-uplinks stream"
  k -n "$namespace" scale deploy/device-management --replicas=0 >/dev/null
  # A pod that survived would read the stream and make the whole phase pass without an outage.
  dm_restore_to="$replicas"
  local left
  for _ in $(seq 1 60); do
    left="$(k -n "$namespace" get pods -l devicechain.io/functional-area=device-management -o name 2>/dev/null | grep -c . || true)"
    [[ "$left" == 0 ]] && break
    sleep 2
  done
  [[ "$left" == 0 ]] || fail "$left device-management pod(s) still run after scaling to zero: there is no outage to test"

  say "(a) uplinks for a decoder's type meanwhile: accepted, parked, not stored, not failed"
  s="$(send "$eui" 3)"; note "$s"
  expect_status "$s" 202 3
  sleep 25
  # The read path is device-management's too, so the counts are read once it is back.
  say "device-management comes back"
  k -n "$namespace" scale deploy/device-management --replicas="$replicas" >/dev/null
  dm_restore_to=""
  k -n "$namespace" rollout status deploy/device-management --timeout=300s >/dev/null || fail "device-management did not come back"
  tenant_login
  say "(b) the parked uplinks are decoded and stored exactly once, and nothing was recorded as failed"
  reaches "$dev" $((base + 21)) 240 || fail "the parked uplinks were not decoded after device-management came back (has $(count_of "$dev"), wanted $((base + 21)))"
  sleep 12
  [[ "$(count_of "$dev")" == $((base + 21)) ]] || fail "after the outage $dev holds $(count_of "$dev"), wanted exactly $((base + 21))"
  [[ "$(all_failed_count "$dev")" == "$failed_base" ]] || fail "an uplink parked during the outage was recorded as failed"
  note "$dev rose by exactly 21 (3 uplinks x 7 keys); no failed event"
  say "PASS (mixed): accepted, parked, never failed, drained exactly once"
}

# ---------------------------------------------------------------------------
# mqtt: a device's own payload over its OWN MQTT connection
# ---------------------------------------------------------------------------
#
# Everything above reaches the decoder over HTTP, through a network server. This phase proves the
# second carrier: a device publishing its own bytes to {instance}/{tenant}/devices/{device}/uplink on
# the platform broker, a topic granted only once the operator turns on
# instance.config.infrastructure.nats.mqttDeviceUplinks, in an upgrade of its own (as the device
# protocol reference says to). Every check is shown failing on a defect planted for it, and each
# reads its SUBJECT: the broker's verdict on that client and that topic, that device's stored
# readings, the failed-decode record and the counter naming its cause. Never a bystander.
#
#   M1  OFF (as bootstrapped): a device publishing to its own uplink topic is REFUSED by the broker
#       (a Publish Violation naming that client and that subject), and nothing is carried or
#       stored, while the same device's ordinary event is stored. Planted: the setting ON; the same
#       check, same device, same topic, goes red.
#   M2  ON: raw frames, with no envelope, are decoded and stored EXACTLY: every key, value and unit
#       of every frame, and nothing else. Planted: a decoder with a scale defect; the check goes
#       red, then green again once the decoder is restored.
#   M3  ON: a device publishing to ANOTHER device's uplink topic is refused by the broker, and the
#       other device stores nothing. Planted: the topic's owner publishing the same bytes; the same
#       check goes red.
#   M4  ON: a frame over the frame decoder's 4 KiB lands on failed-decode, counted under its cause
#       (device_uplink_messages_total{result="too_large"}); so does a payload from a device whose
#       type has no decoder (result="no_decoder"); neither is stored. Planted: a frame of exactly
#       4 KiB (the limit, which is allowed), and a device whose type has a decoder; each check goes
#       red.
#   M5  ON: a device-management that HANGS (every packet from event-sources to it dropped, so a
#       decoder lookup waits out its deadline) does not stall ordinary event decoding: while a cold device
#       floods its uplink topic, another device's ordinary events are stored inside the window.
#       Planted: event-sources built from this tree with the lookup moved back onto the decode
#       worker (the shape before that was fixed); the same check goes red, then green on the image
#       under test.
#   M6  OFF again, in an upgrade of its own: a device that reconnects is refused on its uplink topic
#       again. A connection held open across that upgrade keeps the grant it was minted until it
#       reconnects (the docs say so); the rig reports what that connection's publish met.
#
# The device is a pod in the instance namespace running mosquitto_pub (the image the x509 rig
# pins), which reaches the broker's MQTT listener through its Service. Each run names its devices
# afresh (a run id), so a rerun against the same instance starts from empty stores.

mq_profile="dec-mq"; mq_type="dec-mq"
plain_profile="dec-plain"; plain_type="dec-plain"
device_pod="decrig-device"
device_image="${DC_DEC_RIG_DEVICE_IMAGE:-eclipse-mosquitto:2@sha256:860aeb5d63ce509508f6dcfb2bc8a1c1dd2bc60555b562b45ce90e52d8dabb0f}"
# M5: how long ordinary events may take to be stored while the lookups hang, and the flood's size.
f1_window="${DC_DEC_RIG_F1_WINDOW:-30}"
f1_flood="${DC_DEC_RIG_F1_FLOOD:-200}"
f1_events=8
secrets_dir="$state_dir/mq-secrets"
broker_host=""; mqtt_port=""; es_metrics_port=""
mqa=""; mqb=""; mqctl=""; mqcap=""; mqoff=""; plain=""

# MSYS_NO_PATHCONV: Git Bash would otherwise rewrite the in-pod paths (/bin/sleep, /tmp/...) into
# Windows ones on their way to kubectl.
kn() { MSYS_NO_PATHCONV=1 k -n "$namespace" "$@"; }
mq() { kn exec "$device_pod" -- "$@"; }
# The broker's MQTT listener is TLS on every instance; its certificate names the instance, not the
# Service the pod dials, so the CA is checked and the name is not.
mq_tls="--cafile /tmp/broker-ca.pem --insecure"

# nats_admin runs the nats CLI in the broker's nats-box as the broker administrator; the login
# travels as the CLI's environment, never as an argument.
nats_admin() {
  local pw
  pw="$(kn get secret "dci-$instance-nats-broker-admin" -o jsonpath='{.data.password}' | base64 -d)"
  [[ -n "$pw" ]] || fail "the broker administrator's Secret holds no password"
  kn exec deploy/dc-nats-box -- env NATS_USER=dc-broker-admin-g1 NATS_PASSWORD="$pw" \
    nats --server nats://dc-nats:4222 --inbox-prefix _INBOX_broker-admin "$@"
}
uplink_captured() {
  grep -qxF -- "$instance.*.devices.*.uplink" <<<"$(nats_admin stream info "${instance}_device-events-capture" --json | jq -r '.config.subjects[]')"
}

# ---- the broker's verdict --------------------------------------------------------------------

broker_pods() { kn get pods -l app.kubernetes.io/name=nats,app.kubernetes.io/component=nats -o name; }
# violations <since RFC3339> <client id> <subject>: the Publish Violations the broker logged since
# then for THAT client publishing to THAT subject.
violations() {
  local p n=0 c
  for p in $(broker_pods); do
    c="$(kn logs "$p" -c nats --since-time="$1" 2>/dev/null | grep -F -- "\"$2\"" |
      grep -cF -- "Publish Violation - Subject \"$3\"" || true)"
    n=$((n + c))
  done
  printf '%s' "$n"
}

# ---- event-sources' own accounting ---------------------------------------------------------

# es_pods: the live replicas only. A terminating replica's counters vanish with it, and a sum that
# loses them in the middle of a check goes DOWN, which reads as "nothing happened".
es_pods() {
  kn get pods -l devicechain.io/functional-area=event-sources -o json |
    jq -r '.items[] | select(.metadata.deletionTimestamp == null) | .metadata.name'
}
# es_metric <name> <label filter or ""> sums a counter over every event-sources replica.
es_metric() {
  local p total=0 v
  for p in $(es_pods); do
    v="$(MSYS_NO_PATHCONV=1 k get --raw "/api/v1/namespaces/$namespace/pods/$p:$es_metrics_port/proxy/metrics" |
      awk -v n="$1" -v f="$2" 'substr($0, 1, 1) != "#" && ($1 ~ ("_" n "{") || $1 ~ ("_" n "$")) && (f == "" || index($1, f) > 0) { s += $2 }
        END { printf "%d", s }')"
    total=$((total + v))
  done
  printf '%s' "$total"
}
uplink_result() { es_metric device_uplink_messages_total "result=\"$1\""; }
uplink_all() { es_metric device_uplink_messages_total ""; }
failed_decode_total() { es_metric total_msg_failed_decode ""; }
# failed_decode_stream prints the records the failed-decode stream holds on THIS tenant's subject. (The
# broker administrator may count a stream's subjects but not read its messages, so the record's
# bytes are not read back; and the record carries no cause of its own: the cause is the counter.)
failed_decode_stream() {
  nats_admin stream subjects "${instance}_failed-decode" --json 2>/dev/null |
    jq -r --arg s "$instance.$tenant.failed-decode" '.[$s] // 0'
}

# ---- the model ---------------------------------------------------------------------------------

# The simulator's frame decoder keys its section on fPort, which a device's own payload does not
# have (it is not LoRaWAN): the MQTT profile's section keys on the layout byte alone.
mq_doc() { jq -c '.sections[0].when = "u8(frame, 0) == 1" | .vectors = []' <<<"$good_doc"; }
# The planted scale defect: temperature read in hundredths instead of tenths.
mq_scale_doc() {
  jq -c '.sections[0].outputs |= map(if .key == "temperature" then .expr = "double(i16be(frame, 1)) / 100.0" else . end)' <<<"$(mq_doc)"
}

secret_of() { cat "$secrets_dir/$1"; }

# mq_device <token> <type>: a device with an MQTT_BASIC credential whose id is its token.
mq_device() {
  local d="$1" out
  dmq 'mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}' \
    "$(jq -n --arg t "$d" --arg ty "$2" '{r:{token:$t,name:$t,deviceTypeToken:$ty}}')" >/dev/null || fail "createDevice $d"
  mkdir -p "$secrets_dir"
  openssl rand -hex 24 | tr -d '\n' >"$secrets_dir/$d"
  out="$(dmq 'mutation($r:DeviceCredentialCreateRequest){createDeviceCredential(request:$r){token}}' \
    "$(jq -n --arg t "$d-mqtt" --arg d "$d" --rawfile s "$secrets_dir/$d" \
      '{r:{token:$t,deviceToken:$d,credentialType:"MQTT_BASIC",credentialId:$d,credentialValue:$s,enabled:true}}')" 2>&1)" ||
    fail "createDeviceCredential $d: $out"
}

setup_mqtt_model() {
  say "the MQTT model: a frame-decoder profile, a profile with no decoder, devices with MQTT credentials (run $mq_run)"
  create_profile "$mq_profile" "$(mq_doc)"; publish "$mq_profile" >/dev/null
  tolerate_exists dmq 'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
    "$(jq -n --arg t "$plain_profile" '{r:{token:$t,name:$t}}')" >/dev/null
  tolerate_exists dmq 'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){token}}' \
    "$(jq -n --arg p "$plain_profile" '{r:{token:($p+"-temperature"),deviceProfileToken:$p,metricKey:"temperature",name:"temperature",dataType:"DOUBLE"}}')" >/dev/null
  publish "$plain_profile" >/dev/null
  local t d
  for t in "$mq_type|$mq_profile" "$plain_type|$plain_profile"; do
    tolerate_exists dmq 'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
      "$(jq -n --arg t "${t%%|*}" --arg p "${t#*|}" '{r:{token:$t,name:$t,profileToken:$p}}')" >/dev/null
  done
  mqa="mq-a-$mq_run"; mqb="mq-b-$mq_run"; mqctl="mq-ctl-$mq_run"; mqcap="mq-cap-$mq_run"; mqoff="mq-off-$mq_run"
  plain="mq-plain-$mq_run"
  for d in "$mqa" "$mqb" "$mqctl" "$mqcap" "$mqoff" "mq-cold1-$mq_run" "mq-cold2-$mq_run" "mq-cold3-$mq_run"; do
    mq_device "$d" "$mq_type"
  done
  mq_device "$plain" "$plain_type"
}

start_device_pod() {
  kn delete pod "$device_pod" --ignore-not-found --wait=true >/dev/null
  kn run "$device_pod" --image="$device_image" --restart=Never --labels=app.kubernetes.io/name=decrig-device \
    --command -- /bin/sleep infinity >/dev/null
  kn wait --for=condition=Ready "pod/$device_pod" --timeout=180s >/dev/null || fail "the device pod did not start"
  local ca
  ca="$(kn get secret -o json | jq -r '[.items[] | select(.metadata.name | endswith("nats-tls")) | .data["ca.crt"] // empty][0] // empty')"
  mq sh -c "printf '%s' '$ca' | base64 -d >/tmp/broker-ca.pem"
  mq sh -c 'grep -q "BEGIN CERTIFICATE" /tmp/broker-ca.pem' || fail "no *nats-tls secret with a ca.crt in $namespace: the broker's CA cannot be read"
}

discover() {
  broker_host="$(kn get svc -o json | jq -r '[.items[] | select(.spec.type == "ClusterIP" and .spec.clusterIP != "None")
    | select(any(.spec.ports[]?; .name == "mqtt")) | .metadata.name][0] // empty')"
  [[ -n "$broker_host" ]] || fail "no Service in $namespace names an mqtt port"
  mqtt_port="$(kn get svc "$broker_host" -o json | jq -r '[.spec.ports[] | select(.name == "mqtt")][0].port')"
  es_metrics_port="$(kn get pods -l devicechain.io/functional-area=event-sources -o json |
    jq -r '[.items[0].spec.containers[0].ports[]? | select(.name | test("metric"))][0].containerPort // empty')"
  [[ -n "$es_metrics_port" ]] || es_metrics_port=8080
  note "broker MQTT at $broker_host:$mqtt_port; event-sources metrics on :$es_metrics_port"
  [[ "$(uplink_all)" =~ ^[0-9]+$ ]] || fail "event-sources serves no device_uplink_messages_total"
}

# ---- devices -------------------------------------------------------------------------------------

octal() { local b; for b in "$@"; do printf '\\%03o' "$b"; done; }
# put_frame <file> <temperature, tenths> <humidity> <voltage, tenths> <door>: the simulator's layout.
put_frame() {
  local t="$2"
  ((t >= 0)) || t=$((65536 + t))
  mq sh -c "printf '$(octal 1 $((t >> 8)) $((t & 255)) "$3" "$4" "$5")' >/tmp/$1"
}
# expect_frame <temperature, tenths> <humidity> <voltage, tenths> <door>: the four readings it stores.
expect_frame() {
  awk -v t="$1" -v h="$2" -v v="$3" -v d="$4" 'BEGIN {
    printf "temperature %s C\nhumidity %s null\nbattery.voltage %s V\ndoor %s null\n", t / 10, h, v / 10, d }'
}

PUB_RC=0; PUB_OUT=""
# pub <client device> <topic device> <leaf> <file in the pod>: one connection, one QoS 1 publish.
pub() {
  PUB_RC=0
  # shellcheck disable=SC2086
  PUB_OUT="$(mq timeout 25 mosquitto_pub $mq_tls -h "$broker_host" -p "$mqtt_port" -V mqttv311 -i "$instance:$tenant:$1" \
    -u "$tenant:$1" -P "$(secret_of "$1")" -q 1 -t "$instance/$tenant/devices/$2/$3" -f "/tmp/$4" 2>&1)" || PUB_RC=$?
}
pub_event() {
  mq sh -c "printf '%s' '{\"device\":\"$1\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$2\"}}]}}' >/tmp/ev-$1"
  pub "$1" "$1" events "ev-$1"
}
now_rfc3339() { date -u +%Y-%m-%dT%H:%M:%SZ; }
sorted() { sed '/^$/d' <<<"$1" | sort; }
lines() { sed '/^$/d' <<<"$1" | wc -l | tr -d ' '; }

# ---- the checks ----------------------------------------------------------------------------------
# Each prints what it saw and RETURNS non-zero on a violation; none calls fail, so a planted defect
# runs the very same function and is shown red.

# check_refused <client> <topic device> <file>: the broker refuses this client's publish to that
# device's uplink topic, and nothing of it is carried or stored.
check_refused() {
  local c="$1" td="$2" since s0 u0 v s1 u1 subj
  subj="$instance.$tenant.devices.$td.uplink"
  s0="$(measurements_of "$td")"; u0="$(uplink_all)"; since="$(now_rfc3339)"
  sleep 1
  pub "$c" "$td" uplink "$3"
  sleep 15
  v="$(violations "$since" "$instance:$tenant:$c" "$subj")"
  s1="$(measurements_of "$td")"; u1="$(uplink_all)"
  echo "    $c -> $subj: mosquitto_pub exit $PUB_RC; broker Publish Violations: $v; uplinks carried +$((u1 - u0)); $td stored +$(($(lines "$s1") - $(lines "$s0")))"
  [[ "$v" -ge 1 ]] || { echo "    VIOLATION: the broker logged no Publish Violation for $c on $subj"; return 1; }
  [[ "$u1" == "$u0" ]] || { echo "    VIOLATION: event-sources carried an uplink"; return 1; }
  [[ "$(sorted "$s1")" == "$(sorted "$s0")" ]] || { echo "    VIOLATION: $td stored readings"; return 1; }
}

# check_stored_exactly <device> <want, one "name value unit" per line> <seconds>: the device's stored
# readings become exactly that multiset and stay so through a settling period.
check_stored_exactly() {
  local d="$1" want got i
  want="$(sorted "$2")"
  for ((i = 0; i < $3; i += 3)); do
    got="$(sorted "$(measurements_of "$d")")"
    [[ "$(lines "$got")" -ge "$(lines "$want")" ]] && break
    sleep 3
  done
  sleep 10
  got="$(sorted "$(measurements_of "$d")")"
  if [[ "$got" != "$want" ]]; then
    echo "    VIOLATION: $d stores $(lines "$got") readings, wanted exactly $(lines "$want"); differences (< stored, > wanted):"
    diff <(printf '%s\n' "$got") <(printf '%s\n' "$want") | grep '^[<>]' | sed -n '1,8s/^/      /p'
    return 1
  fi
  echo "    $d stores exactly the $(lines "$want") readings wanted"
}

# check_failed_decode <device> <file> <result>: the payload is not stored; it is recorded once on
# failed-decode (the stream gains one record on the tenant's own subject); and its cause is counted
# once under <result>.
check_failed_decode() {
  local d="$1" f="$2" r="$3" r0 fd0 st0 s0 r1 fd1 st1 s1 sent
  local a0 a1
  r0="$(uplink_result "$r")"; fd0="$(failed_decode_total)"; st0="$(failed_decode_stream)"; s0="$(measurements_of "$d")"; a0="$(uplink_all)"
  sent="$(mq sh -c "wc -c </tmp/$f" | tr -d ' \r')"
  pub "$d" "$d" uplink "$f"
  for _ in $(seq 1 15); do
    [[ "$(uplink_result "$r")" -gt "$r0" ]] && break
    sleep 2
  done
  sleep 10
  r1="$(uplink_result "$r")"; fd1="$(failed_decode_total)"; st1="$(failed_decode_stream)"; s1="$(measurements_of "$d")"; a1="$(uplink_all)"
  echo "    $d published $sent bytes (exit $PUB_RC): result=\"$r\" +$((r1 - r0)); total_msg_failed_decode +$((fd1 - fd0)); failed-decode records on $instance.$tenant.failed-decode +$((st1 - st0)); $d stored +$(($(lines "$s1") - $(lines "$s0")))"
  [[ $((r1 - r0)) == 1 ]] || { echo "    VIOLATION: the cause $r was counted $((r1 - r0)) times, wanted 1"; return 1; }
  # The failed-decode record names no device and the administrator cannot read it back, so what ties
  # the count to THIS payload is that it was the only uplink event-sources handled in the window.
  [[ $((a1 - a0)) == 1 ]] || { echo "    VIOLATION: event-sources handled $((a1 - a0)) uplinks in the window, so the count is not this payload's alone"; return 1; }
  [[ $((fd1 - fd0)) == 1 && $((st1 - st0)) == 1 ]] || { echo "    VIOLATION: the payload was not recorded exactly once on the tenant's failed-decode subject"; return 1; }
  [[ "$(sorted "$s1")" == "$(sorted "$s0")" ]] || { echo "    VIOLATION: $d stored readings"; return 1; }
}

# ---- M5: a hanging device-management --------------------------------------------------------------
#
# The hang is a DROP, on the kind node, of every packet from event-sources' pods to device-management's
# (a NetworkPolicy admitting every peer but event-sources was tried first, and on kind it did not stop
# a single lookup). event-sources' decoder lookups then wait out their
# deadline, as they would against a device-management that stopped answering, while the auth callout,
# event resolution and the API keep working: ordinary events are STORED throughout, so the check
# reads them end to end.

dm_hung=0
hang_rules="$state_dir/dm-hang.rules"
node_exec() { docker exec "$cluster-control-plane" "$@"; }
pod_ips() { kn get pods -l "devicechain.io/functional-area=$1" -o jsonpath='{range .items[*]}{.status.podIP}{"\n"}{end}'; }
dm_hang_on() {
  local es dm
  dm_hang_off
  dm_hung=1
  for es in $(pod_ips event-sources); do
    for dm in $(pod_ips device-management); do
      node_exec iptables -I FORWARD 1 -s "$es" -d "$dm" -p tcp -j DROP || fail "could not install the hang rule"
      printf '%s %s\n' "$es" "$dm" >>"$hang_rules"
    done
  done
  [[ -s "$hang_rules" ]] || fail "no event-sources or device-management pod to put a hang between"
}
dm_hang_off() {
  local es dm
  if [[ -f "$hang_rules" ]]; then
    while read -r es dm; do node_exec iptables -D FORWARD -s "$es" -d "$dm" -p tcp -j DROP 2>/dev/null || true; done <"$hang_rules"
    rm -f "$hang_rules"
  fi
  dm_hung=0
}

# hang_bites <cold device>: that device's uplink meets a lookup that does not answer.
hang_bites() {
  local p0
  p0="$(uplink_result plan_unavailable)"
  mq sh -c "printf 'probe' >/tmp/probe"
  for _ in $(seq 1 6); do
    pub "$1" "$1" uplink probe
    sleep 10
    [[ "$(uplink_result plan_unavailable)" -gt "$p0" ]] && return 0
  done
  return 1
}

# check_events_flow <round> <cold device>: while the cold device floods its uplink topic (and every
# lookup for it hangs), the plain device's ordinary events are stored inside the window.
check_events_flow() {
  local round="$1" cold="$2" base want got i n p0 p1 t0
  base="$(measurements_of "$plain")"
  mq sh -c "i=0; while [ \$i -lt $f1_flood ]; do echo flood-$round-\$i; i=\$((i + 1)); done >/tmp/flood"
  p0="$(uplink_result plan_unavailable)"
  # The flood goes first, so the events queue behind it on the capture consumer.
  mq sh -c "timeout 60 mosquitto_pub $mq_tls -h '$broker_host' -p '$mqtt_port' -V mqttv311 -i '$instance:$tenant:$cold' -u '$tenant:$cold' \
    -P '$(secret_of "$cold")' -q 1 -t '$instance/$tenant/devices/$cold/uplink' -l </tmp/flood" ||
    echo "    (the flood's publisher exited non-zero)"
  t0="$(date +%s)"
  local deadline=$((t0 + f1_window)) took
  want="$base"
  for i in $(seq 1 "$f1_events"); do
    pub_event "$plain" "$round$i.25"
    want+=$'\n'"temperature $round$i.25 null"
  done
  want="$(sorted "$want")"
  while :; do
    got="$(sorted "$(measurements_of "$plain")")"
    [[ "$got" == "$want" || "$(date +%s)" -ge "$deadline" ]] && break
    sleep 2
  done
  took=$(($(date +%s) - t0))
  p1="$(uplink_result plan_unavailable)"
  n=$(($(lines "$got") - $(lines "$base")))
  echo "    $f1_flood uplinks flooded from $cold; $n of $f1_events ordinary events stored $took s after the first was published; lookups that hung meanwhile: $((p1 - p0))"
  [[ $((p1 - p0)) -ge 1 ]] || { echo "    VIOLATION: no lookup hung, so device-management was not hanging for event-sources"; return 1; }
  [[ "$got" == "$want" && "$took" -le "$f1_window" ]] || { echo "    VIOLATION: the ordinary events were not all stored within ${f1_window}s while the lookups hung"; return 1; }
}

# drain_round <accepted before the round> <readings the plain device holds once its events are in>:
# once device-management answers again, the round's flood is carried and its ordinary events are
# stored, so the next round starts from a quiet consumer and a settled store.
drain_round() {
  local i
  for i in $(seq 1 60); do
    [[ $(($(uplink_result accepted) - $1)) -ge "$f1_flood" && "$(lines "$(measurements_of "$plain")")" -ge "$2" ]] && break
    sleep 3
  done
  note "after the hang: $(($(uplink_result accepted) - $1)) of the round's uplinks carried; $plain holds $(lines "$(measurements_of "$plain")") of $2 readings"
  [[ $(($(uplink_result accepted) - $1)) -ge "$f1_flood" ]] || fail "the flood was not carried once device-management answered again"
  [[ "$(lines "$(measurements_of "$plain")")" -ge "$2" ]] || fail "the round's ordinary events were not stored once device-management answered again"
}

# The planted M5 defect: event-sources built from THIS tree with the plan lookup moved back onto the
# decode worker. It is patched in a detached worktree, so the tree under test is never edited, and
# the patch must apply exactly: a defect that did not plant is a control that cannot fail.
planted_es_image=""
build_planted_es() {
  local wt="$rig_tmp/planted-wt" image tag
  image="$(kn get deploy event-sources -o jsonpath='{.spec.template.spec.containers[0].image}')"
  tag="$(cat "$state_dir/image-tag")-f1planted"
  planted_es_image="${image%:*}:$tag"
  git -C "$repo_root" worktree add --detach "$(winpath "$wt")" HEAD >/dev/null 2>&1 || fail "could not add the planted worktree"
  planted_wt="$wt"
  # `up --build` built the WORKING tree, so the control starts from it too: the planted image must
  # differ from the image under test by the planted change alone.
  if ! git -C "$repo_root" diff --quiet HEAD -- backend; then
    git -C "$repo_root" diff HEAD -- backend | git -C "$wt" apply || fail "could not carry the working tree's changes into the planted worktree"
  fi
  perl -0777 -i -pe '
    $n = s/\tif !u\.take\(\) \{\n\t\tu\.busy\(raw\)\n\t\treturn\n\t\}\n\tu\.inflight\.Add\(1\)\n\tgo func\(\) \{\n\t\tdefer u\.inflight\.Done\(\)\n\t\tdefer u\.release\(\)\n(\t\tctx, cancel := context\.WithTimeout\(context\.Background\(\), planLookupTimeout\)\n\t\tlook, err := u\.rt\.Plans\.Lookup)/\tfunc() {\n$1/;
    $m = s/u\.carry\(source, raw, mark, look, true\)\n\t\}\(\)/u.carry(source, raw, mark, look, false)\n\t}()/;
    die "planted: $n and $m substitutions, wanted 1 and 1\n" unless $n == 1 && $m == 1;' \
    "$wt/backend/services/event-sources/processor/mqtt_device_uplink.go" || fail "the M5 defect could not be planted (the source moved)"
  say "building event-sources with the M5 defect planted, as $planted_es_image"
  (cd "$wt/backend/services/event-sources" && go build ./... &&
    KO_DOCKER_REPO="${image%:*}" KO_CONFIG_PATH="$(winpath "$wt/.ko.yaml")" \
      ko build --bare --tags "$tag" --platform linux/amd64 ./ >/dev/null) || fail "the planted event-sources did not build"
  remove_planted_wt
}
planted_wt=""
remove_planted_wt() {
  [[ -n "$planted_wt" ]] || return 0
  git -C "$repo_root" worktree remove --force "$(winpath "$planted_wt")" >/dev/null 2>&1 || true
  git -C "$repo_root" worktree prune >/dev/null 2>&1 || true
  planted_wt=""
}
es_image_restore=""
set_es_image() {
  kn set image deploy/event-sources "$(kn get deploy event-sources -o jsonpath='{.spec.template.spec.containers[0].name}')=$1" >/dev/null
  kn rollout status deploy/event-sources --timeout=300s >/dev/null || fail "event-sources did not roll to $1"
  # The replaced replica must be GONE, not terminating: until then it may still read the capture
  # stream, and the hang (set by pod address) and the counters must cover exactly the live ones.
  local i
  for i in $(seq 1 90); do
    [[ "$(kn get pods -l devicechain.io/functional-area=event-sources -o json | jq '[.items[] | select(.metadata.deletionTimestamp != null)] | length')" == 0 ]] && break
    sleep 2
  done
  discover >/dev/null
}

# ---- the upgrades that turn the setting on and off ----------------------------------------------

set_device_uplinks() {
  local f="$rig_tmp/uplinks-$1.yaml"
  printf 'instance:\n  config:\n    infrastructure:\n      nats:\n        mqttDeviceUplinks: %s\n' "$1" >"$f"
  say "dcctl upgrade: instance.config.infrastructure.nats.mqttDeviceUplinks=$1, an upgrade of its own"
  [[ -x "$dcctl" ]] || build_dcctl
  (cd "$repo_root" && "$dcctl" upgrade local "$instance" --yes --version "$(cat "$state_dir/image-tag")" --values "$(winpath "$f")" 2>&1 | redact |
    tail -n 4) || fail "the upgrade to mqttDeviceUplinks=$1 failed"
  kn rollout status deploy/device-management --timeout=300s >/dev/null || fail "device-management did not settle"
  kn rollout status deploy/event-sources --timeout=300s >/dev/null || fail "event-sources did not settle"
  tenant_login
  discover
}

# A connection held open across an upgrade: mosquitto_pub -l publishes one payload per line read
# from a fifo that a sleeping writer keeps open, so the session lives until the rig ends it.
# Both background processes give up every descriptor of the exec session (stdin, stdout and stderr),
# or kubectl exec waits for them and the rig hangs here for good.
hold_open() {
  mq timeout 20 sh -c "rm -f /tmp/held /tmp/held.log; mkfifo /tmp/held
    mosquitto_pub $mq_tls -h '$broker_host' -p '$mqtt_port' -V mqttv311 -i '$instance:$tenant:$1:held' -u '$tenant:$1' \
      -P '$(secret_of "$1")' -q 1 -k 30 -t '$instance/$tenant/devices/$1/uplink' -l </tmp/held >/tmp/held.log 2>&1 &
    sleep 100000 </dev/null >/tmp/held 2>/dev/null &
    exit 0" >/dev/null || fail "could not open the held connection"
  sleep 3
  held_alive || fail "the held connection did not stay open: $(mq cat /tmp/held.log)"
}
held_alive() { mq pgrep mosquitto_pub >/dev/null 2>&1; }
# A write to the fifo blocks while nothing reads it (the publisher gone), so it is bounded.
held_send() { mq timeout 10 sh -c "printf '%s\n' '$1' >/tmp/held" || note "(the held connection's publisher no longer reads its fifo)"; }
held_close() { mq sh -c 'pkill mosquitto_pub; pkill -f "sleep 100000"; sleep 2; true' >/dev/null 2>&1 || true; }

# ---------------------------------------------------------------------------------------------------

mq_run=""
cmd_mqtt() {
  prepare
  mq_run="$(date -u +%H%M%S)"
  setup_mqtt_model
  start_device_pod
  discover
  local out want since v

  # The phase starts from the bootstrapped default; a rerun after an interrupted one turns it off first.
  if uplink_captured; then set_device_uplinks false; fi

  say "M1 OFF: a device's own uplink topic is refused by the broker"; tenant_login
  pub_event "$mqa" 1.5
  [[ "$PUB_RC" == 0 ]] || fail "M1: $mqa could not publish an ordinary event (exit $PUB_RC): $PUB_OUT"
  check_stored_exactly "$mqa" "temperature 1.5 C" 60 >/dev/null ||
    fail "M1: $mqa's ordinary event was not stored: its connection is broken, so a refusal would prove nothing"
  put_frame f1 214 47 32 1
  out="$(check_refused "$mqa" "$mqa" f1)" || fail "M1: $out"
  printf '%s\n' "$out"
  note "PASS M1: refused by the broker and nothing carried or stored; the same device's event was stored"

  set_device_uplinks true
  uplink_captured || fail "the capture stream does not hold $instance.*.devices.*.uplink after the upgrade"
  note "the capture stream now holds $instance.*.devices.*.uplink"

  say "M1 planted (the setting ON): the same check, same device, same topic, must go red"; tenant_login
  if out="$(check_refused "$mqa" "$mqa" f1)"; then printf '%s\n' "$out"; fail "CONTROL FAILED: M1's check passed with the grant in place"; fi
  printf '%s\n' "$out"; red "M1's check fails once the device holds the grant"

  say "M2 ON: raw frames decoded and stored exactly"; tenant_login
  put_frame f2 -55 63 30 0; put_frame f3 1 100 34 1; put_frame f4 -1234 0 29 0
  pub "$mqa" "$mqa" uplink f2; pub "$mqa" "$mqa" uplink f3; pub "$mqa" "$mqa" uplink f4
  want="temperature 1.5 C"$'\n'"$(expect_frame 214 47 32 1)"$'\n'"$(expect_frame -55 63 30 0)"
  want+=$'\n'"$(expect_frame 1 100 34 1)"$'\n'"$(expect_frame -1234 0 29 0)"
  out="$(check_stored_exactly "$mqa" "$want" 120)" || fail "M2: $out"
  printf '%s\n' "$out"
  note "PASS M2: 4 frames (one of them the M1 control's) -> 16 readings beside the event, every value and unit exact"

  say "M2 planted: a decoder with a scale defect on the profile"; tenant_login
  set_decoder "$mq_profile" "$(mq_scale_doc)"; publish "$mq_profile" >/dev/null
  note "waiting 65 s for event-sources' cached view of the decoder to expire"
  sleep 65
  put_frame c1 237 51 33 0
  pub "$mqctl" "$mqctl" uplink c1
  if out="$(check_stored_exactly "$mqctl" "$(expect_frame 237 51 33 0)" 60)"; then printf '%s\n' "$out"; fail "CONTROL FAILED: M2's check passed under a scale defect"; fi
  printf '%s\n' "$out"; red "M2's check reads the stored values"
  set_decoder "$mq_profile" "$(mq_doc)"; publish "$mq_profile" >/dev/null
  note "restored; waiting 65 s for the cached view to expire"
  sleep 65
  want="$(measurements_of "$mqctl")"$'\n'"$(expect_frame 238 52 33 1)"
  put_frame c2 238 52 33 1
  pub "$mqctl" "$mqctl" uplink c2
  out="$(check_stored_exactly "$mqctl" "$want" 60)" || fail "M2 (restored): $out"
  printf '%s\n' "$out"; note "and green again with the decoder restored"

  say "M3 ON: a device cannot publish to ANOTHER device's uplink topic"; tenant_login
  put_frame x1 111 11 31 1
  out="$(check_refused "$mqb" "$mqa" x1)" || fail "M3: $out"
  printf '%s\n' "$out"
  note "PASS M3: $mqb on $mqa's topic refused by the broker; $mqa stored nothing"
  say "M3 planted: the topic's owner publishes the same bytes to the same topic"; tenant_login
  if out="$(check_refused "$mqa" "$mqa" x1)"; then printf '%s\n' "$out"; fail "CONTROL FAILED: M3's check passed for the topic's owner"; fi
  printf '%s\n' "$out"; red "M3's check fails when the publisher owns the topic"

  say "M4 ON: over the cap, and no decoder, land on failed-decode with their cause"; tenant_login
  mq sh -c "{ printf '$(octal 1 0 200 40 30 0)'; head -c 4091 /dev/zero; } >/tmp/big"
  out="$(check_failed_decode "$mqa" big too_large)" || fail "M4 (over the cap): $out"
  printf '%s\n' "$out"
  put_frame p1 199 44 30 0
  out="$(check_failed_decode "$plain" p1 no_decoder)" || fail "M4 (no decoder): $out"
  printf '%s\n' "$out"
  note "PASS M4: each recorded once on failed-decode and counted under its cause; neither stored"
  say "M4 planted: a frame of exactly 4096 bytes (allowed), and a device whose type has a decoder"; tenant_login
  mq sh -c "{ printf '$(octal 1 0 201 41 30 1)'; head -c 4090 /dev/zero; } >/tmp/atcap"
  if out="$(check_failed_decode "$mqcap" atcap too_large)"; then printf '%s\n' "$out"; fail "CONTROL FAILED: M4's too_large check passed for a frame at the limit"; fi
  printf '%s\n' "$out"; red "M4's too_large check fails for a frame the decoder may take"
  check_stored_exactly "$mqcap" "$(expect_frame 201 41 30 1)" 60 || fail "the 4096-byte frame was neither refused nor stored"
  if out="$(check_failed_decode "$mqb" p1 no_decoder)"; then printf '%s\n' "$out"; fail "CONTROL FAILED: M4's no_decoder check passed for a device with a decoder"; fi
  printf '%s\n' "$out"; red "M4's no_decoder check fails for a device whose type has a decoder"

  say "M5 ON: a hanging device-management does not stall ordinary event decoding"; tenant_login
  pub_event "$plain" 0.5
  check_stored_exactly "$plain" "temperature 0.5 null" 60 >/dev/null || fail "M5: the plain device's event was not stored before the hang"
  local a0
  a0="$(uplink_result accepted)"
  dm_hang_on
  hang_bites "mq-cold1-$mq_run" || fail "M5: every packet from event-sources to device-management is dropped and no lookup hung"
  out="$(check_events_flow 1 "mq-cold1-$mq_run")" || fail "M5: $out"
  printf '%s\n' "$out"
  dm_hang_off
  drain_round "$a0" $((1 + f1_events))
  note "PASS M5: ordinary events stored inside ${f1_window}s while every decoder lookup hung"
  if [[ "${DC_DEC_RIG_F1_CONTROL:-1}" == 1 ]]; then
    say "M5 planted: the plan lookup moved back onto the decode worker"; tenant_login
    build_planted_es
    es_image_restore="$(kn get deploy event-sources -o jsonpath='{.spec.template.spec.containers[0].image}')"
    set_es_image "$planted_es_image"
    a0="$(uplink_result accepted)"
    dm_hang_on
    hang_bites "mq-cold2-$mq_run" || fail "M5 planted: no lookup hung"
    if out="$(check_events_flow 2 "mq-cold2-$mq_run")"; then printf '%s\n' "$out"; fail "CONTROL FAILED: M5's check passed with the lookup on the decode worker"; fi
    printf '%s\n' "$out"; red "M5's check fails when the lookup runs on the decode worker"
    dm_hang_off
    drain_round "$a0" $((1 + 2 * f1_events))
    set_es_image "$es_image_restore"; es_image_restore=""
    a0="$(uplink_result accepted)"
    dm_hang_on
    hang_bites "mq-cold3-$mq_run" || fail "M5 (image under test again): no lookup hung"
    out="$(check_events_flow 3 "mq-cold3-$mq_run")" || fail "M5 (image under test again): $out"
    printf '%s\n' "$out"; dm_hang_off
    drain_round "$a0" $((1 + 3 * f1_events))
    note "and green again on the image under test"
  fi

  say "M6: the setting OFF again, in an upgrade of its own"; tenant_login
  hold_open "$mqoff"
  held_send "pre-off"
  set_device_uplinks false
  ! uplink_captured || fail "the capture stream still holds the uplink subject after turning it off"
  since="$(now_rfc3339)"; sleep 1
  if held_alive; then
    held_send "post-off"
    sleep 8
    v="$(violations "$since" "$instance:$tenant:$mqoff:held" "$instance.$tenant.devices.$mqoff.uplink")"
    note "a connection held across the upgrade is still open; its publish met $v Publish Violation(s)"
  else
    note "the connection held across the upgrade did not survive it"
  fi
  held_close
  put_frame h1 225 48 33 0
  out="$(check_refused "$mqoff" "$mqoff" h1)" || fail "M6: $out"
  printf '%s\n' "$out"
  note "PASS M6: on reconnect the device is refused on its uplink topic again"

  kn delete pod "$device_pod" --ignore-not-found --wait=false >/dev/null
  say "PASS (mqtt): M1-M6 green, and each shown red on its planted defect"
}

case "${1:-}" in
  up) cmd_up ;;
  test) cmd_test ;;
  mixed) cmd_mixed ;;
  mqtt) cmd_mqtt ;;
  down) cmd_down ;;
  selftest) cmd_selftest ;;
  all)
    down_on_exit=1
    cmd_up
    cmd_test
    cmd_mixed
    cmd_mqtt
    ;;
  *) fail "usage: $0 up|test|mixed|mqtt|down|all|selftest" ;;
esac
