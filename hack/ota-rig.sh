#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The over-the-air update rig: one REAL device takes an update from a deployed instance, end to end.
#
# WHAT THIS GATES
#
# The v1 release candidate. Every piece of the update path is CI-tested on its own: the platform's
# reducer, the download endpoint, the reference device (examples/ota-device) against fakes. What
# no test can show is the CLAIM that a separately built device, speaking only the published
# protocol, takes an image from a dcctl-built instance over its real broker and real ingress,
# replaces its own executable, and proves to the platform which version is running. That needs a
# cluster, so it is a manual rig, like hack/ha-rig.sh and hack/dr-rig.sh, and it follows their one
# rule: a check is worth nothing until it has been shown to fail. Seven negative controls (N1-N7)
# plant a defect each; every one must turn the platform's verdict red AND leave the device running
# what it ran before.
#
#   hack/ota-rig.sh up      create the kind cluster dc-otarig (its own kubeconfig, its own host
#                           port), install, and bootstrap an instance with update-management from
#                           images built from THIS tree under a unique tag; assert the download base
#                           was derived from the ingress host
#   hack/ota-rig.sh run     provision, build the device images, start the device, then the
#                           positive update, N1-N7 and the four-device rollout
#   hack/ota-rig.sh all     up + run
#   hack/ota-rig.sh down    remove the device container, destroy the instance, delete the cluster
#                           and dcctl's local state for it
#
# THE STEPS `run` TAKES (each prints a PASS line naming its evidence):
#
#   1  the update-management config carries downloadBaseUrl == <origin>/api/update-management,
#      and the service says downloadsConfigured
#   2  tenant, profile, type, device, MQTT credential; an ECDSA P-256 and an Ed25519 key made in
#      a temporary directory that is deleted on exit; both registered as signers, only the
#      ECDSA public key in the device's trust set
#   3  images: ota-device 1.0.0 (slot a), 1.0.1, 1.0.4; 1.0.2-crash and 1.0.3-liar (the
#      example's test image, which exits before confirming / says it is another product).
#      Each is uploaded through the streaming endpoint and signed with openssl exactly as the
#      device guide shows; nothing here signs with a Go helper of ours
#   4  ota-boot starts the device in a container on the kind network: outside the cluster, as a
#      real device is, reaching the broker's node port and the ingress by the instance host
#   5  POSITIVE: 1.0.1 is assigned and reaches UPDATED; then, from independent sources, the
#      current assignment is 1.0.1 and UPDATED, the payload's PID changed, the RUNNING boot is
#      not the install boot, slot b hashes to the artifact digest, the RUNNING binary describes
#      itself as 1.0.1, and update_reports_total{verdict="APPLIED"} rose
#   6  NEGATIVE CONTROLS, each asserting the platform's verdict and what the device still runs:
#        N1  1.0.4 signed only by the Ed25519 signer (registered, not trusted by the device)
#            -> FAILED UNKNOWN_SIGNER; the inactive slot is untouched
#        N2  1.0.2-crash -> FAILED ROLLED_BACK, never UPDATED
#        N3  1.0.0, signed -> FAILED DOWNGRADE_REFUSED
#        N4  1.0.4: the device is killed at ~40% of the download, one byte of what it holds is
#            flipped, it restarts -> FAILED VERIFICATION_FAILED, after a RANGE resume (the bytes
#            served after the restart equal exactly size - held)
#        N5  1.0.3-liar -> FAILED IMAGE_MISMATCH; the liar was only ever run as `describe`
#        N6  the step-5 UPDATED checker run against N2's attempt MUST FAIL
#        N7  downloadBaseUrl set to "" by a rig-only helm upgrade ->
#            update_download_grants_total{result="not_configured"} rises and the attempt never
#            leaves INITIATED; the release is then rolled back and downloads work again
#   7  FLEET: three more devices, a static group of four, a rollout of 1.0.4 (ECDSA-signed) with
#      maxInFlight=1: never more than one unfinished attempt (sampled every second), COMPLETED
#      with 4/4 UPDATED and every device describing itself as 1.0.4
#
# THE DEVICE RUNS IN A CONTAINER, AND WHY. A download grant's URL is <origin>/api/update-
# management/..., where <origin> is https://<instance host> on port 443, and the device pins it
# (scheme, host AND port). Another kind cluster on this machine may already hold host port 443,
# so a device on the host could not be pointed at this instance without changing what the
# platform derives, which is the thing under test. A container on the kind network reaches the
# node's ingress on 443 and its MQTT node port directly, with the instance host mapped to the node
# by --add-host: the platform's URL is used exactly as derived. The rig's own GraphQL calls go
# through a loopback-only host port (DC_OTA_RIG_HTTPS_PORT).
#
# ISOLATION. The cluster has its own kubeconfig file (under DC_OTA_RIG_STATE) and every kubectl,
# helm, kind and dcctl call is given it explicitly; this rig never reads or changes another
# kubeconfig's current context. `down` deletes only this rig's cluster and container.
#
# Requires: kind, kubectl, helm, docker, jq, openssl (3.x, for Ed25519 pkeyutl -rawin), curl, ko,
# `tofu` OR `terraform`, and a Go toolchain. It pushes images under a unique tag to the local
# registry at localhost:5000 (dcctl's developer path); it never removes anything from it.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="${DC_OTA_RIG_CLUSTER:-dc-otarig}"
kube_context="kind-$cluster"
instance="${DC_OTA_RIG_INSTANCE:-otarig}"
namespace="dci-$instance"
host="${DC_OTA_RIG_HOST:-$instance.devicechain.test}"
https_port="${DC_OTA_RIG_HTTPS_PORT:-18443}"
state_dir="${DC_OTA_RIG_STATE:-$repo_root/.ota-rig}"
device_container="${DC_OTA_RIG_DEVICE_CONTAINER:-$cluster-devices}"
device_image="${DC_OTA_RIG_DEVICE_IMAGE:-alpine:3.22}"
min_free_gb="${DC_OTA_RIG_MIN_FREE_GB:-100}"
tenant="${DC_OTA_RIG_TENANT:-otarig}"
member_email="ota-rig@$tenant.test"
profile="ota-rig-profile"
dtype="ota-rig-type"
group="ota-rig-fleet"
product="ota-device-example"
exe="$(go env GOEXE)"
dcctl="$repo_root/backend/cli/build/dcctl$exe"

# Windows binaries (kind, kubectl, helm, dcctl) are handed paths in the mixed C:/... form, which
# Git Bash's own tools read too. dx, dxi and dk set MSYS_NO_PATHCONV so Git Bash does not rewrite
# the in-container paths they are given (/dev1/...) into Windows ones. It is not exported: make
# and go need the conversion.
winpath() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }
mkdir -p "$state_dir"
kubeconfig="$(winpath "$state_dir/kubeconfig")"
export KUBECONFIG="$kubeconfig"

rig_tmp="$(mktemp -d "${TMPDIR:-/tmp}/dc-ota-rig.XXXXXX")"
cleanup() {
  # The private keys live only here. Removed however the run ends, including a failed check.
  rm -rf "$rig_tmp"
  [[ -z "${pf_pid:-}" ]] || kill "$pf_pid" 2>/dev/null || true
}
trap cleanup EXIT

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
pass() { printf '\033[1;32m    PASS %s\033[0m\n' "$*"; }
red() { printf '\033[1;33m    RED (as designed) %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; dump_device_logs >&2 || true; exit 1; }
warn() { printf '\n\033[1;33mWARN: %s\033[0m\n' "$*" >&2; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }

# The capability in a grant URL is a bearer secret: nothing this rig prints may carry one.
redact() { sed -u -E 's/cap=[^&" ]*/cap=[redacted]/g; s/^([[:space:]]*password:[[:space:]]*).+$/\1[redacted]/'; }

# jq on Windows writes CRLF unless told not to, and "true\r" is not "true". -b is that switch.
if command jq -b -n 1 >/dev/null 2>&1; then jq() { command jq -b "$@"; }; fi

k() { kubectl --kubeconfig "$kubeconfig" --context "$kube_context" "$@"; }
dx() { MSYS_NO_PATHCONV=1 docker exec "$device_container" "$@"; }
dxi() { MSYS_NO_PATHCONV=1 docker exec -i "$device_container" "$@"; }
dk() { MSYS_NO_PATHCONV=1 docker "$@"; }

[[ "$instance" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || fail "DC_OTA_RIG_INSTANCE must be a DNS label, got '$instance'"

# ---------------------------------------------------------------------------
# up / down
# ---------------------------------------------------------------------------

build_dcctl() {
  say "building dcctl"
  make -C "$repo_root/backend/cli" build >/dev/null
  [[ -x "$dcctl" ]] || fail "dcctl was not built at $dcctl"
}

# disk_guard refuses to start a build that would leave the system disk below the floor: the images,
# the node's copy of them and the Go build cache all land there.
disk_guard() {
  local free
  free="$(df -Pk "$HOME" | awk 'NR==2 {printf "%d", $4/1024/1024}')"
  [[ "$free" -ge "$min_free_gb" ]] ||
    fail "only ${free} GB free on the disk holding $HOME; this rig needs at least $min_free_gb (DC_OTA_RIG_MIN_FREE_GB)"
  note "disk: ${free} GB free (floor $min_free_gb)"
}

cluster_exists() { grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; }

create_cluster() {
  if cluster_exists; then
    k get ns kube-system >/dev/null 2>&1 ||
      fail "kind cluster $cluster exists but is not in $kubeconfig; it was not made by this rig (pick DC_OTA_RIG_CLUSTER)"
    note "kind cluster $cluster already exists; reusing it"
    return
  fi
  say "creating kind cluster $cluster (kubeconfig $kubeconfig, https on 127.0.0.1:$https_port)"
  # deploy/local/kind-cluster.yaml, less its host ports 80 and 1883 and with 443 moved to a
  # loopback port of this rig's own: those host ports belong to whichever cluster took them first.
  # The device reaches the node directly on the kind network and needs none of them.
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
      - containerPort: 443
        hostPort: $https_port
        listenAddress: "127.0.0.1"
        protocol: TCP
EOF
  kind create cluster --name "$cluster" --kubeconfig "$kubeconfig" --config "$(winpath "$rig_tmp/kind.yaml")" --wait 120s
}

cmd_up() {
  need kind; need kubectl; need docker; need jq; need ko; need go
  disk_guard
  create_cluster
  if k get ns "$namespace" >/dev/null 2>&1; then
    note "instance $instance already exists on $cluster; reusing it"
    return
  fi
  [[ ! -e "${HOME:?}/.devicechain/instances/$instance" ]] ||
    fail "$HOME/.devicechain/instances/$instance exists but the instance is not on $cluster: it belongs to something else; pick DC_OTA_RIG_INSTANCE"
  build_dcctl
  # A unique tag: the chart pulls IfNotPresent, so a reused tag serves a node's cached layer.
  local tag
  tag="ota-$(git -C "$repo_root" rev-parse --short=8 HEAD)-$(date -u +%Y%m%dT%H%M%SZ)"
  printf '%s\n' "$tag" >"$state_dir/image-tag"
  say "installing the cluster prerequisites (operator image $tag built from this tree)"
  # --no-monitoring: the rig reads the service's own /metrics through the API server, and the
  # monitoring stack is the largest thing a cluster install pulls.
  #
  # --cluster, NOT --kube-context. The cluster is named, so dcctl adopts it as a local kind cluster
  # of its own (the context is found in $KUBECONFIG). With --kube-context it is "a cluster dcctl
  # did not create": the object store defaults to none (every artifact upload refused) and an
  # explicit filesystem store is refused because two pods mount its ReadWriteOnce claim, which
  # dcctl colocates only on a cluster it owns.
  "$dcctl" install local --yes --cluster "$cluster" --no-monitoring --build --version "$tag" 2>&1 | redact
  say "bootstrapping $instance with update-management on top of the default profile"
  # --enable-area rather than a profile: it is what an operator does today to get updates, and it
  # keeps the instance small. --no-escrow: this instance exists to be torn down.
  "$dcctl" bootstrap local "$instance" --yes --no-escrow --cluster "$cluster" \
    --host "$host" --build --version "$tag" --enable-area update-management 2>&1 | redact
  say "UP COMPLETE: run 'hack/ota-rig.sh run'"
}

remove_cluster_state() {
  # dcctl keeps the cluster root's state under ~/.devicechain/clusters/<kube-system UID>, which
  # `dcctl destroy` leaves (the cluster outlives its instances). Same shape as hack/ha-rig.sh.
  local uid="${1:-}" dir clusters="${HOME:?}/.devicechain/clusters"
  if [[ -n "$uid" ]]; then
    case "$uid" in . | .. | */* | *\\*) fail "refusing to remove state for cluster UID '$uid'" ;; esac
    rm -rf "${clusters:?}/$uid"
  fi
  [[ -d "$clusters" ]] || return 0
  for dir in "$clusters"/*/; do
    [[ -f "$dir/cluster.json" ]] || continue
    if [[ "$(jq -r '.kubeContext // empty' "$dir/cluster.json" 2>/dev/null || true)" == "$kube_context" ]]; then
      rm -rf "${dir:?}"
    fi
  done
}

cmd_down() {
  need kind; need kubectl; need jq; need docker
  if docker inspect "$device_container" >/dev/null 2>&1; then
    say "removing the device container $device_container"
    docker rm -f "$device_container" >/dev/null
  fi
  local uid=""
  if cluster_exists; then
    uid="$(k get ns kube-system -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
    if [[ -d "${HOME:?}/.devicechain/instances/$instance" ]]; then
      [[ -x "$dcctl" ]] || build_dcctl
      say "destroying instance $instance"
      "$dcctl" destroy local "$instance" --yes 2>&1 | redact ||
        warn "dcctl destroy exited non-zero; deleting the cluster anyway"
    fi
    say "deleting kind cluster $cluster"
    kind delete cluster --name "$cluster" --kubeconfig "$kubeconfig"
  fi
  remove_cluster_state "$uid"
  rm -f "$state_dir/kubeconfig" "$state_dir/image-tag"
}

# ---------------------------------------------------------------------------
# API helpers
# ---------------------------------------------------------------------------

origin="https://$host"
api="https://$host:$https_port"
um="$api/api/user-management"
dm="$api/api/device-management"
upd="$api/api/update-management"
ingress_ca=""
admin_token=""
tenant_token=""

# curl_api reaches the instance through the rig's loopback port with the instance host as the name
# TLS and the ingress route see, verified against the instance's own CA.
# Schannel (the curl Git for Windows ships) also asks the CA for a revocation list, which a private
# instance CA does not publish; --ssl-no-revoke drops only that lookup, never the verification.
curl_revoke=()
[[ "$(curl -V 2>/dev/null)" != *[Ss]channel* ]] || curl_revoke=(--ssl-no-revoke)
curl_api() { curl -sS "${curl_revoke[@]}" --resolve "$host:$https_port:127.0.0.1" --cacert "$(winpath "$ingress_ca")" "$@"; }

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
updq() { gql "$upd/graphql" "$tenant_token" "$@"; }

login() {
  gql "$um/graphql" - 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -n --arg e "$1" --arg p "$2" '{e:$e,p:$p}')" | jq -r '.login.identityToken'
}

# ---------------------------------------------------------------------------
# Reading the platform: the update service's metrics, and attempts
# ---------------------------------------------------------------------------

um_pods() {
  k -n "$namespace" get pods -l "$um_selector" --field-selector=status.phase=Running -o name
}

# um_metric <metric suffix> <label="value"> prints the sum of that series across every running
# update-management pod, read through the API server's pod proxy. A pod whose metrics cannot be read
# FAILS the rig: reading it as zero would let a control "see" a number it never measured.
um_metric() {
  local name="$1" label="$2" pod body total=0 n pods
  pods="$(um_pods)"
  [[ -n "$pods" ]] || fail "no running update-management pod to read metrics from"
  for pod in $pods; do
    body="$(k get --raw "/api/v1/namespaces/$namespace/pods/${pod#pod/}:$um_port/proxy/metrics")" ||
      fail "could not read the metrics of $pod"
    # The series name ends in _<name>, with or without labels; an empty label matches every series.
    n="$(awk -v m="_${name}" -v l="$label" '
      /^#/ { next }
      { split($1, a, "{"); if (substr(a[1], length(a[1]) - length(m) + 1) != m) next; found = 1
        if (l == "" || index($1, l)) s += $NF }
      END { if (!found) exit 3; printf "%d", s }' <<<"$body")" ||
      fail "$pod exposes no *_${name} series; the metric was renamed"
    total=$((total + n))
  done
  printf '%s\n' "$total"
}

assignment_json() {
  updq 'query($t:String!){updateAssignment(token:$t){token current deviceToken artifact{token version}
    attempt{state targetVersion targetDigest progress{bytes total} failure{stage code detail platform}}}}' \
    "$(jq -n --arg t "$1" '{t:$t}')" | jq -c '.updateAssignment'
}

terminal_state() { [[ "$1" =~ ^(UPDATED|FAILED|TIMED_OUT|CANCELLED)$ ]]; }

# wait_terminal <assignment> <seconds> prints the assignment once its attempt is terminal.
wait_terminal() {
  local a="$1" deadline=$((SECONDS + $2)) j st
  while ((SECONDS < deadline)); do
    j="$(assignment_json "$a")" || j=""
    st="$(jq -r '.attempt.state // empty' <<<"$j")"
    if terminal_state "$st"; then printf '%s\n' "$j"; return 0; fi
    sleep 2
  done
  fail "assignment $a did not finish within $2 s; last seen: ${j:-nothing}"
}

assign() {
  updq 'mutation($d:String!,$a:String!){assignArtifact(deviceToken:$d,artifactToken:$a){token}}' \
    "$(jq -n --arg d "$1" --arg a "$2" '{d:$d,a:$a}')" | jq -r '.assignArtifact.token'
}

# expect_failed <assignment-json> <code>: the platform's verdict for a planted defect.
expect_failed() {
  local j="$1" code="$2" st got
  st="$(jq -r '.attempt.state' <<<"$j")"
  got="$(jq -r '.attempt.failure.code // empty' <<<"$j")"
  [[ "$st" == FAILED && "$got" == "$code" ]] ||
    fail "expected FAILED $code, the platform says $st ${got:-(no code)}: $(jq -c '.attempt' <<<"$j")"
  note "platform: FAILED $code at stage $(jq -r '.attempt.failure.stage' <<<"$j"): $(jq -r '.attempt.failure.detail' <<<"$j")"
}

# ---------------------------------------------------------------------------
# Reading the device: what it runs, read from the container, not from the platform
# ---------------------------------------------------------------------------

# payload_pid <n> prints the PID of device n's running payload (an ota-device image under its slots).
payload_pid() {
  dx sh -c 'for p in /proc/[0-9]*; do e="$(readlink "$p/exe" 2>/dev/null)" || continue
    case "$e" in /dev'"$1"'/slots/*/ota-device) echo "${p#/proc/}";; esac; done' | head -1
}

# wait_payload <n> waits for device n's payload to be running and connected (its boot line printed).
wait_payload() {
  local n="$1" deadline=$((SECONDS + 60)) pid
  while ((SECONDS < deadline)); do
    pid="$(payload_pid "$n")"
    [[ -n "$pid" ]] && { printf '%s\n' "$pid"; return 0; }
    sleep 1
  done
  fail "device $n has no running payload"
}

running_exe() { dx readlink "/proc/$1/exe"; }
# describe_running <pid> asks the binary the process is executing (its /proc exe), not a slot file.
describe_running() { dx "$(running_exe "$1")" describe; }
# last_boot <n> prints the bootId of device n's latest payload start, from its own log line.
last_boot() { dx sh -c "grep -o 'bootId [^ ]*' /dev$1/device.log | tail -1 | cut -d' ' -f2"; }
boot_state() { dx cat "/dev$1/boot.json"; }
slot_hash() { dx sh -c "sha256sum /dev$1/slots/$2/ota-device 2>/dev/null | cut -d' ' -f1"; }

dump_device_logs() {
  [[ -n "${device_container:-}" ]] && docker inspect "$device_container" >/dev/null 2>&1 || return 0
  local n
  for n in 1 2 3 4; do
    dx test -f "/dev$n/device.log" 2>/dev/null || continue
    printf '\n---- device %s log (last 40 lines) ----\n' "$n"
    dx tail -40 "/dev$n/device.log" | redact
  done
}

# check_updated <n> <assignment> <version> <pid-before> <boot-before> <applied-before> returns 0
# only if EVERY source agrees the update took; otherwise it prints which one disagrees and returns
# 1. It is a function that RETURNS rather than fails so that N6 can run it against an attempt that
# did not take and require it to go red.
check_updated() {
  local n="$1" a="$2" want="$3" pid0="$4" boot0="$5" applied0="$6" j pid boot slot digest h id applied
  j="$(assignment_json "$a")" || { echo "the assignment could not be read" >&2; return 1; }
  [[ "$(jq -r '.current' <<<"$j")" == true ]] || { echo "the assignment is not the device's current one" >&2; return 1; }
  [[ "$(jq -r '.attempt.state' <<<"$j")" == UPDATED ]] ||
    { echo "the platform says $(jq -r '.attempt.state' <<<"$j"), not UPDATED" >&2; return 1; }
  [[ "$(jq -r '.attempt.targetVersion' <<<"$j")" == "$want" ]] ||
    { echo "the platform's target is $(jq -r '.attempt.targetVersion' <<<"$j"), not $want" >&2; return 1; }
  pid="$(payload_pid "$n")"
  [[ -n "$pid" && "$pid" != "$pid0" ]] || { echo "the payload PID did not change (was $pid0, is ${pid:-none})" >&2; return 1; }
  boot="$(last_boot "$n")"
  [[ -n "$boot" && "$boot" != "$boot0" ]] || { echo "the running boot is the install boot ($boot0)" >&2; return 1; }
  slot="$(boot_state "$n" | jq -r '.confirmed')"
  digest="$(jq -r '.attempt.targetDigest' <<<"$j")"
  h="$(slot_hash "$n" "$slot")"
  [[ "sha256:$h" == "$digest" ]] || { echo "confirmed slot $slot hashes to sha256:$h, the artifact is $digest" >&2; return 1; }
  [[ "$(running_exe "$pid")" == "/dev$n/slots/$slot/ota-device" ]] ||
    { echo "the payload runs $(running_exe "$pid"), not the confirmed slot $slot" >&2; return 1; }
  id="$(describe_running "$pid")"
  [[ "$(jq -r '.version' <<<"$id")" == "$want" && "$(jq -r '.product' <<<"$id")" == "$product" ]] ||
    { echo "the running binary describes itself as $id" >&2; return 1; }
  applied="$(um_metric update_reports_total 'verdict="APPLIED"')"
  ((applied > applied0)) || { echo "update_reports_total{verdict=\"APPLIED\"} did not rise ($applied0 -> $applied)" >&2; return 1; }
  note "platform: assignment $a current, UPDATED, target $want"
  note "device: payload PID $pid0 -> $pid; bootId $boot0 -> $boot"
  note "device: confirmed slot $slot = sha256:$h = artifact digest; the running binary says $id"
  note "metrics: update_reports_total{verdict=\"APPLIED\"} $applied0 -> $applied"
}

# ---------------------------------------------------------------------------
# Step 1: the derived download base (S0)
# ---------------------------------------------------------------------------

um_selector=""
um_port=""

discover() {
  local d
  k -n "$namespace" get deploy update-management >/dev/null 2>&1 ||
    fail "the instance has no update-management Deployment; was it bootstrapped with --enable-area update-management?"
  um_selector="$(k -n "$namespace" get deploy update-management -o json |
    jq -r '.spec.selector.matchLabels | to_entries | map("\(.key)=\(.value)") | join(",")')"
  um_port="$(k -n "$namespace" get deploy update-management -o json |
    jq -r '[.spec.template.spec.containers[0].ports[] | select(.name == "graphql" or .name == "http" or .name == "metrics")][0].containerPort // empty')"
  [[ -n "$um_port" ]] || fail "update-management exposes no graphql/http/metrics port to read metrics from"
  # The ingress certificate's CA: the device's download trust anchor and the rig's API anchor.
  local secret
  # The front end is a Gateway (or, on an older install, an Ingress): its TLS secret either way.
  secret="$(k -n "$namespace" get gateway -o json 2>/dev/null |
    jq -r --arg h "$host" '[.items[].spec.listeners[] | select(.hostname == $h and .tls) | .tls.certificateRefs[]?.name] | .[0] // empty')"
  [[ -n "$secret" ]] || secret="$(k -n "$namespace" get ingress -o json | jq -r '[.items[].spec.tls[]?.secretName] | unique | .[0] // empty')"
  [[ -n "$secret" ]] || fail "nothing in $namespace serves TLS for $host; this rig needs a TLS instance (not --no-tls)"
  ingress_ca="$rig_tmp/ingress-ca.pem"
  k -n "$namespace" get secret "$secret" -o json | jq -r '.data["ca.crt"] // empty' | base64 -d >"$ingress_ca"
  [[ -s "$ingress_ca" ]] || fail "ingress TLS secret $secret has no ca.crt"
  broker_ca="$rig_tmp/broker-ca.pem"
  d="$(k -n "$namespace" get secret -o json | jq -r '[.items[] | select(.metadata.name | endswith("nats-tls")) | .data["ca.crt"] // empty][0] // empty')"
  [[ -n "$d" ]] || fail "no *nats-tls secret with a ca.crt in $namespace: the broker's CA cannot be read"
  base64 -d <<<"$d" >"$broker_ca"
  node_ip="$(docker inspect "$cluster-control-plane" -f '{{with index .NetworkSettings.Networks "kind"}}{{.IPAddress}}{{end}}')"
  [[ -n "$node_ip" ]] || fail "cannot read the kind node's address on the kind network"
}

step1_download_base() {
  say "STEP 1: the download base is derived from the ingress host"
  local cfg base
  cfg="$(k -n "$namespace" get configmap -o json |
    jq -r '[.items[] | .data["update-management"] // empty][0] // empty')"
  [[ -n "$cfg" ]] || fail "no ConfigMap in $namespace carries the update-management config"
  base="$(jq -r '.updates.downloadBaseUrl // "<absent>"' <<<"$cfg")"
  [[ "$base" == "$origin/api/update-management" ]] ||
    fail "the rendered downloadBaseUrl is '$base', expected '$origin/api/update-management'"
  pass "rendered updates.downloadBaseUrl = $base"
}

# ---------------------------------------------------------------------------
# Step 2: tenant, model, devices, keys, signers
# ---------------------------------------------------------------------------

run_id=""
member_password=""

step2_provision() {
  say "STEP 2: tenant $tenant, profile, type, device, credential, signers"
  local pw tier ident
  pw="$(k -n "$namespace" get secret "dci-$instance-superuser" -o jsonpath='{.data.password}' | base64 -d)"
  [[ -n "$pw" ]] || fail "Secret $namespace/dci-$instance-superuser holds no superuser password"
  admin_token="$(login "superuser@devicechain.local" "$pw")"
  [[ -n "$admin_token" && "$admin_token" != null ]] || fail "could not sign in as the superuser"
  tier="$(adm '{tenantTiers{token}}' | jq -r '.tenantTiers[0].token')"
  [[ -n "$tier" && "$tier" != null ]] || fail "the instance has no tenant tier"
  adm 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
    "$(jq -n --arg t "$tenant" --arg tier "$tier" '{r:{token:$t,name:"OTA rig",tierToken:$tier}}')" >/dev/null ||
    fail "could not create tenant $tenant"
  member_password="$(openssl rand -base64 24 | tr -d '/+=')Aa1"
  adm 'mutation($e:String!,$p:String!){createIdentity(request:{email:$e,password:$p,enabled:true,systemRoles:[]}){email}}' \
    "$(jq -n --arg e "$member_email" --arg p "$member_password" '{e:$e,p:$p}')" >/dev/null ||
    fail "could not create the tenant member"
  adm 'mutation($e:String!,$t:String!){addMembership(email:$e,tenant:$t,roleTokens:["tenant-admin"]){email}}' \
    "$(jq -n --arg e "$member_email" --arg t "$tenant" '{e:$e,t:$t}')" >/dev/null || fail "could not add the membership"
  ident="$(login "$member_email" "$member_password")"
  tenant_token="$(gql "$um/graphql" - 'mutation($i:String!,$t:String!){selectTenant(identityToken:$i,tenant:$t){accessToken}}' \
    "$(jq -n --arg i "$ident" --arg t "$tenant" '{i:$i,t:$t}')" | jq -r '.selectTenant.accessToken')"
  [[ -n "$tenant_token" && "$tenant_token" != null ]] || fail "could not enter tenant $tenant"

  dmq 'mutation($r:DeviceProfileCreateRequest){createDeviceProfile(request:$r){token}}' \
    "$(jq -n --arg t "$profile" '{r:{token:$t,name:"OTA rig device"}}')" >/dev/null || fail "createDeviceProfile"
  dmq 'mutation($t:String!){publishDeviceProfile(token:$t){version}}' "$(jq -n --arg t "$profile" '{t:$t}')" >/dev/null ||
    fail "publishDeviceProfile"
  dmq 'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
    "$(jq -n --arg t "$dtype" --arg p "$profile" '{r:{token:$t,name:"OTA rig device",profileToken:$p}}')" >/dev/null ||
    fail "createDeviceType"

  updq '{updateManagementInfo{downloadsConfigured allowUnverifiedArtifacts}}' >"$rig_tmp/info.json" || fail "updateManagementInfo"
  [[ "$(jq -r '.updateManagementInfo.downloadsConfigured' "$rig_tmp/info.json")" == true ]] ||
    fail "the service says downloads are not configured: $(cat "$rig_tmp/info.json")"
  [[ "$(jq -r '.updateManagementInfo.allowUnverifiedArtifacts' "$rig_tmp/info.json")" == false ]] ||
    fail "the instance allows unverified artifacts; the rig runs signed-by-default"
  pass "updateManagementInfo: downloadsConfigured=true, allowUnverifiedArtifacts=false"

  say "keys: ECDSA P-256 (trusted by the device) and Ed25519 (registered, NOT trusted), in $rig_tmp"
  local kind keyid
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$rig_tmp/ecdsa.pem" 2>/dev/null
  openssl genpkey -algorithm ED25519 -out "$rig_tmp/ed25519.pem" 2>/dev/null
  for kind in ecdsa ed25519; do
    openssl pkey -in "$rig_tmp/$kind.pem" -pubout -out "$rig_tmp/$kind.pub.pem"
    updq 'mutation($n:String!,$p:String!){registerArtifactSigner(name:$n,publicKeyPem:$p){token keyId algorithm}}' \
      "$(jq -n --arg n "ota-rig $kind" --rawfile p "$rig_tmp/$kind.pub.pem" '{n:$n,p:$p}')" >"$rig_tmp/signer-$kind.json" ||
      fail "registerArtifactSigner $kind"
    keyid="$(openssl pkey -in "$rig_tmp/$kind.pem" -pubout -outform DER | openssl dgst -sha256 -r | cut -d' ' -f1)"
    [[ "$(jq -r '.registerArtifactSigner.keyId' "$rig_tmp/signer-$kind.json")" == "$keyid" ]] ||
      fail "the platform's keyId for $kind is not the guide's SHA-256 of the DER public key ($keyid)"
    note "signer $kind: $(jq -c '.registerArtifactSigner' "$rig_tmp/signer-$kind.json")"
  done
  cp "$rig_tmp/ecdsa.pub.pem" "$rig_tmp/trust.pem"
  pass "both signers registered; keyIds match the guide's openssl recipe; trust set = the ECDSA key only"
}

# create_device <n> creates device n and its MQTT credential; the secret is written to rig_tmp only.
create_device() {
  local n="$1" dev="ota-dev-$1" secret
  dmq 'mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}' \
    "$(jq -n --arg t "$dev" --arg ty "$dtype" '{r:{token:$t,name:$t,deviceTypeToken:$ty}}')" >/dev/null || fail "createDevice $dev"
  secret="$(openssl rand -hex 24)"
  printf '%s' "$secret" >"$rig_tmp/secret-$n"
  dmq 'mutation($r:DeviceCredentialCreateRequest){createDeviceCredential(request:$r){token}}' \
    "$(jq -n --arg t "$dev-mqtt" --arg d "$dev" --arg c "$dev" --rawfile s "$rig_tmp/secret-$n" \
      '{r:{token:$t,deviceToken:$d,credentialType:"MQTT_BASIC",credentialId:$c,credentialValue:$s,enabled:true}}')" >/dev/null ||
    fail "createDeviceCredential $dev"
}

# ---------------------------------------------------------------------------
# Step 3: images, built from the example and uploaded + signed as the public guide says
# ---------------------------------------------------------------------------

images="$rig_tmp/images"
pad_mb="${DC_OTA_RIG_PAD_MB:-48}"

build_images() {
  say "STEP 3: building the device images (linux/amd64, static)"
  mkdir -p "$images"
  local ex="$repo_root/examples/ota-device" bi="devicechain.io/examples/ota-device/internal/buildinfo" v
  (cd "$ex" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$images/ota-boot" ./cmd/ota-boot)
  for v in 1.0.0 1.0.1 1.0.4; do
    (cd "$ex" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-X $bi.Version=$v" -o "$images/ota-device-$v" ./cmd/ota-device)
  done
  # 1.0.2-crash and 1.0.3-liar are the example's own test image (internal/testutil/fakeimage):
  # crash exits 1 before it confirms; the liar says it is product "other". Both append their
  # arguments to ran.marker beside themselves, which is how N5 proves the liar was only ever
  # asked to `describe`.
  (cd "$ex" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags "-X main.version=1.0.2 -X main.mode=crash" -o "$images/ota-device-1.0.2-crash" ./internal/testutil/fakeimage)
  (cd "$ex" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags "-X main.version=1.0.3 -X main.product=other" -o "$images/ota-device-1.0.3-liar" ./internal/testutil/fakeimage)
  # 1.0.4 carries $pad_mb MiB of trailing random bytes, which an ELF loader ignores: N4 must stop
  # the device part-way through a download, and an unpadded image is fetched in milliseconds.
  head -c "$((pad_mb * 1024 * 1024))" /dev/urandom >>"$images/ota-device-1.0.4"
  wc -c "$images"/* | sed "s/^/    /"
}

# publish <artifact-token> <name> <version> <file> <signer: ecdsa|ed25519>
publish() {
  local tok="$1" name="$2" ver="$3" file="$4" signer="$5" size digest sig code
  size="$(wc -c <"$file" | tr -d ' ')"
  digest="sha256:$(openssl dgst -sha256 -r "$file" | cut -d' ' -f1)"
  updq 'mutation($r:ArtifactCreateRequest!){createArtifact(request:$r){token state}}' \
    "$(jq -n --arg t "$tok" --arg n "$name" --arg v "$ver" --arg s "$size" --arg d "$digest" --arg p "$profile" \
      '{r:{token:$t,kind:"FIRMWARE",name:$n,version:$v,component:"firmware",sizeBytes:$s,digest:$d,
           mediaType:"application/octet-stream",compatibleProfiles:[$p]}}')" >/dev/null || fail "createArtifact $tok"
  code="$(curl_api -o "$rig_tmp/upload.out" -w '%{http_code}' -X PUT "$upd/updates/artifacts/$tok/content" \
    -H "Authorization: Bearer $tenant_token" -H 'Content-Type: application/octet-stream' --data-binary "@$(winpath "$file")")"
  [[ "$code" == 2* ]] || fail "upload of $tok answered $code: $(head -c 300 "$rig_tmp/upload.out")"
  # The guide's recipes, verbatim in effect: ECDSA signs SHA-256(file); Ed25519 signs the 32 digest bytes.
  case "$signer" in
    ecdsa) sig="$(openssl dgst -sha256 -sign "$rig_tmp/ecdsa.pem" -binary "$file" | base64 | tr -d '\n')" ;;
    ed25519)
      openssl dgst -sha256 -binary "$file" >"$rig_tmp/digest.bin"
      sig="$(openssl pkeyutl -sign -rawin -inkey "$rig_tmp/ed25519.pem" -in "$rig_tmp/digest.bin" | base64 | tr -d '\n')" ;;
  esac
  updq 'mutation($a:String!,$s:String!,$g:String!){addArtifactSignature(artifactToken:$a,signerToken:$s,signature:$g){state trustStatus}}' \
    "$(jq -n --arg a "$tok" --arg s "$(jq -r '.registerArtifactSigner.token' "$rig_tmp/signer-$signer.json")" --arg g "$sig" '{a:$a,s:$s,g:$g}')" \
    >"$rig_tmp/art.json" || fail "addArtifactSignature $tok"
  [[ "$(jq -r '.addArtifactSignature.state + " " + .addArtifactSignature.trustStatus' "$rig_tmp/art.json")" == "AVAILABLE SIGNED" ]] ||
    fail "artifact $tok is $(cat "$rig_tmp/art.json") after upload and signature"
  note "$tok  $name $ver  $size bytes  $digest  AVAILABLE SIGNED ($signer)"
}

step3_artifacts() {
  build_images
  say "STEP 3: uploading through the streaming endpoint and signing with openssl"
  publish fw-100 ota-device 1.0.0 "$images/ota-device-1.0.0" ecdsa
  publish fw-101 ota-device 1.0.1 "$images/ota-device-1.0.1" ecdsa
  publish fw-102 ota-device 1.0.2 "$images/ota-device-1.0.2-crash" ecdsa
  publish fw-103 ota-device 1.0.3 "$images/ota-device-1.0.3-liar" ecdsa
  publish fw-104 ota-device 1.0.4 "$images/ota-device-1.0.4" ecdsa
  # The same bytes as fw-104 under another name, signed ONLY by the key the device does not hold.
  publish fw-104-ed ota-device-ed25519 1.0.4 "$images/ota-device-1.0.4" ed25519
  pass "six artifacts AVAILABLE and SIGNED"
}

# ---------------------------------------------------------------------------
# Step 4: the device, outside the cluster
# ---------------------------------------------------------------------------

start_container() {
  dk rm -f "$device_container" >/dev/null 2>&1 || true
  dk run -d --init --name "$device_container" --network kind --add-host "$host:$node_ip" \
    --label devicechain.io/ota-rig="$cluster" "$device_image" sleep infinity >/dev/null
  dx mkdir -p /opt/ota
  dk cp "$(winpath "$images/ota-boot")" "$device_container:/opt/ota/ota-boot"
  dk cp "$(winpath "$images/ota-device-1.0.0")" "$device_container:/opt/ota/ota-device-1.0.0"
  dk cp "$(winpath "$broker_ca")" "$device_container:/opt/ota/broker-ca.pem"
  dk cp "$(winpath "$ingress_ca")" "$device_container:/opt/ota/ingress-ca.pem"
  dk cp "$(winpath "$rig_tmp/trust.pem")" "$device_container:/opt/ota/trust.pem"
  dx chmod 0755 /opt/ota/ota-boot /opt/ota/ota-device-1.0.0
}

# start_device <n> starts device n's launcher; the first start also lays slot a down with 1.0.0.
start_device() {
  local n="$1" dev="ota-dev-$1"
  if ! dx test -f "/dev$n/start.sh"; then
    dx mkdir -p "/dev$n/slots/a"
    dx cp /opt/ota/ota-device-1.0.0 "/dev$n/slots/a/ota-device"
    dk cp "$(winpath "$rig_tmp/secret-$n")" "$device_container:/dev$n/secret"
    dxi sh -c "cat > /dev$n/start.sh" <<EOF
#!/bin/sh
# Started by hack/ota-rig.sh. The PID written here is the launcher's: exec keeps it.
cd /dev$n
echo \$\$ > /dev$n/boot.pid
OTA_CREDENTIAL_SECRET="\$(cat /dev$n/secret)" exec /opt/ota/ota-boot --root /dev$n --confirm-timeout 60s -- \\
  --root /dev$n --broker ssl://$node_ip:31883 --ca /opt/ota/broker-ca.pem --tls-server-name localhost \\
  --instance $instance --tenant $tenant --device $dev --credential-id $dev \\
  --origin $origin --download-ca /opt/ota/ingress-ca.pem --trust /opt/ota/trust.pem >> /dev$n/device.log 2>&1
EOF
  fi
  dk exec -d "$device_container" sh "/dev$n/start.sh"
}

# reports_total prints every report the update service has judged, whatever the verdict. A device
# that connects sends update.inventory, so this rising is the platform's own word that the devices
# are on the broker, subscribed and heard.
reports_total() { um_metric update_reports_total ''; }

# wait_heard <base> <how-many> waits until the service has judged that many reports since <base>.
wait_heard() {
  local base="$1" want="$2" deadline=$((SECONDS + 120)) now=0
  while ((SECONDS < deadline)); do
    now="$(reports_total)"
    ((now - base >= want)) && { note "platform: $((now - base)) report(s) judged since the start"; return 0; }
    sleep 2
  done
  fail "the platform judged $((now - base)) report(s) from the starting devices, wanted $want: they are not connected"
}

step4_device() {
  say "STEP 4: device 1 on the kind network ($device_image), broker ssl://$node_ip:31883, origin $origin"
  start_container
  create_device 1
  local r0
  r0="$(reports_total)"
  start_device 1
  wait_payload 1 >/dev/null
  wait_heard "$r0" 1
  local id
  id="$(describe_running "$(payload_pid 1)")"
  [[ "$(jq -r .version <<<"$id")" == 1.0.0 ]] || fail "device 1 should start on 1.0.0, it says $id"
  pass "device 1 runs $id from $(running_exe "$(payload_pid 1)")"
}

# ---------------------------------------------------------------------------
# Step 5: the positive update
# ---------------------------------------------------------------------------

step5_positive() {
  say "STEP 5: assign 1.0.1 to device 1; it must be UPDATED on every source's evidence"
  local pid0 boot0 applied0 a j
  pid0="$(payload_pid 1)"; boot0="$(last_boot 1)"
  applied0="$(um_metric update_reports_total 'verdict="APPLIED"')"
  a="$(assign ota-dev-1 fw-101)"
  j="$(wait_terminal "$a" 300)"
  [[ "$(jq -r .attempt.state <<<"$j")" == UPDATED ]] || fail "1.0.1 did not take: $(jq -c .attempt <<<"$j")"
  # The device confirms and reports RUNNING from the new boot; give the launcher a moment to settle.
  sleep 3
  check_updated 1 "$a" 1.0.1 "$pid0" "$boot0" "$applied0" || fail "the UPDATED checker disagrees with the platform"
  pass "1.0.1 is running on device 1"
}

# ---------------------------------------------------------------------------
# Step 6: the negative controls
# ---------------------------------------------------------------------------

# still_on <n> <version> <slot>: the device runs what it ran before, from the slot it ran it from.
still_on() {
  local n="$1" want="$2" slot="$3" pid id
  pid="$(wait_payload "$n")"
  id="$(describe_running "$pid")"
  [[ "$(jq -r .version <<<"$id")" == "$want" ]] || fail "device $n should still run $want, it runs $id"
  [[ "$(running_exe "$pid")" == "/dev$n/slots/$slot/ota-device" ]] || fail "device $n runs $(running_exe "$pid"), not slot $slot"
  [[ "$(boot_state "$n" | jq -r '.confirmed + "/" + (.pending // "")')" == "$slot/" ]] ||
    fail "device $n boot state is $(boot_state "$n"), expected confirmed=$slot with nothing pending"
  note "device: still runs $id from slot $slot, nothing pending"
}

n2_assignment=""

n1_unknown_signer() {
  say "N1: 1.0.4 signed only by the Ed25519 signer the device does not trust"
  local before after a j
  before="$(slot_hash 1 a)"
  a="$(assign ota-dev-1 fw-104-ed)"
  j="$(wait_terminal "$a" 180)"
  expect_failed "$j" UNKNOWN_SIGNER
  still_on 1 1.0.1 b
  after="$(slot_hash 1 a)"
  [[ "$before" == "$after" ]] || fail "the inactive slot changed: $before -> $after"
  note "device: inactive slot a still sha256:$after"
  red "N1 -> FAILED UNKNOWN_SIGNER; device on 1.0.1, inactive slot untouched"
}

n2_rolled_back() {
  say "N2: 1.0.2-crash exits before it confirms; the launcher must roll back"
  local a j
  a="$(assign ota-dev-1 fw-102)"
  j="$(wait_terminal "$a" 300)"
  expect_failed "$j" ROLLED_BACK
  still_on 1 1.0.1 b
  # The crash image was started as a slot exactly once (plus its describe), and then never again.
  note "device: the crash image's own record of its runs: $(dx cat /dev1/slots/a/ran.marker | tr '\n' '|')"
  dx grep -q -- '--root /dev1' /dev1/slots/a/ran.marker || fail "the crash image was never booted: the rollback was not exercised"
  n2_assignment="$a"
  red "N2 -> FAILED ROLLED_BACK (never UPDATED); device on 1.0.1"
}

n6_checker_goes_red() {
  say "N6: the step-5 UPDATED checker, run against N2's attempt, must FAIL"
  local why
  if why="$(check_updated 1 "$n2_assignment" 1.0.2 "" "" 0 2>&1)"; then
    fail "the UPDATED checker PASSED an attempt that rolled back: it cannot go red"
  fi
  note "checker said: $why"
  red "N6 -> the checker refused N2's attempt"
}

n3_downgrade() {
  say "N3: 1.0.0, correctly signed, is a downgrade from 1.0.1"
  local a j
  a="$(assign ota-dev-1 fw-100)"
  j="$(wait_terminal "$a" 180)"
  expect_failed "$j" DOWNGRADE_REFUSED
  still_on 1 1.0.1 b
  red "N3 -> FAILED DOWNGRADE_REFUSED; device on 1.0.1"
}

n4_resume_then_tamper() {
  say "N4: kill device 1 at ~40% of the 1.0.4 download, flip one held byte, restart"
  local size target a held aborted0 aborted bytes0 bytes1 j served deadline
  size="$(wc -c <"$images/ota-device-1.0.4" | tr -d ' ')"
  target=$((size * 40 / 100))
  aborted0="$(um_metric update_downloads_total 'result="aborted"')"
  # The watcher runs INSIDE the container so it acts within milliseconds of the threshold.
  dxi sh -c "cat > /tmp/killat.sh" <<EOF
#!/bin/sh
part=/dev1/slots/a/ota-device.part
end=\$((\$(date +%s) + 120))
while [ "\$(date +%s)" -lt "\$end" ]; do
  s=\$(stat -c %s "\$part" 2>/dev/null || echo 0)
  if [ "\$s" -ge $target ]; then
    kill -9 \$(cat /dev1/boot.pid)
    for p in /proc/[0-9]*; do case "\$(readlink \$p/exe 2>/dev/null)" in /dev1/slots/*/ota-device) kill -9 \${p#/proc/};; esac; done
    sleep 1
    stat -c %s "\$part"
    exit 0
  fi
done
echo timeout
exit 1
EOF
  dx test ! -e /dev1/slots/a/ota-device.part || fail "a .part file is already in slot a before N4 starts"
  dx sh -c 'echo "--- N4 kill watcher armed" >> /dev1/device.log'
  # The watcher is armed BEFORE the assignment: the whole download takes about a second.
  local wpid
  dx sh /tmp/killat.sh >"$rig_tmp/held" &
  wpid=$!
  sleep 2
  a="$(assign ota-dev-1 fw-104)"
  wait "$wpid" || fail "the download never reached 40% of $size bytes"
  held="$(tr -d '\r\n ' <"$rig_tmp/held")"
  [[ "$held" =~ ^[0-9]+$ && "$held" -gt 0 && "$held" -lt "$size" ]] ||
    fail "the kill did not land mid-download (held '$held' of $size); raise DC_OTA_RIG_PAD_MB"
  note "device: killed holding $held of $size bytes ($((held * 100 / size))%)"
  # Wait for the server to count the cut stream, so the byte counter below starts after it.
  deadline=$((SECONDS + 90))
  until aborted="$(um_metric update_downloads_total 'result="aborted"')"; ((aborted > aborted0)); do
    ((SECONDS < deadline)) || fail "the server never counted the interrupted download as aborted"
    sleep 2
  done
  bytes0="$(um_metric update_download_bytes_total '')"
  # Flip one byte well inside what the device holds.
  dxi sh -c "cat > /tmp/flip.sh" <<'EOF'
#!/bin/sh
# flip.sh FILE OFFSET: add one to the byte at OFFSET, in place. Prints "old new".
b=$(dd if="$1" bs=1 skip="$2" count=1 2>/dev/null | od -An -tu1 | tr -d ' ')
n=$(( (b + 1) % 256 ))
printf "$(printf '\\%03o' "$n")" | dd of="$1" bs=1 seek="$2" count=1 conv=notrunc 2>/dev/null
echo "$b $(dd if="$1" bs=1 skip="$2" count=1 2>/dev/null | od -An -tu1 | tr -d ' ')"
EOF
  local flipped
  flipped="$(dx sh /tmp/flip.sh /dev1/slots/a/ota-device.part 4096)"
  read -r fb fa <<<"$flipped"
  [[ -n "$fb" && -n "$fa" && "$fb" != "$fa" ]] || fail "the byte flip did not change the byte at 4096 ($flipped)"
  [[ "$(dx stat -c %s /dev1/slots/a/ota-device.part)" == "$held" ]] || fail "the byte flip changed the size of the held file"
  note "device: byte 4096 of the held file flipped $fb -> $fa"
  start_device 1
  j="$(wait_terminal "$a" 300)"
  expect_failed "$j" VERIFICATION_FAILED
  bytes1="$(um_metric update_download_bytes_total '')"
  served=$((bytes1 - bytes0))
  note "server: bytes streamed after the restart = $served; size - held = $((size - held))"
  [[ "$served" -eq $((size - held)) ]] ||
    fail "the restart was not a range resume from $held: the server streamed $served bytes, not $((size - held))"
  still_on 1 1.0.1 b
  red "N4 -> FAILED VERIFICATION_FAILED after a Range resume from byte $held; device on 1.0.1"
}

n5_liar() {
  say "N5: 1.0.3-liar says it is another product"
  local a j runs
  dx rm -f /dev1/slots/a/ran.marker
  a="$(assign ota-dev-1 fw-103)"
  j="$(wait_terminal "$a" 300)"
  expect_failed "$j" IMAGE_MISMATCH
  still_on 1 1.0.1 b
  runs="$(dx sh -c 'cat /dev1/slots/a/ran.marker 2>/dev/null' | tr '\n' '|')"
  [[ "$runs" =~ ^(describe\|)+$ ]] || fail "the liar image ran as something other than describe: '$runs'"
  note "device: the liar's own record of its runs: $runs"
  red "N5 -> FAILED IMAGE_MISMATCH; the liar only ever ran as describe; device on 1.0.1"
}

helm_release="dc-$instance"
helm_ns=""

wait_downloads_configured() {
  local want="$1" deadline=$((SECONDS + 300)) got=""
  k -n "$namespace" rollout status deploy/update-management --timeout=300s >/dev/null ||
    fail "update-management did not roll out"
  while ((SECONDS < deadline)); do
    got="$(updq '{updateManagementInfo{downloadsConfigured}}' | jq -r '.updateManagementInfo.downloadsConfigured')" || got=""
    [[ "$got" == "$want" ]] && return 0
    sleep 3
  done
  fail "updateManagementInfo.downloadsConfigured stayed '$got', wanted $want"
}

n7_downloads_disabled() {
  say "N7: S0 reverted for the rig only: downloadBaseUrl set to \"\" by a helm upgrade"
  local rev a nc0 nc j st deadline seen=""
  need helm
  helm_ns="$(helm --kubeconfig "$kubeconfig" --kube-context "$kube_context" list -A -o json |
    jq -r --arg r "$helm_release" '.[] | select(.name == $r) | .namespace')"
  [[ -n "$helm_ns" ]] || fail "no helm release $helm_release on $cluster"
  rev="$(helm --kubeconfig "$kubeconfig" --kube-context "$kube_context" -n "$helm_ns" history "$helm_release" -o json | jq -r '.[-1].revision')"
  [[ -n "$rev" && "$rev" != null ]] || fail "cannot read the helm history of $helm_release"
  helm --kubeconfig "$kubeconfig" --kube-context "$kube_context" -n "$helm_ns" upgrade "$helm_release" \
    "$(winpath "$repo_root/deploy/helm/devicechain")" --reuse-values \
    --set-json 'functionalAreas.update-management.config.updates.downloadBaseUrl=""' >/dev/null ||
    fail "the rig-only helm upgrade failed"
  wait_downloads_configured false
  note "platform: updateManagementInfo.downloadsConfigured=false"
  nc0="$(um_metric update_download_grants_total 'result="not_configured"')"
  a="$(assign ota-dev-1 fw-104)"
  deadline=$((SECONDS + 60))
  while ((SECONDS < deadline)); do
    j="$(assignment_json "$a")"; st="$(jq -r .attempt.state <<<"$j")"
    seen="$seen $st"
    [[ "$st" == QUEUED || "$st" == INITIATED ]] || fail "with downloads disabled the attempt reached $st"
    sleep 3
  done
  [[ "$st" == INITIATED ]] || fail "the attempt is $st after 60 s, expected INITIATED (the device asked and was refused)"
  nc="$(um_metric update_download_grants_total 'result="not_configured"')"
  ((nc > nc0)) || fail "update_download_grants_total{result=\"not_configured\"} did not rise ($nc0 -> $nc)"
  note "platform: states sampled over 60 s:$(tr ' ' '\n' <<<"$seen" | uniq | paste -sd' ')"
  note "metrics: update_download_grants_total{result=\"not_configured\"} $nc0 -> $nc"
  still_on 1 1.0.1 b
  red "N7 -> no grant, attempt held at INITIATED, not_configured counted; device on 1.0.1"

  say "N7: restoring: cancel the stuck attempt, roll the release back to revision $rev"
  updq 'mutation($t:String!){cancelUpdate(assignmentToken:$t){token}}' "$(jq -n --arg t "$a" '{t:$t}')" >/dev/null ||
    fail "cancelUpdate"
  j="$(wait_terminal "$a" 120)"
  [[ "$(jq -r .attempt.state <<<"$j")" == CANCELLED ]] || fail "the cancelled attempt ended $(jq -c .attempt <<<"$j")"
  helm --kubeconfig "$kubeconfig" --kube-context "$kube_context" -n "$helm_ns" rollback "$helm_release" "$rev" >/dev/null ||
    fail "helm rollback to $rev failed"
  wait_downloads_configured true
  step1_download_base
  pass "downloads restored: downloadsConfigured=true and the derived base is back"
}

# ---------------------------------------------------------------------------
# Step 7: the fleet
# ---------------------------------------------------------------------------

step7_fleet() {
  say "STEP 7: three more devices, a static group of four, a rollout of 1.0.4 with maxInFlight=1"
  local n reqs r j st max=0 inflight deadline samples=0 prog r0
  for n in 2 3 4; do create_device "$n"; done
  r0="$(reports_total)"
  for n in 2 3 4; do start_device "$n"; done
  for n in 2 3 4; do wait_payload "$n" >/dev/null; done
  wait_heard "$r0" 3
  dmq 'mutation($r:EntityGroupCreateRequest){createEntityGroup(request:$r){token}}' \
    "$(jq -n --arg t "$group" '{r:{token:$t,name:"OTA rig fleet",memberType:"device",membershipMode:"static"}}')" >/dev/null ||
    fail "createEntityGroup"
  reqs="$(jq -n --arg g "$group" '[range(1;5) | {token:"\($g)-m\(.)",sourceType:"group",source:$g,targetType:"device",target:"ota-dev-\(.)",relationshipType:"member"}]')"
  dmq 'mutation($r:[EntityRelationshipCreateRequest!]!){createEntityRelationships(requests:$r){token}}' \
    "$(jq -n --argjson r "$reqs" '{r:$r}')" >/dev/null || fail "adding the four devices to the group"
  r="fleet-104"
  updq 'mutation($r:UpdateRolloutCreateRequest!){createUpdateRollout(request:$r){token state}}' \
    "$(jq -n --arg t "$r" --arg g "$group" '{r:{token:$t,name:"OTA rig 1.0.4",artifactToken:"fw-104",groupToken:$g,maxInFlight:1}}')" >/dev/null ||
    fail "createUpdateRollout"
  updq 'mutation($t:String!){startUpdateRollout(token:$t){state targetCount}}' "$(jq -n --arg t "$r" '{t:$t}')" >"$rig_tmp/start.json" ||
    fail "startUpdateRollout"
  [[ "$(jq -r .startUpdateRollout.targetCount "$rig_tmp/start.json")" == 4 ]] || fail "the rollout froze $(cat "$rig_tmp/start.json"), not 4 devices"
  deadline=$((SECONDS + 900))
  while ((SECONDS < deadline)); do
    # One read per second: every device's current firmware assignment that is this rollout's
    # artifact and not finished. UNKNOWN keeps its place under the cap, so it counts.
    j="$(updq 'query{updateAssignments(criteria:{pageNumber:1,pageSize:50,component:"firmware",currentOnly:true}){results{deviceToken artifact{token} attempt{state}}}}')" ||
      fail "updateAssignments"
    inflight="$(jq '[.updateAssignments.results[] | select(.artifact.token == "fw-104")
      | select(.attempt.state | test("^(UPDATED|FAILED|TIMED_OUT|CANCELLED)$") | not)] | length' <<<"$j")"
    samples=$((samples + 1))
    ((inflight > max)) && max="$inflight"
    ((inflight <= 1)) || fail "$inflight attempts unfinished at once under maxInFlight=1: $j"
    prog="$(updq 'query($t:String!){updateRollout(token:$t){state progress{total pending inFlight succeeded failed unknown cancelled skipped}}}' \
      "$(jq -n --arg t "$r" '{t:$t}')" | jq -c .updateRollout)" || fail "updateRollout"
    st="$(jq -r .state <<<"$prog")"
    [[ "$st" == RUNNING ]] || break
    sleep 1
  done
  note "rollout: $prog after $samples one-second samples; most unfinished at once: $max"
  [[ "$st" == COMPLETED ]] || fail "the rollout ended $st, not COMPLETED: $prog"
  [[ "$(jq -r .progress.succeeded <<<"$prog")" == 4 ]] || fail "the rollout did not succeed on 4/4: $prog"
  for n in 1 2 3 4; do
    local id
    id="$(describe_running "$(wait_payload "$n")")"
    [[ "$(jq -r .version <<<"$id")" == 1.0.4 ]] || fail "device $n runs $id after the rollout"
    note "device $n: $id"
  done
  pass "rollout COMPLETED, 4/4 SUCCEEDED, never more than $max unfinished at once, every device describes itself as 1.0.4"
}

cmd_run() {
  need kubectl; need docker; need jq; need curl; need openssl; need go
  cluster_exists || fail "kind cluster $cluster does not exist; run 'hack/ota-rig.sh up'"
  k get ns "$namespace" >/dev/null 2>&1 || fail "instance $instance is not on $cluster; run 'hack/ota-rig.sh up'"
  # A fresh tenant per run: artifact names and versions are unique per tenant, so a re-run on the
  # same instance starts clean rather than colliding with the last one.
  run_id="$(date -u +%H%M%S)"
  tenant="${DC_OTA_RIG_TENANT:-ota$run_id}"
  member_email="ota-rig@$tenant.test"
  say "run $run_id: tenant $tenant on instance $instance (cluster $cluster)"
  discover
  step1_download_base
  step2_provision
  step3_artifacts
  step4_device
  step5_positive
  n1_unknown_signer
  n2_rolled_back
  n6_checker_goes_red
  n3_downgrade
  n4_resume_then_tamper
  n5_liar
  n7_downloads_disabled
  step7_fleet
  say "OTA RIG GREEN: a separately built device took 1.0.1 over the instance's broker and ingress,
proved it on six independent sources, and every planted defect (N1-N7) turned the platform's
verdict red while the device kept running what it ran before; the four-device rollout held its
cap and completed 4/4."
}

case "${1:-}" in
  up) cmd_up ;;
  run) cmd_run ;;
  all) cmd_up; cmd_run ;;
  down) cmd_down ;;
  *) fail "usage: hack/ota-rig.sh up | run | all | down" ;;
esac
