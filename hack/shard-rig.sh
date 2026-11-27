#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# End-to-end rig for sharded resolved-events with event-processing deployed, on a FRESH install, on a
# kind cluster of this rig's own.
#
#   hack/shard-rig.sh up      # cluster + images built from this tree + a fresh instance installed
#                             # with shardGroups {count: 2, suffixes: [inbound-events, resolved-events]}
#   hack/shard-rig.sh place   # odd vpartitions to group 1 (before any tenant exists), mirrored
#   hack/shard-rig.sh test    # DETECT fires for a tenant in each group; the negative controls
#   hack/shard-rig.sh purge   # a tenant of each group is deleted and its DETECT state erased in its
#                             # group, with no eviction reply refused by the broker; negative control
#   hack/shard-rig.sh down    # destroy the instance and DELETE the cluster
#   hack/shard-rig.sh all     # up, place, test, purge, down (down runs even when a step fails)
#
# WHAT `test` PROVES, through the ingress and across every service:
#
#   1. Each group is really in use: tenant A's resolved events land on the group-0 stream and tenant
#      B's on resolved-events-g1, and nothing of B's on group 0.
#   2. DETECT runs for BOTH groups: a threshold rule on each tenant's profile publishes a detection
#      (a derived event on the tenant's own subject) for each tenant's device.
#   3. NEGATIVE CONTROL (the check can go red). With event-processing scaled to zero, a NEW device of
#      tenant B produces no detection: the derived-event check is shown failing. Scaled back, the
#      detection arrives from the group-1 stream, exactly once.
#   4. NEGATIVE CONTROL (the fresh-install rule). `dcctl upgrade` that changes the group count while
#      resolved-events is sharded is refused before anything is written, and the instance still runs
#      two groups afterwards.
#
# PLACEMENT. Nothing in the product moves a vpartition to another group yet, and the default puts
# every vpartition in group 0. `place` writes the assignment table in user-management's database the
# way the placement store would (assignments plus a generation bump in one transaction) and restarts
# user-management so its start-time sync mirrors the table. It runs BEFORE any tenant exists, which is
# what makes it a fresh install's placement rather than a move.
#
# The cluster is this rig's own (DC_SHARD_RIG_CLUSTER, default dc-shardrig) with its own kubeconfig;
# it never touches another cluster or switches a kube-context. NOT RUN IN CI: it needs a real cluster
# and builds every image. Requires kind, kubectl, docker, jq, curl, go, ko, python3.

# GraphQL documents are single-quoted on purpose: their $variables are GraphQL's, not the shell's.
# shellcheck disable=SC2016
set -euo pipefail

repo_root="${DC_SHARD_RIG_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cluster="${DC_SHARD_RIG_CLUSTER:-dc-shardrig}"
kube_context="kind-$cluster"
instance="${DC_SHARD_RIG_INSTANCE:-shardrig}"
namespace="dci-$instance"
host="${DC_SHARD_RIG_HOST:-$instance.localhost}"
http_port="${DC_SHARD_RIG_HTTP_PORT:-18731}"
state_dir="${DC_SHARD_RIG_STATE:-$repo_root/.shard-rig}"
min_free_gb="${DC_SHARD_RIG_MIN_FREE_GB:-60}"
deadman_hours="${DC_SHARD_RIG_DEADMAN_HOURS:-4}"
exe="$(go env GOEXE 2>/dev/null || true)"
dcctl="$repo_root/backend/cli/build/dcctl$exe"
groups=2

winpath() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }
mkdir -p "$state_dir"
kubeconfig="$(winpath "$state_dir/kubeconfig")"
export KUBECONFIG="$kubeconfig"

[[ "$cluster" != devicechain ]] || { echo "refusing: DC_SHARD_RIG_CLUSTER=devicechain is the shared developer cluster" >&2; exit 2; }

api="http://localhost:$http_port"
host_header=(-H "Host: $host")
um="$api/api/user-management"
dm="$api/api/device-management"

rig_tmp="$(mktemp -d "${TMPDIR:-/tmp}/dc-shard-rig.XXXXXX")"
rig_cmd="${1:-?}"
run_log="$state_dir/run-$(date -u +%Y%m%dT%H%M%SZ).log"
exec > >(tee -a "$run_log") 2>&1
ep_restore_to=""
down_on_exit=0
cleanup() {
  local rc=$?
  trap - EXIT
  set +e
  if [[ -n "$ep_restore_to" ]]; then k -n "$namespace" scale deploy/event-processing --replicas="$ep_restore_to" >/dev/null 2>&1; fi
  if [[ "$down_on_exit" == 1 ]]; then (cmd_down) || true; fi
  rm -rf "$rig_tmp"; printf 'shard-rig: %s exited with status %d\n' "$rig_cmd" "$rc" >>"$run_log"
  exit "$rc"
}
trap cleanup EXIT

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
red() { printf '\033[1;33m    RED (as designed) %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }
redact() { sed -u -E 's/^([[:space:]]*password:[[:space:]]*).+$/\1[redacted]/'; }
if command jq -b -n 1 >/dev/null 2>&1; then jq() { command jq -b "$@"; }; fi

k() { kubectl --kubeconfig "$kubeconfig" --context "$kube_context" "$@"; }
kn() { MSYS_NO_PATHCONV=1 k -n "$namespace" "$@"; }

# ---------------------------------------------------------------------------
# up / down
# ---------------------------------------------------------------------------

cluster_exists() { grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; }

deadman_pid_file="$state_dir/deadman.pid"
disarm_deadman() {
  local pid
  [[ -f "$deadman_pid_file" ]] || return 0
  pid="$(<"$deadman_pid_file")"
  if [[ "$pid" =~ ^[0-9]+$ ]] && grep -q "kind delete cluster --name '$cluster'" <<<"$(ps -ef | awk -v p="$pid" '$2 == p')"; then
    kill "$pid" 2>/dev/null || true
  fi
  rm -f "$deadman_pid_file"
}
arm_deadman() {
  disarm_deadman
  [[ "$deadman_hours" != 0 ]] || return 0
  nohup bash -c "
    sleeper=
    trap 'if [ -n \"\$sleeper\" ]; then kill \"\$sleeper\" 2>/dev/null; fi; exit 0' TERM INT
    sleep $((deadman_hours * 3600)) &
    sleeper=\$!
    wait \"\$sleeper\" && kind delete cluster --name '$cluster' --kubeconfig '$kubeconfig'
  " >"$state_dir/deadman.log" 2>&1 </dev/null &
  printf '%s\n' "$!" >"$deadman_pid_file"
  disown || true
  note "dead-man armed: cluster $cluster is deleted in ${deadman_hours}h unless 'down' runs first"
}

build_dcctl() {
  say "building dcctl"
  make -C "$repo_root/backend/cli" build >/dev/null
  [[ -x "$dcctl" ]] || fail "dcctl was not built at $dcctl"
}

shard_values() {
  local count="$1" f="$rig_tmp/shard-values-$1.yaml"
  cat >"$f" <<EOF
instance:
  config:
    infrastructure:
      nats:
        shardGroups:
          count: $count
          suffixes: [inbound-events, resolved-events]
EOF
  winpath "$f"
}

cmd_up() {
  need kind; need kubectl; need docker; need jq; need ko; need go
  local free
  free="$(df -Pk "$HOME" | awk 'NR==2 {printf "%d", $4/1024/1024}')"
  [[ "$free" -ge "$min_free_gb" ]] || fail "only ${free} GB free; this rig needs $min_free_gb"
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
  # Shard groups widen the broker to three servers, spread one per node by a hard constraint.
  - role: worker
  - role: worker
  - role: worker
EOF
    kind create cluster --name "$cluster" --kubeconfig "$kubeconfig" --config "$(winpath "$rig_tmp/kind.yaml")" --wait 120s
    arm_deadman
  else
    k get ns kube-system >/dev/null 2>&1 || fail "kind cluster $cluster exists but is not in $kubeconfig: not this rig's"
  fi
  if k get ns "$namespace" >/dev/null 2>&1; then note "instance $instance already exists; reusing it"; return; fi
  build_dcctl
  local tag
  tag="shard-$(git -C "$repo_root" rev-parse --short=8 HEAD)-$(date -u +%Y%m%dT%H%M%SZ)"
  printf '%s\n' "$tag" >"$state_dir/image-tag"
  say "installing prerequisites and bootstrapping $instance (FRESH, resolved-events sharded) with images $tag"
  "$dcctl" install local --yes --cluster "$cluster" --no-monitoring --build --version "$tag" 2>&1 | redact
  (cd "$repo_root" && "$dcctl" bootstrap local "$instance" --yes --no-escrow --no-tls --cluster "$cluster" --host "$host" \
    --build --version "$tag" --values "$(shard_values "$groups")" 2>&1 | redact)
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
# placement
# ---------------------------------------------------------------------------

# The relational store is shared by the cluster's instances and lives in dc-system, behind the
# dc-postgresql service (which points at the primary); each instance has a database of its own.
rdb_pod() {
  local pod
  pod="$(k -n dc-system get endpointslices -l kubernetes.io/service-name=dc-postgresql -o json |
    jq -r 'first(.items[].endpoints[] | select(.conditions.ready != false) | .targetRef.name // empty)')"
  [[ -n "$pod" ]] || fail "no ready relational Postgres pod behind dc-system/dc-postgresql"
  printf '%s' "$pod"
}
psql_in() { MSYS_NO_PATHCONV=1 k -n dc-system exec "$(rdb_pod)" -c postgres -- psql -q -U postgres -d "$1" -v ON_ERROR_STOP=1 -tA -c "$2"; }
um_db() {
  local d
  for d in $(psql_in postgres "SELECT datname FROM pg_database WHERE NOT datistemplate"); do
    if [[ "$(psql_in "$d" "SELECT to_regclass('\"user-management\".iam_vpartition_assignments') IS NOT NULL" 2>/dev/null)" == t ]]; then
      printf '%s' "$d"; return
    fi
  done
  fail "no database holds user-management's placement tables"
}
um_sql() { psql_in "$(um_db)" "SET search_path TO \"user-management\"; $1"; }

cmd_place() {
  k get ns "$namespace" >/dev/null 2>&1 || fail "instance $instance is not up; run 'up' first"
  local tenants
  tenants="$(um_sql "SELECT count(*) FROM iam_tenants WHERE token LIKE 'shard-%'")"
  [[ "$tenants" == 0 ]] || fail "rig tenants already exist; placement must be set before any tenant (run 'down' and start over)"
  say "placing odd vpartitions in group 1, before any tenant exists"
  um_sql "BEGIN; UPDATE iam_vpartition_assignments SET shard_group = vpartition % $groups;
          UPDATE iam_partition_config SET generation = generation + 1, updated_at = now(); COMMIT;" >/dev/null
  note "groups now: $(um_sql "SELECT shard_group, count(*) FROM iam_vpartition_assignments GROUP BY 1 ORDER BY 1" | tr '\n' ' ')"
  kn rollout restart deploy/user-management >/dev/null
  kn rollout status deploy/user-management --timeout=300s >/dev/null
  # The mirror's start-time sync publishes the table; the other services adopt it from the KV watch.
  sleep 20
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
  if grep -qi 'conflict\|already' <<<"$out"; then return 0; fi
  printf '%s\n' "$out" >&2
  return 1
}

admin_token=""
adm() { gql "$um/admin/graphql" "$admin_token" "$@"; }
declare -A tenant_tokens=()
member_password=""
dmq() { gql "$dm/graphql" "${tenant_tokens[$1]}" "${@:2}"; }

admin_login() {
  local pw
  pw="$(kn get secret "dci-$instance-superuser" -o jsonpath='{.data.password}' | base64 -d)"
  admin_token="$(gql "$um/graphql" - 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -n --arg e "superuser@devicechain.local" --arg p "$pw" '{e:$e,p:$p}')" | jq -r '.login.identityToken')"
  [[ -n "$admin_token" && "$admin_token" != null ]] || fail "could not sign in as the superuser"
}

member_email() { printf 'shard-admin-%s@%s.example' "$1" "$instance"; }

setup_tenant() {
  local t="$1" tier ident
  tier="$(adm '{tenantTiers{token}}' | jq -r '.tenantTiers[0].token')"
  tolerate_exists adm 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
    "$(jq -n --arg t "$t" --arg tier "$tier" '{r:{token:$t,name:$t,tierToken:$tier}}')" >/dev/null
  tolerate_exists adm 'mutation($e:String!,$p:String!){createIdentity(request:{email:$e,password:$p,enabled:true,systemRoles:[]}){email}}' \
    "$(jq -n --arg e "$(member_email "$t")" --arg p "$member_password" '{e:$e,p:$p}')" >/dev/null
  adm 'mutation($e:String!,$p:String!){setPassword(email:$e,password:$p){email}}' \
    "$(jq -n --arg e "$(member_email "$t")" --arg p "$member_password" '{e:$e,p:$p}')" >/dev/null
  tolerate_exists adm 'mutation($e:String!,$t:String!){addMembership(email:$e,tenant:$t,roleTokens:["tenant-admin"]){email}}' \
    "$(jq -n --arg e "$(member_email "$t")" --arg t "$t" '{e:$e,t:$t}')" >/dev/null
  ident="$(gql "$um/graphql" - 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -n --arg e "$(member_email "$t")" --arg p "$member_password" '{e:$e,p:$p}')" | jq -r '.login.identityToken')"
  tenant_tokens[$t]="$(gql "$um/graphql" - 'mutation($i:String!,$t:String!){selectTenant(identityToken:$i,tenant:$t){accessToken}}' \
    "$(jq -n --arg i "$ident" --arg t "$t" '{i:$i,t:$t}')" | jq -r '.selectTenant.accessToken')"
  [[ -n "${tenant_tokens[$t]}" && "${tenant_tokens[$t]}" != null ]] || fail "could not enter tenant $t"
}

# group_of prints the shard group a tenant hashes to under `place` (FNV-1a over instance/tenant,
# modulo P = 64, odd vpartitions in group 1). The test asserts it against the streams as well.
group_of() {
  "$py" -I -c '
import sys
h = 14695981039346656037
for c in (sys.argv[1] + "/" + sys.argv[2]).encode():
    h ^= c
    h = (h * 1099511628211) % (1 << 64)
print((h % 64) % int(sys.argv[3]))' "$instance" "$1" "$groups" | tr -d '\r'
}

pick_tenant() {
  local want="$1" prefix="${2:-shard}" i t
  for i in $(seq 1 200); do
    t="$prefix-$want-$i"
    [[ "$(group_of "$t")" == "$want" ]] && { printf '%s' "$t"; return; }
  done
  fail "no tenant token hashes to group $want"
}

sim=""
build_sim() {
  sim="$rig_tmp/dc-simulator$exe"
  (cd "$repo_root/backend/sims/dc-simulator" && go build -o "$sim" .)
}

declare -A int_tokens=()
setup_model() {
  local t="$1" p="shard-sensor" ty="shard-sensor" m key typ unit out
  tolerate_exists dmq "$t" 'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
    "$(jq -n --arg t "$p" '{r:{token:$t,name:$t}}')" >/dev/null
  for m in "temperature|DOUBLE|C" "humidity|DOUBLE|" "battery.voltage|DOUBLE|V" "door|BOOLEAN|"; do
    IFS='|' read -r key typ unit <<<"$m"
    tolerate_exists dmq "$t" 'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){token}}' \
      "$(jq -n --arg t "$p-${key//./-}" --arg p "$p" --arg k "$key" --arg d "$typ" --arg u "$unit" \
        '{r:({token:$t,deviceProfileToken:$p,metricKey:$k,name:$k,dataType:$d} + (if $u == "" then {} else {unit:$u} end))}')" >/dev/null
  done
  # Always true for any reading: one detection per device (the rule is edge-triggered).
  tolerate_exists dmq "$t" 'mutation($r:DetectionRuleCreateRequest!){createDetectionRule(request:$r){token}}' \
    "$(jq -n --arg p "$p" '{r:{token:"any-temperature",deviceProfileToken:$p,enabled:true,
      definition:"{\"name\":\"any-temperature\",\"type\":\"threshold\",\"when\":{\"metric\":\"temperature\",\"op\":\"gt\",\"threshold\":-1000}}"}}')" >/dev/null
  dmq "$t" 'mutation($t:String!){publishDeviceProfile(token:$t){version}}' "$(jq -n --arg t "$p" '{t:$t}')" >/dev/null ||
    fail "could not publish $p for $t"
  tolerate_exists dmq "$t" 'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
    "$(jq -n --arg t "$ty" --arg p "$p" '{r:{token:$t,name:$t,profileToken:$p}}')" >/dev/null
  if ! out="$(dmq "$t" 'mutation($r:IntegrationCreateRequest!){createIntegration(request:$r){issuedToken}}' \
    "$(jq -n --arg ty "$ty" '{r:{token:"chirpstack-shard",name:"chirpstack-shard",type:"CHIRPSTACK_V4",enabled:true,deviceTypeTokens:[$ty]}}')" 2>&1)"; then
    out="$(dmq "$t" 'mutation($t:String!){rotateIntegrationToken(token:$t){issuedToken}}' '{"t":"chirpstack-shard"}')" || fail "integration for $t: $out"
    int_tokens[$t]="$(jq -r '.rotateIntegrationToken.issuedToken' <<<"$out")"
  else
    int_tokens[$t]="$(jq -r '.createIntegration.issuedToken' <<<"$out")"
  fi
}

add_device() {
  local t="$1" d="$2" eui="$3"
  tolerate_exists dmq "$t" 'mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}' \
    "$(jq -n --arg t "$d" --arg v "$eui" '{r:{token:$t,name:$t,deviceTypeToken:"shard-sensor",aliases:[{scheme:"lorawan.deveui",value:$v}]}}')" >/dev/null
}

send() {
  local t="$1" eui="$2" n="$3"
  DC_SIM_INTEGRATION_TOKEN="${int_tokens[$t]}" "$sim" --lorawan-ns chirpstack --integration-host "$host" \
    --integration-url "$api/api/integrations/$t/chirpstack-shard" --deveui "$eui" --uplinks "$n"
}

# ---------------------------------------------------------------------------
# Reading the broker
# ---------------------------------------------------------------------------

nats_admin() {
  local pw
  pw="$(kn get secret "dci-$instance-nats-broker-admin" -o jsonpath='{.data.password}' | base64 -d)"
  kn exec deploy/dc-nats-box -- env NATS_USER=dc-broker-admin-g1 NATS_PASSWORD="$pw" \
    nats --server nats://dc-nats:4222 --inbox-prefix _INBOX_broker-admin "$@"
}

# subject_count <stream suffix> <tenant> <subject suffix>: messages on one tenant's subject.
subject_count() {
  local out
  out="$(nats_admin stream subjects "${instance}_$1" "$instance.$2.$3" --json 2>/dev/null || echo '{}')"
  jq -r --arg s "$instance.$2.$3" '.[$s] // 0' <<<"$out"
}
derived_count() { subject_count derived-events "$1" derived-events; }

wait_derived() {
  local t="$1" want="$2" secs="$3" got=0
  for _ in $(seq 1 $((secs / 3))); do
    got="$(derived_count "$t")"
    [[ "$got" -ge "$want" ]] && { printf '%s' "$got"; return 0; }
    sleep 3
  done
  printf '%s' "$got"
  return 1
}

# ---------------------------------------------------------------------------
# test
# ---------------------------------------------------------------------------

cmd_test() {
  need curl; need jq; need kubectl; need go
  py="$(command -v python3 || command -v python || true)"; [[ -n "$py" ]] || fail "python is required but not on PATH"
  k get ns "$namespace" >/dev/null 2>&1 || fail "instance $instance is not up; run 'up' first"
  build_sim
  kn rollout status deploy/event-processing --timeout=300s >/dev/null || fail "event-processing is not ready on the sharded install"
  local ta tb got
  ta="$(pick_tenant 0)"; tb="$(pick_tenant 1)"
  say "tenants: $ta (group 0) and $tb (group 1)"
  admin_login
  member_password="$(openssl rand -base64 24 | tr -d '/+=')Aa1"
  for t in "$ta" "$tb"; do setup_tenant "$t"; setup_model "$t"; done
  add_device "$ta" dev-a1 70B3D57ED0A00001
  add_device "$tb" dev-b1 70B3D57ED0B00001
  sleep 10 # the published rules and the roster reach DETECT as facts

  say "1. each tenant's resolved events land on its own group's stream"
  send "$ta" 70B3D57ED0A00001 3 >/dev/null
  send "$tb" 70B3D57ED0B00001 3 >/dev/null
  sleep 15
  local a0 b1 b0
  a0="$(subject_count resolved-events "$ta" resolved-events)"
  b1="$(subject_count resolved-events-g1 "$tb" resolved-events-g1)"
  b0="$(subject_count resolved-events "$tb" resolved-events)"
  note "group 0 holds $a0 of $ta's; group 1 holds $b1 of $tb's; group 0 holds $b0 of $tb's"
  [[ "$a0" -ge 1 ]] || fail "no resolved event of $ta on the group-0 stream"
  [[ "$b1" -ge 1 ]] || fail "no resolved event of $tb on resolved-events-g1: the stream is not sharded as installed"
  [[ "$b0" == 0 ]] || fail "$tb's resolved events reached the group-0 stream"

  say "2. DETECT runs in both groups"
  got="$(wait_derived "$ta" 1 90)" || fail "no detection for $ta (group 0) in 90s (got $got)"
  note "$ta: $got detection(s)"
  got="$(wait_derived "$tb" 1 90)" || fail "no detection for $tb (group 1) in 90s (got $got)"
  note "$tb: $got detection(s)"
  local before
  before="$got"

  say "3. NEGATIVE CONTROL: with event-processing stopped, a new group-1 device produces no detection"
  ep_restore_to="$(kn get deploy event-processing -o jsonpath='{.spec.replicas}')"
  kn scale deploy/event-processing --replicas=0 >/dev/null
  kn wait --for=delete pod -l devicechain.io/functional-area=event-processing --timeout=180s >/dev/null 2>&1 || true
  [[ -z "$(kn get pods -l devicechain.io/functional-area=event-processing -o name)" ]] ||
    fail "event-processing pods are still present after scaling to zero"
  # A device new to this run: the rule is edge-triggered, so a device that already fired cannot.
  local nc_dev nc_eui
  nc_eui="$(printf '70B3D57E%08X' "$(( $(date +%s) % 4294967296 ))")"
  nc_dev="dev-b-${nc_eui: -8}"
  add_device "$tb" "$nc_dev" "$nc_eui"
  send "$tb" "$nc_eui" 3 >/dev/null
  if got="$(wait_derived "$tb" $((before + 1)) 45)"; then
    fail "a detection appeared with event-processing stopped ($got): the check does not depend on DETECT"
  fi
  red "the detection check fails with event-processing stopped ($got, wanted $((before + 1)))"
  kn scale deploy/event-processing --replicas="$ep_restore_to" >/dev/null
  ep_restore_to=""
  kn rollout status deploy/event-processing --timeout=300s >/dev/null
  got="$(wait_derived "$tb" $((before + 1)) 180)" || fail "the group-1 detection never arrived after event-processing returned (got $got)"
  sleep 20
  got="$(derived_count "$tb")"
  [[ "$got" == $((before + 1)) ]] || fail "expected exactly one new detection for $nc_dev, got $((got - before))"
  note "green again: exactly one detection for $nc_dev, read from resolved-events-g1 after restart"

  say "4. NEGATIVE CONTROL: dcctl refuses to change the group count of the sharded instance"
  local out
  if out="$(cd "$repo_root" && "$dcctl" upgrade local "$instance" --yes --version "$(cat "$state_dir/image-tag")" \
    --values "$(shard_values 3)" 2>&1)"; then
    fail "dcctl upgrade changed shardGroups.count on a sharded instance: $(tail -3 <<<"$out")"
  fi
  grep -q "change shardGroups.count from $groups to 3" <<<"$out" || fail "the upgrade failed for another reason: $(tail -5 <<<"$out")"
  red "dcctl upgrade refused the count change: $(grep -m1 'change shardGroups.count' <<<"$out")"
  [[ "$(nats_admin stream ls --names 2>/dev/null | grep -c "^${instance}_resolved-events-g2$" || true)" == 0 ]] || fail "a third group stream exists"
  kn rollout status deploy/event-processing --timeout=120s >/dev/null || fail "event-processing is not ready after the refused upgrade"

  say "SHARD RIG PASSED"
}

# ---------------------------------------------------------------------------
# purge
# ---------------------------------------------------------------------------

# delete_tenant <tenant>: a tenant with members cannot be deleted, so the rig's own member goes first.
delete_tenant() {
  adm 'mutation($e:String!,$t:String!){removeMembership(email:$e,tenant:$t){email}}'     "$(jq -n --arg e "$(member_email "$1")" --arg t "$1" '{e:$e,t:$t}')" >/dev/null || fail "removeMembership for $1"
  adm 'mutation($t:String!){deleteTenant(token:$t)}' "$(jq -n --arg t "$1" '{t:$t}')" >/dev/null || fail "deleteTenant $1"
}

# deletion_line <tenant> prints the tenant's in-flight deletion as one JSON line.
deletion_line() {
  adm 'query($t:String!){tenantDeletion(token:$t){erased blockedBy stores{store complete retaining}}}' \
    "$(jq -n --arg t "$1" '{t:$t}')" | jq -c '.tenantDeletion'
}
detect_store() { jq -c '[.stores[]? | select(.store == "detect")][0] // {}' <<<"$1"; }

# wait_erased <tenant> <secs>: true once every store, DETECT's included, went clean and settled.
wait_erased() {
  local t="$1" secs="$2" line=""
  for _ in $(seq 1 $((secs / 15))); do
    # The superuser's identity token outlives a passing run but not this whole wait.
    line="$(deletion_line "$t" 2>/dev/null)" || { admin_login; line="$(deletion_line "$t")" || line=""; }
    [[ "$(jq -r '.erased // false' <<<"$line")" == true ]] && { printf '%s' "$line"; return 0; }
    sleep 15
  done
  printf '%s' "$line"
  return 1
}

# The broker refuses a reply to user-management's inbox that event-processing's login may not
# send, and the client logs each refusal it is told about. This counts them since a point in time.
ep_refusals() {
  local since="$1" pods p log n=0 c
  pods="$(kn get pods -l devicechain.io/functional-area=event-processing -o name)" || fail "could not list event-processing pods"
  [[ -n "$pods" ]] || fail "no event-processing pod to read refusals from"
  for p in $pods; do
    log="$(kn logs "$p" --since-time="$since")" || fail "could not read the log of $p"
    # A container that restarted during the check took its refusals with it into --previous.
    if [[ "$(kn get "$p" -o jsonpath='{.status.containerStatuses[0].restartCount}')" != 0 ]]; then
      log+=$'\n'"$(kn logs "$p" --previous --since-time="$since")" || fail "could not read the previous log of $p"
    fi
    c="$(grep -c 'Permissions Violation for Publish to "_INBOX_user-management' <<<"$log" || true)"
    n=$((n + c))
  done
  printf '%s' "$n"
}

# 5. A deleted tenant's DETECT state is erased in BOTH groups. event-processing holds one
#    partition per group in one process, on one broker connection, and each must answer the
#    eviction. The broker admits only one reply per connection to a request it delivered, so an
#    engine that answered once per partition had every answer refused and the deletion never
#    finished.
cmd_purge() {
  need curl; need jq; need kubectl; need go
  py="$(command -v python3 || command -v python || true)"; [[ -n "$py" ]] || fail "python is required but not on PATH"
  k get ns "$namespace" >/dev/null 2>&1 || fail "instance $instance is not up; run 'up' first"
  build_sim
  kn rollout status deploy/event-processing --timeout=300s >/dev/null || fail "event-processing is not ready"
  local run ta tb since got line
  run="p$(date -u +%H%M%S)"
  ta="$(pick_tenant 0 "$run")"; tb="$(pick_tenant 1 "$run")"
  say "purge: tenants $ta (group 0) and $tb (group 1), each with live DETECT state"
  admin_login
  member_password="$(openssl rand -base64 24 | tr -d '/+=')Aa1"
  for t in "$ta" "$tb"; do setup_tenant "$t"; setup_model "$t"; done
  add_device "$ta" dev-pa 70B3D57ED0C00001
  add_device "$tb" dev-pb 70B3D57ED0D00001
  sleep 10
  send "$ta" 70B3D57ED0C00001 2 >/dev/null
  send "$tb" 70B3D57ED0D00001 2 >/dev/null
  got="$(wait_derived "$ta" 1 90)" || fail "no detection for $ta (group 0) before the deletion (got $got)"
  got="$(wait_derived "$tb" 1 90)" || fail "no detection for $tb (group 1) before the deletion (got $got)"
  sleep 20 # a checkpoint names both groups' partitions
  since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  say "5a. NEGATIVE CONTROL: with event-processing stopped, the DETECT store holds the deletion"
  ep_restore_to="$(kn get deploy event-processing -o jsonpath='{.spec.replicas}')"
  kn scale deploy/event-processing --replicas=0 >/dev/null
  kn wait --for=delete pod -l devicechain.io/functional-area=event-processing --timeout=180s >/dev/null 2>&1 || true
  # The defect this step guards produces the same deferral with event-processing RUNNING, so the
  # control means something only once no engine is left to answer.
  [[ -z "$(kn get pods -l devicechain.io/functional-area=event-processing -o name)" ]] ||
    fail "event-processing pods are still present after scaling to zero"
  delete_tenant "$tb"
  line=""
  for _ in $(seq 1 12); do
    sleep 15
    line="$(deletion_line "$tb")" || continue
    grep -q 'no DETECT engine answered' <<<"$(detect_store "$line")" && break
  done
  grep -q 'no DETECT engine answered' <<<"$(detect_store "$line")" ||
    fail "the DETECT store did not hold the deletion with event-processing stopped: $(detect_store "$line")"
  red "the DETECT store holds $tb's deletion with no engine running: $(detect_store "$line" | cut -c1-200)"
  kn scale deploy/event-processing --replicas="$ep_restore_to" >/dev/null
  ep_restore_to=""
  kn rollout status deploy/event-processing --timeout=300s >/dev/null

  say "5b. both groups answer: the deletions of $tb and $ta finish, with no reply refused"
  delete_tenant "$ta"
  for t in "$tb" "$ta"; do
    line="$(wait_erased "$t" 900)" ||
      fail "the deletion of $t did not finish; blocked by $(jq -c '.blockedBy' <<<"$line" | cut -c1-400); refused replies: $(ep_refusals "$since")"
    note "$t erased; detect: $(detect_store "$line")"
  done
  got="$(ep_refusals "$since")"
  [[ "$got" == 0 ]] || fail "the broker refused $got of event-processing's eviction replies"
  note "no eviction reply was refused"
  say "SHARD RIG PURGE PASSED"
}

case "${1:-}" in
  up) cmd_up ;;
  place) cmd_place ;;
  test) cmd_test ;;
  purge) cmd_purge ;;
  down) cmd_down ;;
  all)
    down_on_exit=1
    cmd_up
    cmd_place
    cmd_test
    cmd_purge
    ;;
  *) fail "usage: $0 up|place|test|purge|down|all" ;;
esac
