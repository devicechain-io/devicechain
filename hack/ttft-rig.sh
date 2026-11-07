#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The time-to-first-telemetry rig: from a clean start, how long until a device's reading is on a
# dashboard? Measured, not promised.
#
# WHAT THIS MEASURES
#
# The wall clock of each phase an evaluator waits through, on one kind cluster of its own, with
# the PUBLISHED release images and (by default) the dcctl built from this tree, exactly as the
# quickstart runs them:
#
#   cluster          kind create cluster
#   operators        dcctl install local        (the cluster prerequisites and operators)
#   bootstrap_prep   dcctl bootstrap, every step before the chart goes in
#   infra            dcctl bootstrap, "Apply infrastructure" (databases, broker, object store)
#   chart            dcctl bootstrap, "Install instance (Helm)" and "Wait for readiness"
#   provision        sign in, create a tenant, a device type, a device and its credential (API)
#   first_stored     from the first publish until the reading comes back from the event store
#   first_dashboard  from the first publish until the query a dashboard chart sends (one-minute
#                    buckets, the continuous aggregate) returns it
#
# and ttft_ms: the total from the start of `cluster` to the end of first_dashboard. Every dcctl
# bootstrap step is also recorded by name under bootstrap_steps, so a phase that grows can be
# traced to the step that grew.
#
# The result is one JSON document per run (DC_TTFT_OUT, default .ttft-rig/): the git HEAD, the
# dcctl and image versions, the host, every phase, and "status". A run that does not reach the
# end writes status "failed" with the phase that did not, and exits non-zero. The numbers are a
# baseline for THIS host and THIS network: image pulls dominate a first install and a registry
# mirror or a warm node would change them, so compare runs from one host only.
#
#   hack/ttft-rig.sh run        one full run from a clean start; tears the cluster down after
#                               (DC_TTFT_KEEP=1 leaves it for `negative`)
#   hack/ttft-rig.sh negative   the NEGATIVE CONTROL against a kept instance: the device is aimed at
#                               the wrong broker port and the first-reading phase must FAIL. The same
#                               phase at the right port must pass, or the control proves nothing.
#   hack/ttft-rig.sh checks     offline: the result writer and the judge shown red on bad records
#   hack/ttft-rig.sh down       remove the device container and delete the cluster
#
# A check is worth nothing until it has been shown to fail: `checks` runs first inside `run`, and
# `negative` is the same refusal against a real cluster.
#
# ISOLATION. The cluster has its own kubeconfig file (under DC_TTFT_STATE) and every kubectl, kind
# and dcctl call is given it explicitly; this rig never reads or changes another kubeconfig's
# current context, and `down` deletes only this rig's cluster and container. It never runs a
# host-wide docker kill, stop or prune.
#
# Requires: kind, kubectl, docker, jq, openssl, curl, and `tofu` OR `terraform` (dcctl drives it).
# Without DC_TTFT_DCCTL it also needs a Go toolchain, to build dcctl from this tree, and then
# DC_TTFT_VERSION must name a PUBLISHED release (a dcctl built from source carries no image tag).
#
# Knobs: DC_TTFT_DCCTL (a dcctl binary, e.g. a release download), DC_TTFT_VERSION (--version for
# both dcctl commands), DC_TTFT_CLUSTER, DC_TTFT_INSTANCE, DC_TTFT_HTTPS_PORT, DC_TTFT_OUT,
# DC_TTFT_STATE, DC_TTFT_READING_TIMEOUT (seconds to wait for the reading, default 240),
# DC_TTFT_KEEP.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="${DC_TTFT_CLUSTER:-dc-ttft}"
kube_context="kind-$cluster"
instance="${DC_TTFT_INSTANCE:-ttft}"
namespace="dci-$instance"
host="${DC_TTFT_HOST:-$instance.devicechain.test}"
https_port="${DC_TTFT_HTTPS_PORT:-18643}"
state_dir="${DC_TTFT_STATE:-$repo_root/.ttft-rig}"
out_dir="${DC_TTFT_OUT:-$state_dir}"
device_container="${DC_TTFT_DEVICE_CONTAINER:-$cluster-device}"
device_image="${DC_TTFT_DEVICE_IMAGE:-eclipse-mosquitto:2@sha256:860aeb5d63ce509508f6dcfb2bc8a1c1dd2bc60555b562b45ce90e52d8dabb0f}"
version="${DC_TTFT_VERSION:-}"
reading_timeout="${DC_TTFT_READING_TIMEOUT:-240}"
tenant="${DC_TTFT_TENANT:-ttft}"
member_email="ttft-rig@$tenant.test"
dtype="ttft-type"
profile="ttft-profile"
device_token="ttft-device-1"
mqtt_node_port=31883
wrong_mqtt_port=31884

winpath() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }
mkdir -p "$state_dir" "$out_dir"
kubeconfig="$(winpath "$state_dir/kubeconfig")"
export KUBECONFIG="$kubeconfig"

# Never the shared developer cluster: `down` deletes the cluster it is pointed at.
[[ "$cluster" != devicechain ]] || { echo "refusing: DC_TTFT_CLUSTER=devicechain is the shared developer cluster" >&2; exit 2; }
[[ "$instance" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || { echo "DC_TTFT_INSTANCE must be a DNS label, got '$instance'" >&2; exit 2; }

rig_cmd="${1:-}"
run_stamp="$(date -u +%Y%m%dT%H%M%SZ)"
run_log="$state_dir/run-$run_stamp.log"
exec > >(tee -a "$run_log") 2>&1

rig_tmp="$(mktemp -d "${TMPDIR:-/tmp}/dc-ttft-rig.XXXXXX")"
work="$rig_tmp/work"
mkdir -p "$work"
result_file=""
cleanup() {
  local rc=$?
  # A run that dies leaves a result saying so, never a missing or stale file.
  if [[ -n "$result_file" && ! -s "$result_file" ]]; then
    write_result failed "${current_phase:-unknown}" >/dev/null 2>&1 || true
  fi
  rm -rf "$rig_tmp"
  printf 'ttft-rig: %s exited with status %d\n' "${rig_cmd:-?}" "$rc" >>"$run_log"
}
trap cleanup EXIT

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
pass() { printf '\033[1;32m    PASS %s\033[0m\n' "$*"; }
red() { printf '\033[1;33m    RED (as designed) %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }

# jq on Windows writes CRLF unless told not to, and "true\r" is not "true". -b is that switch.
if command jq -b -n 1 >/dev/null 2>&1; then jq() { command jq -b "$@"; }; fi

k() { kubectl --kubeconfig "$kubeconfig" --context "$kube_context" "$@"; }
dx() { MSYS_NO_PATHCONV=1 docker exec "$device_container" "$@"; }
dk() { MSYS_NO_PATHCONV=1 docker "$@"; }
now_ms() { echo $(($(date +%s%N) / 1000000)); }
cluster_exists() { grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; }

# ---------------------------------------------------------------------------
# The record: phases are measured into files, the result is built from them
# ---------------------------------------------------------------------------

phases_file="$work/phases.tsv" # <name>\t<ms>
steps_file="$work/steps.tsv"   # <name>\t<ms>
: >"$phases_file"
: >"$steps_file"
current_phase=""
t_start=0

phase_begin() { current_phase="$1"; phase_t0="$(now_ms)"; say "PHASE $1"; }
phase_end() { local ms=$(($(now_ms) - phase_t0)); printf '%s\t%d\n' "$current_phase" "$ms" >>"$phases_file"; note "$current_phase: ${ms} ms"; current_phase=""; }
phase_ms() { awk -F'\t' -v p="$1" '$1 == p { v = $2 } END { if (v == "") print "null"; else print v }' "$phases_file"; }

# The phases a user waits through, in order. ttftMs is their sum; first_stored overlaps first_dashboard
# (both run from the first publish) and is reported, not summed.
summed_phases="cluster operators bootstrap_prep infra chart provision device_setup first_dashboard"

# write_result <status> [failed phase]: one JSON document, written whole (never appended to).
# The phase and step tables go through real files: a native jq cannot open a bash process substitution.
write_result() {
  local status="$1" failed="${2:-}" head dirty dcctl_ver total
  head="$(git -C "$repo_root" rev-parse HEAD 2>/dev/null || echo unknown)"
  dirty="$(git -C "$repo_root" status --porcelain 2>/dev/null | wc -l | tr -d ' ')"
  dcctl_ver="$("$dcctl" version 2>/dev/null | head -n1 | tr -d '\r' || true)"
  jq -R -s 'split("\n") | map(select(length>0) | split("\t") | {key: .[0], value: (.[1]|tonumber)}) | from_entries' "$phases_file" >"$work/phases.json"
  jq -R -s 'split("\n") | map(select(length>0) | split("\t") | {name: .[0], ms: (.[1]|tonumber)})' "$steps_file" >"$work/steps.json"
  total=null
  ((t_start == 0)) || total=$(($(now_ms) - t_start))
  jq -n --arg status "$status" --arg failed "$failed" --arg head "$head" --argjson dirty "${dirty:-0}" \
    --arg dcctl "${dcctl_ver:-unknown}" --arg version "${version:-tree}" --arg started "$run_stamp" \
    --arg host_os "$(uname -s)" --arg ncpu "$(nproc 2>/dev/null || echo 0)" --arg script "$(git -C "$repo_root" hash-object hack/ttft-rig.sh 2>/dev/null || echo unknown)" \
    --argjson total "$total" --arg summed "$summed_phases" \
    --slurpfile ph "$(winpath "$work/phases.json")" \
    --slurpfile st "$(winpath "$work/steps.json")" \
    '{schema: 1, status: $status, failedPhase: (if $failed == "" then null else $failed end),
      startedAt: $started, gitHead: $head, dirtyPaths: $dirty, rigBlob: $script, dcctl: $dcctl, images: $version,
      host: {os: $host_os, cpus: ($ncpu|tonumber)},
      phasesMs: $ph[0],
      ttftMs: (if $status == "ok" then ([($summed|split(" "))[] as $p | $ph[0][$p]] | add) else null end),
      wallMs: $total, bootstrapSteps: $st[0]}' >"$result_file"
}

# judge_result <file>: the verdict on a result document, a pure function of it. It goes RED (status 1,
# one line per reason) unless the run is "ok", every phase is a positive number, ttftMs is exactly the sum
# of the phases a user waits through, and the wall clock is at least that long. A result that merely
# EXISTS proves nothing.
judge_result() {
  local f="$1" bad=0 p
  [[ "$(jq -r '.status' "$f")" == ok ]] || { echo "RED: status is '$(jq -r '.status' "$f")', failed phase '$(jq -r '.failedPhase // "-"' "$f")'"; bad=1; }
  for p in $summed_phases first_stored; do
    jq -e --arg p "$p" '.phasesMs[$p] | type == "number" and . > 0' "$f" >/dev/null 2>&1 || { echo "RED: phase $p is missing or not positive"; bad=1; }
  done
  jq -e '.ttftMs | type == "number"' "$f" >/dev/null 2>&1 || { echo "RED: ttftMs is not a number"; bad=1; }
  jq -e --arg summed "$summed_phases" '.ttftMs == ([($summed|split(" "))[] as $p | .phasesMs[$p]] | add)' "$f" >/dev/null 2>&1 || { echo "RED: ttftMs is not the sum of the phases a user waits through"; bad=1; }
  jq -e '(.wallMs | type == "number") and .wallMs >= .ttftMs' "$f" >/dev/null 2>&1 || { echo "RED: the wall clock is missing or shorter than ttftMs"; bad=1; }
  ((bad == 0)) || return 1
  echo GREEN
}

cmd_checks() {
  say "CHECKS: the judge must go red on every bad record"
  need jq
  local saved_result="$result_file" saved_dcctl="$dcctl" good bad
  result_file="$work/judge.json"
  dcctl=/nonexistent
  good='{"status":"ok","failedPhase":null,"ttftMs":748000,"wallMs":800000,"phasesMs":{"cluster":30000,"operators":120000,"bootstrap_prep":20000,"infra":200000,"chart":300000,"provision":3000,"device_setup":5000,"first_stored":2000,"first_dashboard":70000}}'
  printf '%s' "$good" >"$work/good.json"
  [[ "$(judge_result "$work/good.json")" == GREEN ]] || fail "the judge rejected a good record"
  pass "a complete record is GREEN"
  local -a bads=(
    "$(jq -c '.status="failed"|.failedPhase="first_stored"' <<<"$good")"
    "$(jq -c 'del(.phasesMs.first_dashboard)' <<<"$good")"
    "$(jq -c '.phasesMs.chart=0' <<<"$good")"
    "$(jq -c '.ttftMs=null' <<<"$good")"
    "$(jq -c '.ttftMs=1000' <<<"$good")"
    "$(jq -c '.wallMs=1' <<<"$good")"
    "$(jq -c 'del(.wallMs)' <<<"$good")"
    "$(jq -c 'del(.phasesMs.device_setup)' <<<"$good")"
  )
  for bad in "${bads[@]}"; do
    printf '%s' "$bad" >"$work/bad.json"
    if out="$(judge_result "$work/bad.json")"; then fail "the judge accepted a bad record: $bad"; fi
    red "$(head -n1 <<<"$out")"
  done
  pass "the judge went red on all ${#bads[@]} bad records"
  # The writer itself, end to end: a fabricated run goes through write_result and must come out GREEN,
  # and the same run marked failed must come out RED. A writer that could not produce a file the judge
  # reads would otherwise be found only after the first real run.
  printf 'cluster	30000
operators	120000
bootstrap_prep	20000
infra	200000
chart	300000
provision	3000
device_setup	5000
first_stored	2000
first_dashboard	70000
' >"$phases_file"
  printf 'Apply infrastructure	200000
' >"$steps_file"
  t_start=$(($(now_ms) - 900000))
  write_result ok
  [[ "$(judge_result "$result_file")" == GREEN ]] || fail "write_result's own output did not pass the judge: $(cat "$result_file")"
  write_result failed first_stored
  if judge_result "$result_file" >/dev/null; then fail "a failed run written by write_result passed the judge"; fi
  pass "write_result's output passes the judge when ok and fails it when failed"
  : >"$phases_file"
  : >"$steps_file"
  t_start=0
  # Container ownership: a stand-in docker records every rm. An unlabelled or foreign-labelled
  # container must survive (and the call must fail); an own-labelled one must go; an absent one is fine.
  local rm_log="$work/docker-rm.log" label
  : >"$rm_log"
  docker() {
    case "$1" in
      inspect) [[ -n "${fake_label+x}" ]] || return 1; printf '%s
' "$fake_label" ;;
      rm) printf '%s
' "$*" >>"$rm_log" ;;
    esac
  }
  for label in "" "not-$cluster"; do
    fake_label="$label"
    if (remove_device_container) >/dev/null 2>&1; then fail "remove_device_container accepted a container labelled '$label'"; fi
    [[ ! -s "$rm_log" ]] || fail "a container labelled '$label' was removed"
  done
  fake_label="$cluster"
  remove_device_container
  [[ "$(wc -l <"$rm_log")" -eq 1 ]] || fail "an own container was not removed exactly once"
  : >"$rm_log"
  unset fake_label
  remove_device_container
  [[ ! -s "$rm_log" ]] || fail "rm ran for an absent container"
  unset -f docker
  pass "a container is removed only when it carries this rig's label"
  result_file="$saved_result"
  dcctl="$saved_dcctl"
}

# ---------------------------------------------------------------------------
# dcctl
# ---------------------------------------------------------------------------

dcctl="${DC_TTFT_DCCTL:-}"
resolve_dcctl() {
  if [[ -n "$dcctl" ]]; then
    [[ -x "$dcctl" ]] || fail "DC_TTFT_DCCTL=$dcctl is not an executable file"
    return
  fi
  [[ -n "$version" ]] || fail "without DC_TTFT_DCCTL, set DC_TTFT_VERSION to a published release (a dcctl built from source names no image tag)"
  need go
  say "building dcctl from this tree"
  make -C "$repo_root/backend/cli" build >/dev/null
  dcctl="$repo_root/backend/cli/build/dcctl$(go env GOEXE)"
  [[ -x "$dcctl" ]] || fail "dcctl was not built at $dcctl"
}
version_args=()
[[ -z "$version" ]] || version_args=(--version "$version")

# ---------------------------------------------------------------------------
# The phases
# ---------------------------------------------------------------------------

phase_cluster() {
  phase_begin cluster
  cluster_exists && fail "kind cluster $cluster already exists: a clean start needs none (run 'hack/ttft-rig.sh down')"
  [[ ! -e "${HOME:?}/.devicechain/instances/$instance" ]] ||
    fail "$HOME/.devicechain/instances/$instance exists: it belongs to something else; pick DC_TTFT_INSTANCE"
  local published
  published="$(dk ps --format '{{.Names}} {{.Ports}}')"
  if grep -q ":$https_port->" <<<"$published"; then
    fail "host port $https_port is already published by another container; pick DC_TTFT_HTTPS_PORT"
  fi
  # deploy/local/kind-cluster.yaml, less host ports 80 and 1883 and with 443 moved to a loopback
  # port of this rig's own: those host ports belong to whichever cluster took them first.
  cat >"$work/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "ingress-ready=true"
    extraPortMappings:
      - containerPort: 443
        hostPort: $https_port
        listenAddress: "127.0.0.1"
        protocol: TCP
EOF
  kind create cluster --name "$cluster" --kubeconfig "$kubeconfig" --config "$(winpath "$work/kind.yaml")" --wait 120s
  phase_end
}

phase_operators() {
  phase_begin operators
  # --cluster, NOT --kube-context: a named cluster is adopted as a local kind cluster dcctl owns.
  "$dcctl" install local --yes --cluster "$cluster" --no-monitoring "${version_args[@]}" 2>&1
  phase_end
}

# run_bootstrap times dcctl bootstrap's own "[i/N] step" headings, then folds them into the three
# phases. A step runs from its heading to the next heading (the last, to the end of the command).
run_bootstrap() {
  local marks="$work/marks.tsv" end_ms ts name prev_name="" prev_ms=0 pre=0 infra=0 chart=0 ms
  local -a pst
  : >"$marks"
  phase_begin bootstrap
  # The heading test is a glob first: forking sed for every line of a long log would make this reader
  # lag the command it is timing.
  shopt -s extglob
  set +e
  "$dcctl" bootstrap local "$instance" --yes --no-escrow --cluster "$cluster" --host "$host" "${version_args[@]}" 2>&1 |
    while IFS= read -r line; do
      printf '%s\n' "$line"
      if [[ "$line" == *'['*/*']'* ]]; then
        clean="${line//$'\e['*([0-9;])m/}"
        clean="${clean%$'\r'}"
        if [[ "$clean" =~ ^\[[0-9]+/[0-9]+\]\ (.+)$ ]]; then
          printf '%s\t%s\n' "$(now_ms)" "${BASH_REMATCH[1]}" >>"$marks"
        fi
      fi
    done
  pst=("${PIPESTATUS[@]}")
  set -e
  end_ms="$(now_ms)"
  [[ "${pst[0]}" == 0 ]] || fail "dcctl bootstrap exited ${pst[0]}"
  while IFS=$'\t' read -r ts name; do
    if [[ -n "$prev_name" ]]; then printf '%s\t%d\n' "$prev_name" $((ts - prev_ms)) >>"$steps_file"; fi
    prev_name="$name"
    prev_ms="$ts"
  done <"$marks"
  [[ -z "$prev_name" ]] || printf '%s\t%d\n' "$prev_name" $((end_ms - prev_ms)) >>"$steps_file"
  grep -q $'^Wait for readiness\t' "$steps_file" || fail "dcctl bootstrap never reached 'Wait for readiness'"
  grep -q $'^Report access info\t' "$steps_file" || fail "dcctl bootstrap did not finish (no 'Report access info' step)"
  while IFS=$'\t' read -r name ms; do
    case "$name" in
      "Apply infrastructure") infra=$((infra + ms)) ;;
      "Install instance (Helm)" | "Wait for readiness" | "Report access info") chart=$((chart + ms)) ;;
      *) pre=$((pre + ms)) ;;
    esac
  done <"$steps_file"
  printf 'bootstrap_prep\t%d\ninfra\t%d\nchart\t%d\n' "$pre" "$infra" "$chart" >>"$phases_file"
  current_phase=""
  note "bootstrap_prep ${pre} ms, infra ${infra} ms, chart ${chart} ms"
}

# ---------------------------------------------------------------------------
# The API
# ---------------------------------------------------------------------------

api="https://$host:$https_port"
um="$api/api/user-management"
dm="$api/api/device-management"
em="$api/api/event-management"
ingress_ca=""
broker_ca=""
node_ip=""
admin_token=""
tenant_token=""
curl_revoke=()
[[ "$(curl -V 2>/dev/null)" != *[Ss]channel* ]] || curl_revoke=(--ssl-no-revoke)
# A bounded call: an ingress that accepts and never answers must fail the call, or the polling loops
# that wrap it could never reach their own timeout and a hung rig would never record the failure.
curl_api() { curl -sS --connect-timeout "${DC_TTFT_CURL_CONNECT_TIMEOUT:-10}" --max-time "${DC_TTFT_CURL_MAX_TIME:-60}" "${curl_revoke[@]}" --resolve "$host:$https_port:127.0.0.1" --cacert "$(winpath "$ingress_ca")" "$@"; }

# gql <url> <token|-> <query> [variables-json] prints .data; a GraphQL error prints to stderr, returns 1.
gql() {
  local url="$1" token="$2" query="$3" vars="${4:-}" body resp auth=()
  [[ -n "$vars" ]] || vars='{}'
  [[ "$token" == "-" ]] || auth=(-H "Authorization: Bearer $token")
  body="$(jq -n --arg q "$query" --argjson v "$vars" '{query: $q, variables: $v}')"
  resp="$(curl_api -X POST "$url" -H 'Content-Type: application/json' "${auth[@]}" --data-binary @- <<<"$body")" || true
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
adm() { gql "$um/admin/graphql" "$admin_token" "$@"; }
dmq() { gql "$dm/graphql" "$tenant_token" "$@"; }
emq() { gql "$em/graphql" "$tenant_token" "$@"; }
login() {
  gql "$um/graphql" - 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -n --arg e "$1" --arg p "$2" '{e:$e,p:$p}')" | jq -r '.login.identityToken'
}

discover() {
  local secret d
  secret="$(k -n "$namespace" get gateway -o json 2>/dev/null |
    jq -r --arg h "$host" '[.items[].spec.listeners[] | select(.hostname == $h and .tls) | .tls.certificateRefs[]?.name] | .[0] // empty')" || true
  [[ -n "$secret" ]] || secret="$(k -n "$namespace" get ingress -o json | jq -r '[.items[].spec.tls[]?.secretName] | unique | .[0] // empty')"
  [[ -n "$secret" ]] || fail "nothing in $namespace serves TLS for $host"
  ingress_ca="$work/ingress-ca.pem"
  k -n "$namespace" get secret "$secret" -o json | jq -r '.data["ca.crt"] // empty' | base64 -d >"$ingress_ca"
  [[ -s "$ingress_ca" ]] || fail "ingress TLS secret $secret has no ca.crt"
  broker_ca="$work/broker-ca.pem"
  d="$(k -n "$namespace" get secret -o json | jq -r '[.items[] | select(.metadata.name | endswith("nats-tls")) | .data["ca.crt"] // empty][0] // empty')"
  [[ -n "$d" ]] || fail "no *nats-tls secret with a ca.crt in $namespace"
  base64 -d <<<"$d" >"$broker_ca"
  node_ip="$(docker inspect "$cluster-control-plane" -f '{{with index .NetworkSettings.Networks "kind"}}{{.IPAddress}}{{end}}')"
  [[ -n "$node_ip" ]] || fail "cannot read the kind node's address on the kind network"
}

phase_provision() {
  phase_begin provision
  local pw tier ident member_password cred_secret
  pw="$(k -n "$namespace" get secret "dci-$instance-superuser" -o jsonpath='{.data.password}' | base64 -d)"
  [[ -n "$pw" ]] || fail "Secret $namespace/dci-$instance-superuser holds no superuser password"
  admin_token="$(login "superuser@devicechain.local" "$pw")"
  [[ -n "$admin_token" && "$admin_token" != null ]] || fail "could not sign in as the superuser"
  tier="$(adm '{tenantTiers{token}}' | jq -r '.tenantTiers[0].token')"
  [[ -n "$tier" && "$tier" != null ]] || fail "the instance has no tenant tier"
  adm 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
    "$(jq -n --arg t "$tenant" --arg tier "$tier" '{r:{token:$t,name:"TTFT rig",tierToken:$tier}}')" >/dev/null || fail "could not create tenant $tenant"
  member_password="$(openssl rand -base64 24 | tr -d '/+=')Aa1"
  adm 'mutation($e:String!,$p:String!){createIdentity(request:{email:$e,password:$p,enabled:true,systemRoles:[]}){email}}' \
    "$(jq -n --arg e "$member_email" --arg p "$member_password" '{e:$e,p:$p}')" >/dev/null || fail "could not create the tenant member"
  adm 'mutation($e:String!,$t:String!){addMembership(email:$e,tenant:$t,roleTokens:["tenant-admin"]){email}}' \
    "$(jq -n --arg e "$member_email" --arg t "$tenant" '{e:$e,t:$t}')" >/dev/null || fail "could not add the membership"
  ident="$(login "$member_email" "$member_password")"
  tenant_token="$(gql "$um/graphql" - 'mutation($i:String!,$t:String!){selectTenant(identityToken:$i,tenant:$t){accessToken}}' \
    "$(jq -n --arg i "$ident" --arg t "$tenant" '{i:$i,t:$t}')" | jq -r '.selectTenant.accessToken')"
  [[ -n "$tenant_token" && "$tenant_token" != null ]] || fail "could not enter tenant $tenant"
  dmq 'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
    "$(jq -n --arg t "$profile" '{r:{token:$t,name:"TTFT rig device"}}')" >/dev/null || fail "createDeviceProfile"
  dmq 'mutation($t:String!){publishDeviceProfile(token:$t){version}}' "$(jq -n --arg t "$profile" '{t:$t}')" >/dev/null || fail "publishDeviceProfile"
  dmq 'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
    "$(jq -n --arg t "$dtype" --arg p "$profile" '{r:{token:$t,name:"TTFT rig device",profileToken:$p}}')" >/dev/null || fail "createDeviceType"
  cred_secret="$work/secret-token"
  openssl rand -hex 24 | tr -d '\n' >"$cred_secret"
  dmq 'mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}' \
    "$(jq -n --arg t "$device_token" --arg ty "$dtype" '{r:{token:$t,name:$t,deviceTypeToken:$ty}}')" >/dev/null || fail "createDevice"
  dmq 'mutation($r:DeviceCredentialCreateRequest){createDeviceCredential(request:$r){token}}' \
    "$(jq -n --arg t "$device_token-mqtt" --arg d "$device_token" --rawfile s "$cred_secret" \
      '{r:{token:$t,deviceToken:$d,credentialType:"MQTT_BASIC",credentialId:$d,credentialValue:$s,enabled:true}}')" >/dev/null || fail "createDeviceCredential"
  # The tenant member's credentials stay in memory and the work dir; neither is printed.
  phase_end
}

# remove_device_container removes $device_container only if this rig made it (the label carries the
# cluster name). A container that exists under the name but is not ours is somebody else's: refuse
# rather than delete it, whatever the cluster state turns out to be.
remove_device_container() {
  local owner
  owner="$(dk inspect -f '{{index .Config.Labels "devicechain.io/ttft-rig"}}' "$device_container" 2>/dev/null)" || return 0
  [[ "$owner" == "$cluster" ]] ||
    fail "container $device_container exists but is not this rig's (label devicechain.io/ttft-rig=${owner:-<none>}, want $cluster): refusing to remove it; set DC_TTFT_DEVICE_CONTAINER to another name"
  dk rm -f "$device_container" >/dev/null
}

start_container() {
  remove_device_container
  cp "$broker_ca" "$work/broker-ca.pem" 2>/dev/null || true
  dk run -d --init --name "$device_container" --network kind --label devicechain.io/ttft-rig="$cluster" \
    -v "$(winpath "$work"):/work" --entrypoint /bin/sleep "$device_image" infinity >/dev/null
  dx sh -c 'command -v mosquitto_pub' >/dev/null || fail "the device image has no mosquitto_pub"
}

# publish <port> <value>: one measurement at QoS 1 as the token device. Returns mosquitto_pub's status:
# 0 only if the broker accepted the connect AND acknowledged the publish.
publish() {
  # The secret is read inside the container from the mounted work dir, so it never appears on the host
  # docker command line.
  local body
  body="{\"device\":\"$device_token\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$2\"}}]}}"
  dx sh -c 'exec timeout 25 mosquitto_pub -h "$1" -p "$2" -V mqttv311 --cafile /work/broker-ca.pem --insecure -i "$3" -u "$4" -P "$(cat /work/secret-token)" -q 1 -t "$5" -m "$6"' sh \
    "$node_ip" "$1" "$instance:$tenant:$device_token" "$tenant:$device_token" "$instance/$tenant/devices/$device_token/events" "$body" 2>&1
}

# stored <value> prints 1 when the event store returns a measurement of the device with that value.
stored() {
  local r
  r="$(emq 'query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{deviceToken name value}}}' \
    "$(jq -n --arg d "$device_token" '{c:{pageNumber:1,pageSize:50,deviceToken:$d}}')" 2>/dev/null)" || { echo 0; return; }
  jq -r --argjson v "$1" '[.measurementEvents.results[]? | select(.value == $v)] | length | if . > 0 then 1 else 0 end' <<<"$r"
}

# dashboard <value> prints 1 when the query a dashboard chart sends, one-minute buckets (served from the
# continuous aggregate), returns a bucket holding the value.
dashboard() {
  local r start
  start="$(date -u -d '-1 hour' +%Y-%m-%dT%H:%M:%SZ)"
  r="$(emq 'query($c:MeasurementAggregationCriteria!){bucketedMeasurements(criteria:$c){bucketStart name avg count}}' \
    "$(jq -n --arg d "$device_token" --arg s "$start" '{c:{deviceToken:$d,name:"temperature",startTime:$s,intervalSeconds:60}}')" 2>/dev/null)" || { echo 0; return; }
  jq -r --argjson v "$1" '[.bucketedMeasurements[]? | select(.count > 0 and .avg == $v)] | length | if . > 0 then 1 else 0 end' <<<"$r"
}

# first_reading <port> <value>: the whole of phases first_stored and first_dashboard against a port. The
# publish is retried for as long as the BROKER refuses it (the instance is Ready before every
# subscriber is), but a port nothing listens on never connects, so it runs out the timeout and returns
# 1. Sets T_STORED and T_DASH (ms from the first publish attempt).
T_STORED=""
T_DASH=""
PUBLISH_REFUSALS=0
first_reading() {
  local port="$1" val="$2" t0 deadline published=0 out
  t0="$(now_ms)"
  deadline=$((SECONDS + reading_timeout))
  T_STORED=""
  T_DASH=""
  PUBLISH_REFUSALS=0
  while ((SECONDS < deadline)); do
    if ((published == 0)); then
      if out="$(publish "$port" "$val")"; then
        published=1
        note "published $val at port $port"
      else
        PUBLISH_REFUSALS=$((PUBLISH_REFUSALS + 1))
        note "publish refused: ${out//$'\n'/ }"
        sleep 2
        continue
      fi
    fi
    if [[ -z "$T_STORED" && "$(stored "$val")" == 1 ]]; then T_STORED=$(($(now_ms) - t0)); note "stored after ${T_STORED} ms"; fi
    if [[ -z "$T_DASH" && "$(dashboard "$val")" == 1 ]]; then T_DASH=$(($(now_ms) - t0)); note "dashboard query returned it after ${T_DASH} ms"; fi
    [[ -z "$T_STORED" || -z "$T_DASH" ]] || return 0
    sleep 1
  done
  # A publish can reach the broker and still report failure (PUBACK lost), so the absence the negative
  # control relies on is read from the store, not inferred from the refusal.
  if [[ -z "$T_STORED" && "$(stored "$val")" == 1 ]]; then T_STORED=$(($(now_ms) - t0)); fi
  if [[ -z "$T_DASH" && "$(dashboard "$val")" == 1 ]]; then T_DASH=$(($(now_ms) - t0)); fi
  return 1
}

# ---------------------------------------------------------------------------
# run / negative / down
# ---------------------------------------------------------------------------

cmd_down() {
  need kind; need docker; need kubectl
  if docker inspect "$device_container" >/dev/null 2>&1; then
    say "removing the device container $device_container"
    remove_device_container
  fi
  local rec="${HOME:?}/.devicechain/instances/$instance/instance.json" uid="" recctx
  if cluster_exists; then
    uid="$(k get ns kube-system -o jsonpath='{.metadata.uid}' 2>/dev/null)" ||
      fail "kind cluster $cluster exists but $kubeconfig does not reach it: it is not this rig's; refusing to delete it"
    if [[ -f "$rec" ]]; then
      recctx="$(jq -r '.kubeContext // empty' "$rec")"
      [[ "$recctx" == "$kube_context" ]] || fail "$rec names context '$recctx', not $kube_context: not this rig's; refusing to destroy it"
      resolve_dcctl
      say "destroying instance $instance"
      "$dcctl" destroy local "$instance" --yes 2>&1 || echo "dcctl destroy exited non-zero; deleting the cluster anyway"
    fi
    say "deleting kind cluster $cluster"
    kind delete cluster --name "$cluster" --kubeconfig "$kubeconfig"
  fi
  # dcctl keeps the cluster root's state under ~/.devicechain/clusters/<kube-system UID>.
  if [[ -n "$uid" ]]; then
    case "$uid" in . | .. | */* | *\\*) fail "refusing to remove state for cluster UID '$uid'" ;; esac
    rm -rf "${HOME:?}/.devicechain/clusters/${uid:?}"
  fi
  rm -f "$state_dir/kubeconfig"
}

cmd_run() {
  need kind; need kubectl; need docker; need jq; need curl; need openssl
  cmd_checks
  resolve_dcctl
  result_file="$out_dir/ttft-$run_stamp.json"
  git -C "$repo_root" status --porcelain >/dev/null 2>&1 || true
  note "git HEAD $(git -C "$repo_root" rev-parse HEAD); $(git -C "$repo_root" status --porcelain | wc -l | tr -d ' ') dirty path(s)"
  note "dcctl: $dcctl; images: ${version:-tree default}"

  t_start="$(now_ms)"
  phase_cluster
  phase_operators
  run_bootstrap
  local ds_t ds_ms
  ds_t="$(now_ms)"
  discover
  ds_ms=$(($(now_ms) - ds_t))
  phase_provision
  ds_t="$(now_ms)"
  start_container
  ds_ms=$((ds_ms + $(now_ms) - ds_t))
  printf 'device_setup	%d
' "$ds_ms" >>"$phases_file"

  local val="1001.5" ok=0
  phase_begin first_reading
  if first_reading "$mqtt_node_port" "$val"; then ok=1; fi
  current_phase=""
  if ((ok == 0)); then
    current_phase=first_stored
    if [[ -n "$T_STORED" ]]; then current_phase=first_dashboard; fi
    write_result failed "$current_phase"
    fail "no reading reached the dashboard within ${reading_timeout}s (stored: ${T_STORED:-never}); wrote $result_file"
  fi
  printf 'first_stored\t%d\nfirst_dashboard\t%d\n' "$T_STORED" "$T_DASH" >>"$phases_file"
  write_result ok
  if ! verdict="$(judge_result "$result_file")"; then
    echo "$verdict"
    fail "the result did not pass its own judge: $result_file"
  fi
  jq . "$result_file"
  say "TTFT RIG GREEN: $(jq -r '.ttftMs' "$result_file") ms from an empty machine to a reading on a dashboard query; result $result_file"
  if [[ "${DC_TTFT_KEEP:-}" == 1 ]]; then
    note "DC_TTFT_KEEP=1: cluster $cluster left up for 'negative'; remove it with 'hack/ttft-rig.sh down'"
  else
    cmd_down
  fi
}

# negative: the control. The device is aimed at a port nothing serves; the first-reading phase must
# FAIL. Its counterweight, the same phase at the real port, must pass, or a failure here would be
# the instance's and not the control's.
cmd_negative() {
  need kind; need kubectl; need docker; need jq; need curl; need openssl
  cluster_exists || fail "kind cluster $cluster does not exist; run 'DC_TTFT_KEEP=1 hack/ttft-rig.sh run' first"
  k -n "$namespace" get deploy device-management >/dev/null 2>&1 || fail "instance $instance is not on $cluster"
  discover
  # The kept run's tenant and device exist; only their tokens are re-derived, from a fresh tenant.
  tenant="n$(date -u +%H%M%S)"
  member_email="ttft-rig@$tenant.test"
  dtype="ttft-type-$tenant"
  profile="ttft-profile-$tenant"
  phase_provision >/dev/null
  start_container
  reading_timeout="${DC_TTFT_NEGATIVE_TIMEOUT:-45}"
  say "NEGATIVE CONTROL: the device aimed at port $wrong_mqtt_port, where nothing listens"
  if first_reading "$wrong_mqtt_port" "2002.5"; then
    fail "the first-reading phase PASSED with the device aimed at the wrong port: the rig cannot go red"
  fi
  red "no reading reached the store or the dashboard with the device at port $wrong_mqtt_port (stored: ${T_STORED:-never}, dashboard: ${T_DASH:-never})"
  [[ -z "$T_STORED" && -z "$T_DASH" ]] || fail "a reading arrived although the device could not have reached the broker"
  ((PUBLISH_REFUSALS > 0)) || fail "the phase failed, but not because the broker refused the device: the control failed for a reason it did not plant"
  say "COUNTERWEIGHT: the same phase at the real port $mqtt_node_port"
  reading_timeout=240
  first_reading "$mqtt_node_port" "2003.5" || fail "the first-reading phase failed at the right port too: the control above proves nothing"
  pass "negative control: wrong port FAILED the phase, right port passed it (stored ${T_STORED} ms, dashboard ${T_DASH} ms)"
}

case "$rig_cmd" in
  run) cmd_run ;;
  negative) cmd_negative ;;
  checks) cmd_checks ;;
  down) cmd_down ;;
  *) echo "usage: hack/ttft-rig.sh run | negative | checks | down" >&2; exit 2 ;;
esac
