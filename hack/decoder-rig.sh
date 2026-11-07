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
#   hack/decoder-rig.sh down       # destroy the instance and DELETE the cluster
#   hack/decoder-rig.sh all        # up, test, mixed, down (down runs even when a step fails)
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
cleanup() { local rc=$?; rm -rf "$rig_tmp"; printf 'decoder-rig: %s exited with status %d\n' "$rig_cmd" "$rc" >>"$run_log"; }
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
dmq() { gql "$dm/graphql" "$tenant_token" "$@"; }
emq() { gql "$em/graphql" "$tenant_token" "$@"; }

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
  bad="$(awk '$1 == "door" && $2 != "0" && $2 != "1"' <<<"$body")"
  [[ -z "$bad" ]] || fail "$label: a boolean must be stored as 0 or 1: $bad"
  c="$(awk '$1 == "door" && $2 == "1" { c++ } END { print c + 0 }' <<<"$body")"
  [[ "$c" == 2 ]] || fail "$label: door is open on the odd uplinks (2 of $uplinks), got $c"
  bad="$(awk '$1 == "lorawan.gateways" && $2 != "2"' <<<"$body")"
  [[ -z "$bad" ]] || fail "$label: two gateways reported every uplink: $bad"
  note "$label: all 7 keys stored $uplinks times, unit and 1/0 booleans as declared"
}

good_body() {
  local i
  for i in $(seq 0 $((uplinks - 1))); do
    printf 'temperature 21.5 C\nhumidity 50 null\nbattery.voltage 3.1 V\ndoor %s null\nlorawan.rssi -80 null\nlorawan.snr 8.5 null\nlorawan.gateways 2 null\n' $((i % 2))
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
# mixed: the rolling-upgrade window
# ---------------------------------------------------------------------------
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
  say "scaling device-management ($replicas replica(s)) to zero: nothing reads the integration-uplinks stream"
  k -n "$namespace" scale deploy/device-management --replicas=0 >/dev/null
  k -n "$namespace" wait --for=delete pod -l app.kubernetes.io/name=device-management --timeout=120s >/dev/null 2>&1 || true

  say "(a) uplinks for a decoder's type meanwhile: accepted, parked, not stored, not failed"
  s="$(send "$eui" 3)"; note "$s"
  expect_status "$s" 202 3
  sleep 25
  # The read path is device-management's too, so the counts are read once it is back.
  say "device-management comes back"
  k -n "$namespace" scale deploy/device-management --replicas="$replicas" >/dev/null
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

case "${1:-}" in
  up) cmd_up ;;
  test) cmd_test ;;
  mixed) cmd_mixed ;;
  down) cmd_down ;;
  selftest) cmd_selftest ;;
  all)
    trap 'cmd_down || true; rm -rf "$rig_tmp"' EXIT
    cmd_up
    cmd_test
    cmd_mixed
    ;;
  *) fail "usage: $0 up|test|mixed|down|all|selftest" ;;
esac
