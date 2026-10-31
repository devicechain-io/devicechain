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
#                           port), install, and bootstrap an instance (default profile; DC_OTA_RIG_BOOTSTRAP_MODE) from
#                           images built from THIS tree under a unique tag; assert the download base
#                           was derived from the ingress host
#   hack/ota-rig.sh run     provision, build the device images, start the device, then the
#                           positive update, N1-N7, the four-device rollout and the failure
#                           matrix M1-M7
#   hack/ota-rig.sh matrix  the failure matrix alone, on a fresh tenant with four devices on
#                           1.0.0 (DC_OTA_RIG_CASES="m3 m7" narrows it while a case is worked on)
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
#   5  POSITIVE: 1.0.1 is assigned and reaches UPDATED. The checker (check_updated) then reads
#      THREE DISCRIMINATING SOURCES, each of which can tell 1.0.1 from anything else: the
#      platform (the current assignment is UPDATED for 1.0.1), the device's confirmed slot (it
#      hashes to the artifact digest, and it is the file the payload process executes), and the
#      running binary itself (its `describe` says 1.0.1). It also reads THREE LIVENESS SIGNALS,
#      which say something happened but not what: the payload PID changed, the bootId changed,
#      and update_reports_total{verdict="APPLIED"} rose (it counts a FAILED report as readily as
#      a RUNNING one). Every sub-check is evaluated and each failure is named by a tag (state,
#      pid, boot, slot, exe, describe, applied), so a control can assert WHICH ones went red
#   6  NEGATIVE CONTROLS, each asserting the platform's verdict and what the device still runs:
#        N1  1.0.4 signed only by the Ed25519 signer (registered, not trusted by the device)
#            -> FAILED UNKNOWN_SIGNER; the inactive slot is untouched
#        N2  1.0.2-crash -> FAILED ROLLED_BACK, never UPDATED; the crash image booted exactly once
#        N3  1.0.0, signed -> FAILED DOWNGRADE_REFUSED
#        N4  1.0.4: the device is killed at ~40% of the download, one byte of what it holds is
#            flipped, it restarts -> FAILED VERIFICATION_FAILED, after a RANGE resume (the bytes
#            served after the restart equal exactly size - held), and the digest the device
#            reports is the one the rig computes for "flipped prefix + true tail"
#        N5  1.0.3-liar -> FAILED IMAGE_MISMATCH; the liar was only ever run as `describe`
#        N6  the step-5 UPDATED checker run against N2's attempt MUST FAIL on exactly
#            {state, slot, describe}; N6b runs it against the step-5 attempt with the CURRENT
#            PID and bootId as "before" and it must fail on pid and boot (and not on slot, exe or
#            describe). A transport error is never counted as red
#        N7  downloadBaseUrl set to "" by a rig-only helm upgrade -> the step-1 check goes red on
#            that value, update_download_grants_total{result="not_configured"} rises and the
#            attempt never leaves INITIATED; the release is then rolled back (also on any failure
#            while N7 holds it) and downloads work again
#   7  FLEET: three more devices, a static group of four, a rollout of 1.0.4 (ECDSA-signed) with
#      maxInFlight=1. While it runs, every sample (about one a second) must see at most one
#      unfinished attempt AND the platform's own progress.inFlight <= 1, and at least one sample
#      must have caught an attempt in flight. After COMPLETED (4/4), a deterministic check: each
#      device's dispatchedAt is no earlier than the previous device's last report, compared as
#      RFC 3339 instants (a fabricated overlapping timeline must fail it, offline, first). Then
#      the device-state checker is shown red twice against the new state
#   8  the FAILURE MATRIX, M1-M7, described above `step8_matrix`: a signature that does not verify,
#      bytes that do not hash, an offline period mid-download and one before the assignment,
#      cancellation (in flight and queued), a deadline, and a rollout that pauses at its
#      failure threshold with one device unable to take the image
#
# OFFLINE CONTROLS run first, with no cluster: the rollout-overlap check must reject a fabricated
# overlapping timeline (including one a naive string compare of timestamps would pass) and accept
# a fabricated sequential one.
#
# EVIDENCE. `run` prints the git HEAD, the number of dirty paths, `git hash-object` of this script
# and the update-management image, and refuses an image whose tag is not the one `up` recorded.
# Every invocation also writes .ota-rig/run-<UTC timestamp>.log with its real exit status last.
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
#
# PRUNING THE RIG'S IMAGES, by hand and only this rig's tags. Every tag `up` pushes starts with
# `ota-` (the tag is also kept in .ota-rig/image-tag until `down`). The registry is SHARED with
# other lanes, so never delete a tag you did not push and never wipe the registry's volume:
#
#   reg=http://localhost:5000; tag=ota-<sha8>-<timestamp>
#   for repo in $(curl -s $reg/v2/_catalog | jq -r '.repositories[]'); do
#     d=$(curl -sI -H 'Accept: application/vnd.oci.image.index.v1+json' \
#           -H 'Accept: application/vnd.docker.distribution.manifest.v2+json' \
#           "$reg/v2/$repo/manifests/$tag" | tr -d '\r' | awk 'tolower($1)=="docker-content-digest:"{print $2}')
#     [ -n "$d" ] && curl -s -X DELETE "$reg/v2/$repo/manifests/$d"
#   done
#
# 🔴 A manifest is deleted BY DIGEST, and that removes every tag pointing at the same digest. ko
# builds reproducibly, so another lane that built the same source may hold a tag on that very
# digest: before each DELETE, list the repo's tags (/v2/<repo>/tags/list), resolve each one, and
# skip the digest if any tag but yours names it.
# A DELETE needs the registry to allow deletes (REGISTRY_STORAGE_DELETE_ENABLED); if it answers
# 405 it does not, and nothing was removed. Deleting a manifest frees no disk until the registry's
# garbage collector runs, which is a registry-wide operation: run it only when no lane is pushing.

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
device_image="${DC_OTA_RIG_DEVICE_IMAGE:-alpine:3.22@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f}"
min_free_gb="${DC_OTA_RIG_MIN_FREE_GB:-100}"
# How the instance gets update-management. profile-default (the default) bootstraps the DEFAULT
# profile with no --enable-area, which is what the v1 release gate proves: an operator who installs
# the product as shipped can update a device. enable-area passes --enable-area update-management as
# well, which is the same instance on a tree where the default profile did not yet carry the area.
bootstrap_mode="${DC_OTA_RIG_BOOTSTRAP_MODE:-profile-default}"
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

# 🔴 NEVER the shared developer cluster. Other lanes build on `devicechain` (and its registry);
# `down` deletes the cluster it is pointed at, so the name is refused before anything runs.
[[ "$cluster" != devicechain ]] || { echo "refusing: DC_OTA_RIG_CLUSTER=devicechain is the shared developer cluster" >&2; exit 2; }

# Every invocation keeps its own log, ending in its real exit status (written by the EXIT trap,
# so a `fail` deep inside a step is recorded as the failure it is).
run_log="$state_dir/run-$(date -u +%Y%m%dT%H%M%SZ).log"
exec > >(tee -a "$run_log") 2>&1

rig_tmp="$(mktemp -d "${TMPDIR:-/tmp}/dc-ota-rig.XXXXXX")"
n4_watcher=""
n7_restore_rev=""
cleanup() {
  local rc=$?
  # N7 holds the release with downloads disabled; a failure while it does must not leave the
  # instance that way.
  if [[ -n "$n7_restore_rev" ]]; then
    echo "cleanup: rolling $helm_release back to revision $n7_restore_rev (N7 did not finish)"
    helm --kubeconfig "$kubeconfig" --kube-context "$kube_context" -n "$helm_ns" rollback "$helm_release" "$n7_restore_rev" >/dev/null 2>&1 ||
      echo "cleanup: the rollback FAILED; restore it by hand: helm -n $helm_ns rollback $helm_release $n7_restore_rev"
  fi
  # The failure matrix takes the devices' network down; never leave the container that way.
  if [[ "${mx_link_down:-0}" == 1 ]]; then
    MSYS_NO_PATHCONV=1 docker exec "$device_container" sh -c "ip link set eth0 up; ip route replace default via ${mx_gw:-0.0.0.0} dev eth0" >/dev/null 2>&1 || true
  fi
  if [[ -n "${mx_watcher:-}" ]]; then kill "$mx_watcher" 2>/dev/null || true; fi
  if [[ -n "$n4_watcher" ]]; then
    kill "$n4_watcher" 2>/dev/null || true
    MSYS_NO_PATHCONV=1 docker exec "$device_container" pkill -f /tmp/killat.sh 2>/dev/null || true
  fi
  # The private keys live only here. Removed however the run ends, including a failed check.
  rm -rf "$rig_tmp"
  printf 'ota-rig: %s exited with status %d\n' "${rig_cmd:-?}" "$rc" >>"$run_log"
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
  local area_args=()
  case "$bootstrap_mode" in
    profile-default) say "bootstrapping $instance on the default profile (update-management comes with it)" ;;
    enable-area)
      say "bootstrapping $instance with update-management enabled explicitly"
      area_args=(--enable-area update-management)
      ;;
    *) fail "DC_OTA_RIG_BOOTSTRAP_MODE must be profile-default or enable-area, got '$bootstrap_mode'" ;;
  esac
  # --no-escrow: this instance exists to be torn down.
  "$dcctl" bootstrap local "$instance" --yes --no-escrow --cluster "$cluster"     --host "$host" --build --version "$tag" ${area_args[@]+"${area_args[@]}"} 2>&1 | redact
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
  local uid="" rec="${HOME:?}/.devicechain/instances/$instance/instance.json" recctx
  if cluster_exists; then
    # Only a cluster this rig's own kubeconfig reaches is this rig's to delete: a kind cluster of
    # the same name made by someone else is not in that file.
    uid="$(k get ns kube-system -o jsonpath='{.metadata.uid}' 2>/dev/null)" ||
      fail "kind cluster $cluster exists but $kubeconfig does not reach it: it is not this rig's; refusing to delete it"
    [[ -n "$uid" ]] || fail "cannot read the kube-system UID of $cluster through $kubeconfig; refusing to delete it"
    if [[ -f "$rec" ]]; then
      # dcctl destroys by the instance's RECORD; only a record naming this rig's context is ours.
      recctx="$(jq -r '.kubeContext // empty' "$rec")"
      [[ "$recctx" == "$kube_context" ]] ||
        fail "$rec names context '$recctx', not $kube_context: instance $instance is not this rig's; refusing to destroy it"
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

# um_pods lists the running update-management pods that are not on their way out: a pod being
# deleted during a rollout still answers, with counters the new pods do not carry.
um_pods() {
  k -n "$namespace" get pods -l "$um_selector" --field-selector=status.phase=Running -o json |
    jq -r '.items[] | select(.metadata.deletionTimestamp == null) | .metadata.name'
}

# um_metric <metric suffix> <label pair | * | ""> prints the sum of that series across every live
# update-management pod, read through the API server's pod proxy. <labels> is ONE label pair,
# matched exactly as a pair and anywhere in the series' label set: 'result="aborted"' matches
# {result="aborted"} and {kind="x",result="aborted"} but not {result="aborted2"}. "" matches only
# the unlabelled series, and "*" sums every series of the name. A pod whose
# metrics cannot be read FAILS the rig: reading it as zero would let a control "see" a number it
# never measured.
um_metric() {
  local name="$1" label="$2" pod body total=0 n pods
  pods="$(um_pods)"
  [[ -n "$pods" ]] || fail "no live update-management pod to read metrics from"
  for pod in $pods; do
    body="$(k get --raw "/api/v1/namespaces/$namespace/pods/$pod:$um_port/proxy/metrics")" ||
      fail "could not read the metrics of $pod"
    # The series name ends in _<name>; its labels are what sits between the braces.
    n="$(awk -v m="_${name}" -v l="$label" '
      /^#/ { next }
      { i = index($1, "{"); nm = (i ? substr($1, 1, i - 1) : $1)
        if (length(nm) < length(m) || substr(nm, length(nm) - length(m) + 1) != m) next; found = 1
        ls = (i ? substr($1, i + 1, length($1) - i - 1) : "")
        # <labels> is ONE label pair (verdict="APPLIED"), matched among the labels of a series wherever
        # it sits: a series also carries labels the caller does not name (kind="progress"), and
        # an exact comparison of the whole set read 0 the moment a second label was added.
        # "" matches only an unlabelled series; "*" matches every series.
        hit = (l == "*") || (l == "" && ls == "")
        if (!hit && l != "") { c = split(ls, parts, ","); for (j = 1; j <= c; j++) if (parts[j] == l) hit = 1 }
        if (hit) s += $NF }
      END { if (!found) exit 3; printf "%d", s }' <<<"$body")" ||
      fail "$pod exposes no *_${name} series; the metric was renamed"
    total=$((total + n))
  done
  printf '%s\n' "$total"
}

assignment_json() {
  updq 'query($t:String!){updateAssignment(token:$t){token current deviceToken artifact{token version}
    attempt{state targetVersion targetDigest cancelRequested progress{bytes total} failure{stage code detail platform}}}}' \
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
  # The first match ends the loop INSIDE the container: a `| head -1` here would close the pipe
  # early, and under pipefail the writer's SIGPIPE fails the whole read.
  dx sh -c 'for p in /proc/[0-9]*; do e="$(readlink "$p/exe" 2>/dev/null)" || continue
    case "$e" in /dev'"$1"'/slots/*/ota-device) echo "${p#/proc/}"; break;; esac; done'
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

# CHECKERS. A checker RETURNS instead of failing, so a control can run it against a state that
# must not pass and require it to go red. Its verdict is its last line:
#
#   GREEN               every sub-check agreed (status 0)
#   RED: <tag> <tag>    these sub-checks disagreed, each explained on a line of its own (status 1)
#   TRANSPORT: <why>    it could not read what it needed (status 2)
#
# A checker evaluates EVERY sub-check before it answers, so the set of tags says which sources
# disagreed, not merely the first. TRANSPORT, a crash, or a `fail` inside a checker is never red:
# expect_red refuses anything without a RED line.

# check_updated <n> <assignment> <version> <pid-before> <boot-before> <applied-before>
#
# Three DISCRIMINATING sources, each able to tell <version> from anything else:
#   state     the platform: the assignment is the device's current one, UPDATED, for <version>
#   slot      the confirmed slot hashes to the artifact digest
#   exe       ...and it is the file the payload process executes
#   describe  the running binary (its /proc exe, not a slot file) says <product> <version>
# Three LIVENESS signals, which say that something happened but not what:
#   pid       the payload PID is not <pid-before>
#   boot      the latest bootId is not <boot-before>
#   applied   update_reports_total{verdict="APPLIED"} rose past <applied-before>; a FAILED
#             report is applied as readily as RUNNING, so this alone proves nothing
check_updated() {
  local n="$1" a="$2" want="$3" pid0="$4" boot0="$5" applied0="$6" j cur st tv pid boot bs slot digest h exe id applied
  local -a red=()
  j="$(assignment_json "$a")" || { echo "TRANSPORT: assignment $a could not be read"; return 2; }
  [[ -n "$j" && "$j" != null ]] || { echo "TRANSPORT: there is no assignment $a"; return 2; }
  cur="$(jq -r '.current' <<<"$j")"; st="$(jq -r '.attempt.state' <<<"$j")"; tv="$(jq -r '.attempt.targetVersion' <<<"$j")"
  [[ "$cur" == true && "$st" == UPDATED && "$tv" == "$want" ]] ||
    { red+=(state); echo "  state: current=$cur state=$st target=$tv, wanted current=true UPDATED $want"; }
  pid="$(payload_pid "$n")" || { echo "TRANSPORT: cannot list device $n's processes"; return 2; }
  [[ -n "$pid" ]] || { echo "TRANSPORT: device $n has no running payload to read"; return 2; }
  [[ "$pid" != "$pid0" ]] || { red+=(pid); echo "  pid: the payload is still PID $pid"; }
  boot="$(last_boot "$n")" || { echo "TRANSPORT: cannot read device $n's log"; return 2; }
  [[ -n "$boot" && "$boot" != "$boot0" ]] || { red+=(boot); echo "  boot: the latest boot is still $boot0"; }
  bs="$(boot_state "$n")" || { echo "TRANSPORT: cannot read device $n's boot.json"; return 2; }
  slot="$(jq -r '.confirmed' <<<"$bs")"
  digest="$(jq -r '.attempt.targetDigest' <<<"$j")"
  h="$(slot_hash "$n" "$slot")" || { echo "TRANSPORT: cannot hash device $n's slot $slot"; return 2; }
  [[ "sha256:$h" == "$digest" ]] || { red+=(slot); echo "  slot: confirmed slot $slot is sha256:$h, the artifact is $digest"; }
  exe="$(running_exe "$pid")" || { echo "TRANSPORT: cannot read /proc/$pid/exe"; return 2; }
  [[ "$exe" == "/dev$n/slots/$slot/ota-device" ]] || { red+=(exe); echo "  exe: the payload executes $exe, not confirmed slot $slot"; }
  id="$(dx "$exe" describe)" || { echo "TRANSPORT: $exe describe did not answer"; return 2; }
  [[ "$(jq -r '.version' <<<"$id")" == "$want" && "$(jq -r '.product' <<<"$id")" == "$product" ]] ||
    { red+=(describe); echo "  describe: the running binary says $id, wanted $product $want"; }
  applied="$(um_metric update_reports_total 'verdict="APPLIED"')"
  ((applied > applied0)) || { red+=(applied); echo "  applied: update_reports_total{verdict=\"APPLIED\"} $applied0 -> $applied"; }
  if ((${#red[@]})); then echo "RED: ${red[*]}"; return 1; fi
  note "platform (state): assignment $a current, UPDATED, target $want"
  note "device (slot, exe): confirmed slot $slot = sha256:$h = the artifact digest = the file PID $pid executes"
  note "device (describe): the running binary says $id"
  note "liveness: PID $pid0 -> $pid; bootId $boot0 -> $boot; APPLIED reports $applied0 -> $applied"
  echo GREEN
}

# expect_red <label> <required tags> <other tags: "*" = none allowed, else a list forbidden>
#            <reason text, or ""> <checker...>
# runs a checker that MUST go red, and red for the right reason: every required tag present, no
# tag it may not carry, and (when given) the reason text in its explanation.
expect_red() {
  local label="$1" need="$2" forbid="$3" reason="$4" out rc=0 tags t
  shift 4
  out="$("$@" 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    | /'
  ((rc != 0)) || fail "$label: the checker PASSED a state it must refuse: it cannot go red"
  tags="$(sed -n 's/^RED: //p' <<<"$out" | tail -n 1)"
  [[ -n "$tags" ]] || fail "$label: the checker stopped without a verdict (status $rc); a transport error is not red"
  for t in $need; do
    [[ " $tags " == *" $t "* ]] || fail "$label: red for the wrong reason: [$tags] lacks '$t'"
  done
  if [[ "$forbid" == "*" ]]; then
    for t in $tags; do [[ " $need " == *" $t "* ]] || fail "$label: red for the wrong reason: '$t' in [$tags], wanted exactly [$need]"; done
  else
    for t in $forbid; do [[ " $tags " != *" $t "* ]] || fail "$label: red for the wrong reason: '$t' in [$tags]"; done
  fi
  [[ -z "$reason" || "$out" == *"$reason"* ]] || fail "$label: red, but the explanation does not say \"$reason\""
  note "$label: red on [$tags]${reason:+, saying \"$reason\"}"
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

# check_download_base: the update-management config the chart rendered carries the base derived
# from the instance host. A checker (see CHECKERS): its one tag is `base`.
check_download_base() {
  local cfgs cfg base
  cfgs="$(k -n "$namespace" get configmap -o json)" || { echo "TRANSPORT: cannot list ConfigMaps in $namespace"; return 2; }
  cfg="$(jq -r '[.items[] | .data["update-management"] // empty][0] // empty' <<<"$cfgs")"
  [[ -n "$cfg" ]] || { echo "TRANSPORT: no ConfigMap in $namespace carries the update-management config"; return 2; }
  base="$(jq -r '.updates.downloadBaseUrl // "<absent>"' <<<"$cfg")"
  if [[ "$base" != "$origin/api/update-management" ]]; then
    echo "  base: the rendered downloadBaseUrl is '$base', expected '$origin/api/update-management'"
    echo "RED: base"
    return 1
  fi
  echo "rendered updates.downloadBaseUrl = $base"
  echo GREEN
}

step1_download_base() {
  say "STEP 1: the download base is derived from the ingress host"
  local out
  out="$(check_download_base)" || fail "$out"
  pass "${out%$'\n'GREEN}"
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
    --cap-add NET_ADMIN --label devicechain.io/ota-rig="$cluster" "$device_image" sleep infinity >/dev/null
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
reports_total() { um_metric update_reports_total '*'; }

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

step5_assignment=""

step5_positive() {
  say "STEP 5: assign 1.0.1 to device 1; three discriminating sources and three liveness signals must agree"
  local pid0 boot0 applied0 a j
  pid0="$(payload_pid 1)"; boot0="$(last_boot 1)"
  applied0="$(um_metric update_reports_total 'verdict="APPLIED"')"
  a="$(assign ota-dev-1 fw-101)"
  j="$(wait_terminal "$a" 300)"
  [[ "$(jq -r .attempt.state <<<"$j")" == UPDATED ]] || fail "1.0.1 did not take: $(jq -c .attempt <<<"$j")"
  # The device confirms and reports RUNNING from the new boot; give the launcher a moment to settle.
  sleep 3
  check_updated 1 "$a" 1.0.1 "$pid0" "$boot0" "$applied0" >"$rig_tmp/check5" ||
    fail "the UPDATED checker disagrees with the platform: $(cat "$rig_tmp/check5")"
  grep -v '^GREEN$' "$rig_tmp/check5"
  step5_assignment="$a"
  pass "1.0.1 is running on device 1"
}

# ---------------------------------------------------------------------------
# Step 6: the negative controls
# ---------------------------------------------------------------------------

# check_still_on <n> <version> <slot>: the device runs what it ran before, from the slot it ran it
# from, with nothing pending. A checker (see CHECKERS); its tags are version, exe and boot.
check_still_on() {
  local n="$1" want="$2" slot="$3" pid exe id bs
  local -a red=()
  pid="$(payload_pid "$n")" || { echo "TRANSPORT: cannot list device $n's processes"; return 2; }
  [[ -n "$pid" ]] || { echo "TRANSPORT: device $n has no running payload to read"; return 2; }
  exe="$(running_exe "$pid")" || { echo "TRANSPORT: cannot read /proc/$pid/exe"; return 2; }
  id="$(dx "$exe" describe)" || { echo "TRANSPORT: $exe describe did not answer"; return 2; }
  bs="$(boot_state "$n")" || { echo "TRANSPORT: cannot read device $n's boot.json"; return 2; }
  [[ "$(jq -r .version <<<"$id")" == "$want" ]] || { red+=(version); echo "  version: device $n runs $id, expected $want"; }
  [[ "$exe" == "/dev$n/slots/$slot/ota-device" ]] || { red+=(exe); echo "  exe: device $n executes $exe, not slot $slot"; }
  [[ "$(jq -r '.confirmed + "/" + (.pending // "")' <<<"$bs")" == "$slot/" ]] ||
    { red+=(boot); echo "  boot: device $n boot.json is $(jq -c . <<<"$bs"), expected confirmed=$slot with nothing pending"; }
  if ((${#red[@]})); then echo "RED: ${red[*]}"; return 1; fi
  echo "device: still runs $id from slot $slot, nothing pending"
  echo GREEN
}

still_on() {
  local out
  wait_payload "$1" >/dev/null
  out="$(check_still_on "$@")" || fail "device $1 is not where it should be: $out"
  note "${out%$'\n'GREEN}"
}

n2_assignment=""
n2_pid0=""
n2_boot0=""
n2_applied0=""

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
  local a j runs boots
  # What N6 will compare against: the state of the device BEFORE this update.
  n2_pid0="$(payload_pid 1)"; n2_boot0="$(last_boot 1)"
  n2_applied0="$(um_metric update_reports_total 'verdict="APPLIED"')"
  a="$(assign ota-dev-1 fw-102)"
  j="$(wait_terminal "$a" 300)"
  expect_failed "$j" ROLLED_BACK
  still_on 1 1.0.1 b
  # The crash image's own record: one `describe` (the preflight) and EXACTLY ONE boot. Zero means
  # the rollback was never exercised; two would mean the launcher gave a failed slot a second try.
  runs="$(dx cat /dev1/slots/a/ran.marker)"
  note "device: the crash image's own record of its runs: $(tr '\n' '|' <<<"$runs")"
  boots="$(grep -cv '^describe$' <<<"$runs" || true)"
  [[ "$boots" == 1 ]] || fail "the crash image booted $boots times, expected exactly 1"
  note "device: the crash image booted exactly once"
  n2_assignment="$a"
  red "N2 -> FAILED ROLLED_BACK (never UPDATED); one boot of the crash image; device on 1.0.1"
}

n6_checker_goes_red() {
  say "N6: the step-5 UPDATED checker, run against N2's attempt, must go red on exactly {state, slot, describe}"
  # N2's device rolled back: a new process, a new boot and applied reports all happened, so pid,
  # boot and applied are GREEN and must not be what turns the checker red. What says "not 1.0.2"
  # is the platform (state), the confirmed slot's bytes (slot) and the running binary (describe).
  expect_red N6 "state slot describe" "*" "" check_updated 1 "$n2_assignment" 1.0.2 "$n2_pid0" "$n2_boot0" "$n2_applied0"
  red "N6 -> the checker refused N2's attempt on the three discriminating sources"

  say "N6b: the same checker against the step-5 attempt, with the CURRENT PID and bootId as 'before'"
  # Nothing has restarted since this reading, so the liveness checks must go red, while slot, exe
  # and describe (the device really does run 1.0.1 from its confirmed slot) must stay green.
  expect_red N6b "pid boot" "slot exe describe" "" \
    check_updated 1 "$step5_assignment" 1.0.1 "$(payload_pid 1)" "$(last_boot 1)" "$(um_metric update_reports_total 'verdict="APPLIED"')"
  red "N6b -> the liveness checks can go red on their own"
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
  dx sh /tmp/killat.sh >"$rig_tmp/held" &
  n4_watcher=$!
  sleep 2
  a="$(assign ota-dev-1 fw-104)"
  wait "$n4_watcher" || fail "the download never reached 40% of $size bytes"
  n4_watcher=""
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
  # What the device must have hashed if it kept the bytes it held (the flipped one included) and
  # fetched only the rest: the image's first <held> bytes with byte 4096 set to the flipped value,
  # then the image's true tail. Built on the host from the original image, never from the device.
  local expect_file expect_digest
  expect_file="$rig_tmp/n4-expected"
  head -c "$held" "$images/ota-device-1.0.4" >"$expect_file"
  # shellcheck disable=SC2059 # the format IS the octal escape of the flipped byte
  printf "\\$(printf '%03o' "$fa")" | dd of="$expect_file" bs=1 seek=4096 count=1 conv=notrunc 2>/dev/null
  tail -c +"$((held + 1))" "$images/ota-device-1.0.4" >>"$expect_file"
  [[ "$(wc -c <"$expect_file" | tr -d ' ')" == "$size" ]] || fail "the reconstructed image is not $size bytes"
  expect_digest="sha256:$(openssl dgst -sha256 -r "$expect_file" | cut -d' ' -f1)"
  [[ "$(jq -r .attempt.failure.detail <<<"$j")" == *"$expect_digest"* ]] ||
    fail "the device's failure does not name $expect_digest, the digest of (flipped held prefix + true tail)"
  note "rig: (flipped held prefix + true tail) hashes to $expect_digest: the digest the device reported"
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
  # `rollout status` returns once the NEW pod is ready; the old one is still draining then, and it
  # still holds its NATS consumer, so it can answer a device's grant request with the OLD
  # configuration. Nothing is sampled until every old pod is gone.
  while ((SECONDS < deadline)); do
    [[ "$(k -n "$namespace" get pods -l "$um_selector" -o json | jq '[.items[] | select(.metadata.deletionTimestamp != null)] | length')" == 0 ]] && break
    sleep 2
  done
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
  # From here until the rollback below, ANY exit rolls the release back (see cleanup).
  n7_restore_rev="$rev"
  helm --kubeconfig "$kubeconfig" --kube-context "$kube_context" -n "$helm_ns" upgrade "$helm_release" \
    "$(winpath "$repo_root/deploy/helm/devicechain")" --reuse-values \
    --set-json 'functionalAreas.update-management.config.updates.downloadBaseUrl=""' >/dev/null ||
    fail "the rig-only helm upgrade failed"
  # The step-1 checker must see what was done: the rendered base is now "".
  expect_red "N7 step-1 check" "base" "*" "the rendered downloadBaseUrl is ''" check_download_base
  wait_downloads_configured false
  note "platform: updateManagementInfo.downloadsConfigured=false"
  nc0="$(um_metric update_download_grants_total 'result="not_configured"')"
  a="$(assign ota-dev-1 fw-104)"
  deadline=$((SECONDS + 60))
  while ((SECONDS < deadline)); do
    j="$(assignment_json "$a")"; st="$(jq -r .attempt.state <<<"$j")"
    seen="$seen $st"
    if [[ "$st" != QUEUED && "$st" != INITIATED ]]; then
      echo "N7 evidence: attempt $(jq -c .attempt <<<"$j")" >&2
      k -n "$namespace" get pods -l "$um_selector" -o wide >&2 || true
      k -n "$namespace" logs deploy/update-management --tail=200 2>&1 | grep -a -i "grant\|download\|capab\|not set" >&2 || true
      fail "with downloads disabled the attempt reached $st"
    fi
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
  n7_restore_rev=""
  wait_downloads_configured true
  step1_download_base
  pass "downloads restored: downloadsConfigured=true and the derived base is back"
}

# ---------------------------------------------------------------------------
# Step 7: the fleet
# ---------------------------------------------------------------------------

# OVERLAP is the deterministic half of the maxInFlight=1 check. Its input is one object per rollout
# target, {device, dispatchedAt, lastReportAt}; it prints a JSON array of everything wrong: a
# missing timestamp, a target count that is not 4, or a device dispatched BEFORE the device
# dispatched just ahead of it made its last report (two updates overlapping in time). Instants are
# compared as RFC 3339 values, [epoch seconds, nanoseconds], never as strings: "...:05Z" sorts
# after "...:05.1Z" as text although it is the earlier instant.
OVERLAP_JQ='
def inst:
  if . == null then error("a missing timestamp") else
  (capture("^(?<b>[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(\\.(?<f>[0-9]{1,9}))?(?<z>Z|[+-][0-9]{2}:[0-9]{2})$")
    // error("not an RFC 3339 instant: \(.)")) as $c
  | (($c.b + "Z") | fromdateiso8601) as $s
  | ($c.z | if . == "Z" then 0 else (((.[1:3] | tonumber) * 3600 + (.[4:6] | tonumber) * 60) * (if .[0:1] == "+" then -1 else 1 end)) end) as $off
  | [$s + $off, ((($c.f // "") + "000000000")[0:9] | tonumber)]
  end;
[ (if length != 4 then "expected 4 targets, got \(length)" else empty end),
  (.[] | select(.dispatchedAt == null or .lastReportAt == null) | "\(.device) has no dispatchedAt or lastReportAt"),
  ( [.[] | select(.dispatchedAt != null and .lastReportAt != null)] | sort_by(.dispatchedAt | inst) | . as $t
    | range(1; length)
    | select(($t[.].dispatchedAt | inst) < ($t[. - 1].lastReportAt | inst))
    | "\($t[.].device) was dispatched at \($t[.].dispatchedAt), before \($t[. - 1].device) last reported at \($t[. - 1].lastReportAt)" ) ]'

# overlap_problems <targets-json> prints one line per problem; none means the updates were serial.
overlap_problems() { jq -r "$OVERLAP_JQ | .[]" <<<"$1"; }

# offline_controls proves, with no cluster, that the overlap check can fail, and fails for the
# right reason, before its green answer on the real rollout is believed.
offline_controls() {
  say "OFFLINE CONTROLS: the rollout-overlap check, on fabricated timelines"
  local good bad naive nulls p
  good='[{"device":"d1","dispatchedAt":"2026-10-27T10:00:00Z","lastReportAt":"2026-10-27T10:00:04.9Z"},
         {"device":"d2","dispatchedAt":"2026-10-27T10:00:05Z","lastReportAt":"2026-10-27T10:00:09.123456789Z"},
         {"device":"d3","dispatchedAt":"2026-10-27T10:00:09.2Z","lastReportAt":"2026-10-27T10:00:14Z"},
         {"device":"d4","dispatchedAt":"2026-10-27T06:00:15-04:00","lastReportAt":"2026-10-27T10:00:20Z"}]'
  # d2 starts 3 s before d1 finishes.
  bad='[{"device":"d1","dispatchedAt":"2026-10-27T10:00:00Z","lastReportAt":"2026-10-27T10:00:08Z"},
        {"device":"d2","dispatchedAt":"2026-10-27T10:00:05Z","lastReportAt":"2026-10-27T10:00:12Z"},
        {"device":"d3","dispatchedAt":"2026-10-27T10:00:13Z","lastReportAt":"2026-10-27T10:00:14Z"},
        {"device":"d4","dispatchedAt":"2026-10-27T10:00:15Z","lastReportAt":"2026-10-27T10:00:20Z"}]'
  # d2 starts 100 ms before d1 finishes; compared as TEXT, "...:05Z" > "...:05.1Z" would pass it.
  naive='[{"device":"d1","dispatchedAt":"2026-10-27T10:00:00Z","lastReportAt":"2026-10-27T10:00:05.1Z"},
          {"device":"d2","dispatchedAt":"2026-10-27T10:00:05Z","lastReportAt":"2026-10-27T10:00:06Z"},
          {"device":"d3","dispatchedAt":"2026-10-27T10:00:07Z","lastReportAt":"2026-10-27T10:00:08Z"},
          {"device":"d4","dispatchedAt":"2026-10-27T10:00:09Z","lastReportAt":"2026-10-27T10:00:10Z"}]'
  nulls='[{"device":"d1","dispatchedAt":"2026-10-27T10:00:00Z","lastReportAt":null},
          {"device":"d2","dispatchedAt":"2026-10-27T10:00:05Z","lastReportAt":"2026-10-27T10:00:06Z"},
          {"device":"d3","dispatchedAt":"2026-10-27T10:00:07Z","lastReportAt":"2026-10-27T10:00:08Z"}]'
  p="$(overlap_problems "$good")" || fail "the overlap check crashed on a valid timeline"
  [[ -z "$p" ]] || fail "the overlap check rejects a sequential timeline: $p"
  note "sequential timeline (mixed precision, one -04:00 offset): accepted"
  p="$(overlap_problems "$bad")" || fail "the overlap check crashed on the overlapping timeline"
  [[ "$p" == *"d2 was dispatched at 2026-10-27T10:00:05Z, before d1 last reported"* ]] ||
    fail "the overlap check did not reject d2 overlapping d1 (got: '${p:-nothing}')"
  red "overlap check: d2 dispatched 3 s before d1 finished -> rejected"
  p="$(overlap_problems "$naive")" || fail "the overlap check crashed on the sub-second timeline"
  [[ "$p" == *"d2 was dispatched at 2026-10-27T10:00:05Z, before d1 last reported at 2026-10-27T10:00:05.1Z"* ]] ||
    fail "the overlap check passed a 100 ms overlap a string compare would miss (got: '${p:-nothing}')"
  red "overlap check: a 100 ms overlap that string comparison would pass -> rejected"
  p="$(overlap_problems "$nulls")" || fail "the overlap check crashed on missing timestamps"
  [[ "$p" == *"expected 4 targets, got 3"* && "$p" == *"d1 has no dispatchedAt or lastReportAt"* ]] ||
    fail "the overlap check accepted a missing timestamp or a short list (got: '${p:-nothing}')"
  red "overlap check: three targets, one with no lastReportAt -> rejected on both counts"
}

# last_report_at <assignment> prints the attempt's lastReportAt, or nothing.
last_report_at() {
  updq 'query($t:String!){updateAssignment(token:$t){attempt{lastReportAt}}}' "$(jq -n --arg t "$1" '{t:$t}')" |
    jq -r '.updateAssignment.attempt.lastReportAt // empty'
}

step7_fleet() {
  say "STEP 7: three more devices, a static group of four, a rollout of 1.0.4 with maxInFlight=1"
  local n reqs r j st max=0 inflight pinflight deadline samples=0 prog r0 targets timeline p id dev asg disp last
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
    # Two reads per sample. From the assignments: every device's current firmware assignment for
    # this artifact that is not finished (UNKNOWN keeps its place under the cap, so it counts).
    # From the rollout: the platform's own count of devices in flight.
    j="$(updq 'query{updateAssignments(criteria:{pageNumber:1,pageSize:50,component:"firmware",currentOnly:true}){results{deviceToken artifact{token} attempt{state}}}}')" ||
      fail "updateAssignments"
    inflight="$(jq '[.updateAssignments.results[] | select(.artifact.token == "fw-104")
      | select(.attempt.state | test("^(UPDATED|FAILED|TIMED_OUT|CANCELLED)$") | not)] | length' <<<"$j")"
    prog="$(updq 'query($t:String!){updateRollout(token:$t){state progress{total pending inFlight succeeded failed unknown cancelled skipped}}}' \
      "$(jq -n --arg t "$r" '{t:$t}')" | jq -c .updateRollout)" || fail "updateRollout"
    pinflight="$(jq -r .progress.inFlight <<<"$prog")"
    samples=$((samples + 1))
    ((inflight > max)) && max="$inflight"
    ((pinflight > max)) && max="$pinflight"
    ((inflight <= 1)) || fail "$inflight attempts unfinished at once under maxInFlight=1: $j"
    ((pinflight <= 1)) || fail "the rollout itself reports $pinflight in flight under maxInFlight=1: $prog"
    st="$(jq -r .state <<<"$prog")"
    [[ "$st" == RUNNING ]] || break
    sleep 1
  done
  note "rollout: $prog after $samples samples; most in flight at once: $max"
  [[ "$st" == COMPLETED ]] || fail "the rollout ended $st, not COMPLETED: $prog"
  [[ "$(jq -r .progress.succeeded <<<"$prog")" == 4 ]] || fail "the rollout did not succeed on 4/4: $prog"
  # A sampler that never saw anything in flight has shown nothing about the cap.
  ((max >= 1)) || fail "no sample ever saw an update in flight: the cap was never observed"

  # The deterministic half: each dispatch against the previous device's last report.
  targets="$(updq 'query($t:String!){updateRolloutTargets(token:$t,criteria:{pageNumber:1,pageSize:50}){results{deviceToken assignmentToken dispatchedAt outcome}}}' \
    "$(jq -n --arg t "$r" '{t:$t}')")" || fail "updateRolloutTargets"
  timeline="[]"
  while read -r dev asg disp; do
    last=""
    [[ "$asg" == null ]] || last="$(last_report_at "$asg")"
    timeline="$(jq -c --arg d "$dev" --arg s "$disp" --arg l "$last" \
      '. + [{device:$d, dispatchedAt:(if $s == "null" then null else $s end), lastReportAt:(if $l == "" then null else $l end)}]' <<<"$timeline")"
  done < <(jq -r '.updateRolloutTargets.results[] | "\(.deviceToken) \(.assignmentToken // "null") \(.dispatchedAt // "null")"' <<<"$targets")
  jq -r '.[] | "    \(.device): dispatched \(.dispatchedAt), last report \(.lastReportAt)"' <<<"$timeline"
  p="$(overlap_problems "$timeline")" || fail "the overlap check could not read the rollout's timeline: $timeline"
  [[ -z "$p" ]] || fail "the rollout's updates overlapped: $p"
  note "platform: every dispatch came after the previous device's last report (4 non-null pairs)"
  for n in 1 2 3 4; do
    id="$(describe_running "$(wait_payload "$n")")"
    [[ "$(jq -r .version <<<"$id")" == 1.0.4 ]] || fail "device $n runs $id after the rollout"
    note "device $n: $id"
  done
  pass "rollout COMPLETED, 4/4 SUCCEEDED, serial by its own timestamps, every device describes itself as 1.0.4"

  say "STEP 7: the device-state checker must go red against the new state"
  # Device 1 went 1.0.1 (slot b) -> 1.0.4 (slot a). Asked about its OLD version on its new slot,
  # only the version may disagree; asked about its new version on its OLD slot, only the slot may.
  expect_red "still_on 1 1.0.1 a" "version" "*" "expected 1.0.1" check_still_on 1 1.0.1 a
  expect_red "still_on 1 1.0.4 b" "exe boot" "*" "not slot b" check_still_on 1 1.0.4 b
  red "the still_on checker names what moved, and only that"
}

# ---------------------------------------------------------------------------
# Step 8: the failure matrix (M1-M7)
# ---------------------------------------------------------------------------
#
# The happy path and N1-N7 above show that a planted DEVICE-side defect turns the verdict red. The
# matrix covers what a field fleet does to an update that no defect planted in an image can: the
# network goes away, an operator changes their mind, a deadline passes, one device of many cannot
# take the image. Each case reads the PLATFORM's state (the assignment, the rollout) and the
# DEVICE's (what it runs, what it left on disk), and each is shown able to fail:
#
#   M1  a signature that does not verify is refused at the platform (not stored, artifact stays
#       UNVERIFIED, not assignable); the same artifact signed correctly is then accepted. The
#       device-side SIGNATURE_INVALID branch cannot be reached through a platform that checks a
#       signature before storing it; it is pinned by examples/ota-device/internal/verify.
#   M2  bytes that do not hash to the declared digest are refused at upload (artifact stays
#       UPLOADING, not assignable); the true bytes are then accepted. The device-side digest
#       check is N4.
#   M3  the device's network goes down at 50% of a download and stays down past its idle timeout:
#       the platform holds the attempt non-terminal, and on reconnect it resumes with a Range
#       request (the server streams less again than the device already held) and reaches UPDATED.
#   M4  the device is offline when the update is assigned: the platform holds it (QUEUED or
#       INITIATED) and it completes on reconnect.
#   M5  CANCELLATION. M5a: cancelled mid-download while the device is unreachable: the request is
#       recorded (cancelRequested), the attempt stays non-terminal until the device confirms, then
#       CANCELLED; the device keeps its version and leaves no partial file. M5b: cancelled while
#       queued for an offline device: CANCELLED at once and the device downloads nothing.
#   M6  TIMEOUT: the download-phase deadline is cut to 20 s by a rig-only helm upgrade; a device
#       that goes silent mid-download ends TIMED_OUT, and what the late device does is recorded.
#   M7  PARTIAL ROLLOUT FAILURE: a four-device rollout with maxInFlight=1 and failureThreshold=1,
#       the second target unable to take the image: the rollout PAUSES at the threshold with the
#       remaining targets undispatched and still on their old version; resumed with a higher
#       threshold it dispatches the rest and COMPLETES, the failed device still reported failed.
#
# Negative controls: check_attempt and check_rollout are each run against a state that does not
# match and must go red on exactly the field that differs; the resume arithmetic is shown to
# reject a restart from byte zero; every outage is shown real by a broker probe that fails while
# the link is down and succeeds after, and every "holds" assertion is followed by the release that
# proves the wait was not vacuous.

mx_gw=""
mx_held=""
mx_assignment=""
mx_link_down=0
mx_watcher=""

# resume_problems <size> <held> <served>: why a download that streamed <served> bytes in total for
# an artifact of <size> bytes, of which the device held <held> when it was cut, was not a Range
# resume. A resume streams the whole image once plus whatever the server had written that the
# device never received; a restart from byte zero streams at least <held> bytes again.
resume_problems() {
  local size="$1" held="$2" served="$3"
  ((served >= size)) || echo "the server streamed $served bytes in all, fewer than the image's $size"
  ((served - size < held)) || echo "the server streamed $((served - size)) bytes beyond the image, at least the $held the device held: that is a restart, not a resume"
}

offline_controls_resume() {
  local p
  p="$(resume_problems 48000000 24000000 49500000)"
  [[ -z "$p" ]] || fail "the resume check rejects a resume that re-sent 1.5 MB of in-flight bytes: $p"
  note "resume arithmetic: 49.5 MB streamed for a 48 MB image held to 24 MB: accepted as a resume"
  p="$(resume_problems 48000000 24000000 72000000)"
  [[ "$p" == *"a restart, not a resume"* ]] || fail "the resume check accepted a restart from byte zero (got: '${p:-nothing}')"
  red "resume arithmetic: 72 MB streamed for a 48 MB image held to 24 MB (a restart from zero) -> rejected"
  p="$(resume_problems 48000000 24000000 30000000)"
  [[ "$p" == *"fewer than the image"* ]] || fail "the resume check accepted a download that streamed less than the image (got: '${p:-nothing}')"
  red "resume arithmetic: 30 MB streamed for a 48 MB image -> rejected"
}

# inactive_slot <n> prints the slot a download on device n goes to. A device the launcher has never
# switched has no boot.json yet and runs slot a, so a missing file means "a is confirmed", never "b".
inactive_slot() {
  local c
  c="$(dx sh -c "cat /dev$1/boot.json 2>/dev/null || true" | jq -r '.confirmed // empty')"
  if [[ "$c" == b ]]; then echo a; else echo b; fi
}

# probe_broker succeeds when the device container can open the instance's MQTT node port.
probe_broker() { dx sh -c "nc -z -w 3 $node_ip 31883" >/dev/null 2>&1; }

# link_down / link_up take the device container's network away and give it back. The rig's own
# calls to the instance go through the host, so they are not affected. Every device in the
# container is offline while it is down; the matrix runs one case at a time.
# mx_read_gw remembers the container's default gateway the first time, while the route still has one.
mx_read_gw() {
  [[ -z "$mx_gw" ]] || return 0
  mx_gw="$(dx ip route show default | awk '{for (i = 1; i < NF; i++) if ($i == "via") { print $(i + 1); break }}')"
  [[ "$mx_gw" =~ ^[0-9.]+$ ]] || { mx_gw=""; fail "cannot read the container's default gateway"; }
}
link_down() {
  mx_read_gw
  dx ip link set eth0 down
  mx_link_down=1
}
link_up() {
  dx ip link set eth0 up
  dx ip route replace default via "$mx_gw" dev eth0
  mx_link_down=0
}

expect_offline() { if probe_broker; then fail "the broker is still reachable from the device container: the outage is not real"; fi; note "probe: the broker's node port is unreachable from the device container"; }
expect_online() {
  local deadline=$((SECONDS + 30))
  until probe_broker; do ((SECONDS < deadline)) || fail "the broker is unreachable after the link came back"; sleep 1; done
  note "probe: the broker's node port is reachable again"
}

# arm_outage <n> <size> <percent>: a watcher INSIDE the container takes the link down the moment
# device n's partial download reaches <percent> of <size>, then prints the bytes it holds.
arm_outage() {
  local n="$1" size="$2" pct="$3" slot target
  mx_read_gw
  slot="$(inactive_slot "$n")"; target=$((size * pct / 100))
  dx test ! -e "/dev$n/slots/$slot/ota-device.part" || fail "a .part file is already in device $n's slot $slot"
  dxi sh -c "cat > /tmp/linkat.sh" <<EOF
#!/bin/sh
part=/dev$n/slots/$slot/ota-device.part
end=\$((\$(date +%s) + 120))
while [ "\$(date +%s)" -lt "\$end" ]; do
  s=\$(stat -c %s "\$part" 2>/dev/null || echo 0)
  if [ "\$s" -ge $target ]; then
    ip link set eth0 down
    sleep 1
    stat -c %s "\$part"
    exit 0
  fi
done
echo timeout
exit 1
EOF
  dx sh /tmp/linkat.sh >"$rig_tmp/outage-held" &
  mx_watcher=$!
  sleep 2
}

# await_outage <size>: waits for the watcher (so it must run in this shell, never in a command
# substitution), marks the link down and leaves the bytes held in mx_held.
await_outage() {
  local size="$1" held
  wait "$mx_watcher" || {
    echo "outage evidence: assignment ${mx_assignment:-?}: $(mx_out="$(assignment_json "${mx_assignment:-x}" 2>&1)"; printf '%s' "${mx_out:0:600}")" >&2
    dx sh -c 'ls -la /dev*/slots/*/ 2>&1 | sed -n 1,40p' >&2 || true
    fail "the download never reached the outage threshold of $size bytes"
  }
  mx_watcher=""
  mx_link_down=1
  held="$(tr -d '\r\n ' <"$rig_tmp/outage-held")"
  [[ "$held" =~ ^[0-9]+$ && "$held" -gt 0 && "$held" -lt "$size" ]] ||
    fail "the outage did not land mid-download (held '$held' of $size); raise DC_OTA_RIG_PAD_MB"
  mx_held="$held"
}

# hold_nonterminal <assignment> <seconds> [stop-state]: samples the attempt about every 3 s and
# fails the moment it is terminal (unless it is <stop-state>, which ends the wait early and
# successfully). Prints the distinct states seen on one line.
hold_nonterminal() {
  local a="$1" deadline=$((SECONDS + $2)) stop="${3:-}" j st seen=""
  while ((SECONDS < deadline)); do
    j="$(assignment_json "$a")" || j=""
    st="$(jq -r '.attempt.state // empty' <<<"$j")"
    seen="$seen $st"
    if [[ -n "$stop" && "$st" == "$stop" ]]; then break; fi
    if terminal_state "$st"; then fail "attempt $a went terminal ($st) while it was meant to be held: $(jq -c .attempt <<<"$j")"; fi
    sleep 3
  done
  tr ' ' '\n' <<<"$seen" | sed '/^$/d' | uniq | paste -sd' '
}

# check_attempt <assignment> <state> <failure code | "-" for none | "*" for any>: the platform's
# verdict on one attempt. A checker (see CHECKERS); its tags are state and code.
check_attempt() {
  local a="$1" want="$2" code="$3" j st got
  local -a red=()
  j="$(assignment_json "$a")" || { echo "TRANSPORT: assignment $a could not be read"; return 2; }
  [[ -n "$j" && "$j" != null ]] || { echo "TRANSPORT: there is no assignment $a"; return 2; }
  st="$(jq -r '.attempt.state' <<<"$j")"; got="$(jq -r '.attempt.failure.code // "-"' <<<"$j")"
  [[ "$st" == "$want" ]] || { red+=(state); echo "  state: the attempt is $st, wanted $want"; }
  [[ "$code" == "*" || "$got" == "$code" ]] || { red+=(code); echo "  code: the failure code is $got, wanted $code"; }
  if ((${#red[@]})); then echo "RED: ${red[*]}"; return 1; fi
  echo "platform: assignment $a is $st (failure code $got)"
  echo GREEN
}

# check_rollout <rollout> <state> <succeeded> <failed> <pending> <pause reason | "-">
# A checker; its tags are state, counts and reason.
check_rollout() {
  local r="$1" want="$2" ok="$3" bad="$4" pend="$5" reason="$6" j st p reasongot
  local -a red=()
  j="$(updq 'query($t:String!){updateRollout(token:$t){state pauseReason progress{total pending inFlight succeeded failed unknown cancelled skipped}}}' \
    "$(jq -n --arg t "$r" '{t:$t}')" | jq -c .updateRollout)" || { echo "TRANSPORT: rollout $r could not be read"; return 2; }
  [[ -n "$j" && "$j" != null ]] || { echo "TRANSPORT: there is no rollout $r"; return 2; }
  st="$(jq -r .state <<<"$j")"; reasongot="$(jq -r '.pauseReason // "-"' <<<"$j")"
  p="$(jq -r '"succeeded=\(.progress.succeeded) failed=\(.progress.failed) pending=\(.progress.pending)"' <<<"$j")"
  [[ "$st" == "$want" ]] || { red+=(state); echo "  state: the rollout is $st, wanted $want"; }
  [[ "$p" == "succeeded=$ok failed=$bad pending=$pend" ]] || { red+=(counts); echo "  counts: the rollout shows $p, wanted succeeded=$ok failed=$bad pending=$pend"; }
  [[ "$reasongot" == "$reason" ]] || { red+=(reason); echo "  reason: the pause reason is $reasongot, wanted $reason"; }
  if ((${#red[@]})); then echo "RED: ${red[*]}"; return 1; fi
  echo "platform: rollout $r is $st ($p, pause reason $reasongot)"
  echo GREEN
}

# expect_green <label> <checker...>: a checker that must pass; its explanation is printed.
expect_green() {
  local label="$1" out
  shift
  out="$("$@" 2>&1)" || fail "$label: $out"
  printf '%s\n' "${out%$'\n'GREEN}" | sed 's/^/    | /'
}

build_matrix_images() {
  say "STEP 8: building the matrix images (1.0.5 and 1.0.7 and 1.0.8 padded, 1.0.6 and 1.1.0 small)"
  local ex="$repo_root/examples/ota-device" bi="devicechain.io/examples/ota-device/internal/buildinfo" v
  for v in 1.0.5 1.0.6 1.0.7 1.0.8 1.1.0; do
    (cd "$ex" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-X $bi.Version=$v" -o "$images/ota-device-$v" ./cmd/ota-device)
  done
  # A different run of random bytes on each padded image: the digests must differ.
  for v in 1.0.5 1.0.7 1.0.8; do head -c "$((pad_mb * 1024 * 1024))" /dev/urandom >>"$images/ota-device-$v"; done
}

# m_artifact_state <token> prints "<state> <trustStatus>".
m_artifact_state() {
  updq 'query($t:String!){artifact(token:$t){state trustStatus}}' "$(jq -n --arg t "$1" '{t:$t}')" |
    jq -r '.artifact | "\(.state) \(.trustStatus)"'
}

m1_bad_signature() {
  say "M1: a signature that does not verify is refused at the platform"
  local file="$images/ota-device-1.1.0" other="$images/ota-device-1.0.0" tok=fw-m-badsig size digest bad out
  size="$(wc -c <"$file" | tr -d ' ')"; digest="sha256:$(openssl dgst -sha256 -r "$file" | cut -d' ' -f1)"
  updq 'mutation($r:ArtifactCreateRequest!){createArtifact(request:$r){token state}}' \
    "$(jq -n --arg t "$tok" --arg s "$size" --arg d "$digest" --arg p "$profile" \
      '{r:{token:$t,kind:"FIRMWARE",name:"ota-device-badsig",version:"1.1.1",component:"firmware",sizeBytes:$s,digest:$d,mediaType:"application/octet-stream",compatibleProfiles:[$p]}}')" >/dev/null ||
    fail "createArtifact $tok"
  [[ "$(curl_api -o /dev/null -w '%{http_code}' -X PUT "$upd/updates/artifacts/$tok/content" -H "Authorization: Bearer $tenant_token" \
    -H 'Content-Type: application/octet-stream' --data-binary "@$(winpath "$file")")" == 2* ]] || fail "upload of $tok"
  # A well-formed ECDSA signature by the signer's own key, over DIFFERENT bytes.
  bad="$(openssl dgst -sha256 -sign "$rig_tmp/ecdsa.pem" -binary "$other" | base64 | tr -d '\n')"
  if out="$(updq 'mutation($a:String!,$s:String!,$g:String!){addArtifactSignature(artifactToken:$a,signerToken:$s,signature:$g){state trustStatus}}' \
    "$(jq -n --arg a "$tok" --arg s "$(jq -r '.registerArtifactSigner.token' "$rig_tmp/signer-ecdsa.json")" --arg g "$bad" '{a:$a,s:$s,g:$g}')" 2>&1)"; then
    fail "the platform ACCEPTED a signature that does not verify: $out"
  fi
  note "platform refused the signature: $(head -c 300 <<<"$out")"
  [[ "$(m_artifact_state "$tok")" == "AVAILABLE UNVERIFIED" ]] || fail "after the refusal $tok is '$(m_artifact_state "$tok")', expected AVAILABLE UNVERIFIED"
  note "platform: $tok stays AVAILABLE UNVERIFIED"
  if out="$(updq 'mutation($d:String!,$a:String!){assignArtifact(deviceToken:$d,artifactToken:$a){token}}' "$(jq -n --arg d ota-dev-1 --arg a "$tok" '{d:$d,a:$a}')" 2>&1)"; then
    fail "an artifact whose only signature was refused was ASSIGNED: $out"
  fi
  note "platform refused to assign it: $(head -c 300 <<<"$out")"
  # The control: the same call with the correct signature is accepted, so the refusal was about the signature.
  out="$(openssl dgst -sha256 -sign "$rig_tmp/ecdsa.pem" -binary "$file" | base64 | tr -d '\n')"
  updq 'mutation($a:String!,$s:String!,$g:String!){addArtifactSignature(artifactToken:$a,signerToken:$s,signature:$g){state trustStatus}}' \
    "$(jq -n --arg a "$tok" --arg s "$(jq -r '.registerArtifactSigner.token' "$rig_tmp/signer-ecdsa.json")" --arg g "$out" '{a:$a,s:$s,g:$g}')" >/dev/null ||
    fail "the CORRECT signature on $tok was refused: the M1 refusal proves nothing"
  [[ "$(m_artifact_state "$tok")" == "AVAILABLE SIGNED" ]] || fail "after the correct signature $tok is '$(m_artifact_state "$tok")'"
  red "M1 -> a non-verifying signature is refused, the artifact stays UNVERIFIED and unassignable; the correct one makes it SIGNED"
}

m2_hash_mismatch_at_upload() {
  say "M2: bytes that do not hash to the declared digest are refused at upload"
  local file="$images/ota-device-1.1.0" tok=fw-m-hash size digest code last
  size="$(wc -c <"$file" | tr -d ' ')"; digest="sha256:$(openssl dgst -sha256 -r "$file" | cut -d' ' -f1)"
  updq 'mutation($r:ArtifactCreateRequest!){createArtifact(request:$r){token state}}' \
    "$(jq -n --arg t "$tok" --arg s "$size" --arg d "$digest" --arg p "$profile" \
      '{r:{token:$t,kind:"FIRMWARE",name:"ota-device-hash",version:"1.1.2",component:"firmware",sizeBytes:$s,digest:$d,mediaType:"application/octet-stream",compatibleProfiles:[$p]}}')" >/dev/null ||
    fail "createArtifact $tok"
  # The same size, one byte different.
  cp "$file" "$rig_tmp/m2-tampered"
  last="$(tail -c 1 "$file" | od -An -tu1 | tr -d ' ')"
  # shellcheck disable=SC2059 # the format IS the computed octal escape, by design
  printf "$(printf '\\%03o' $(((last + 1) % 256)))" | dd of="$rig_tmp/m2-tampered" bs=1 seek=$((size - 1)) count=1 conv=notrunc 2>/dev/null
  [[ "$(wc -c <"$rig_tmp/m2-tampered" | tr -d ' ')" == "$size" ]] || fail "the tampered copy is not $size bytes"
  code="$(curl_api -o "$rig_tmp/upload.out" -w '%{http_code}' -X PUT "$upd/updates/artifacts/$tok/content" \
    -H "Authorization: Bearer $tenant_token" -H 'Content-Type: application/octet-stream' --data-binary "@$(winpath "$rig_tmp/m2-tampered")")"
  [[ "$code" != 2* ]] || fail "the platform ACCEPTED bytes that do not hash to the declared digest (HTTP $code)"
  note "platform refused the upload: HTTP $code $(head -c 300 "$rig_tmp/upload.out")"
  [[ "$(m_artifact_state "$tok")" == "UPLOADING UNVERIFIED" ]] || fail "after the refusal $tok is '$(m_artifact_state "$tok")', expected UPLOADING UNVERIFIED"
  note "platform: $tok stays UPLOADING (no content stored)"
  code="$(curl_api -o "$rig_tmp/upload.out" -w '%{http_code}' -X PUT "$upd/updates/artifacts/$tok/content" \
    -H "Authorization: Bearer $tenant_token" -H 'Content-Type: application/octet-stream' --data-binary "@$(winpath "$file")")"
  [[ "$code" == 2* ]] || fail "the TRUE bytes were refused too (HTTP $code): the M2 refusal proves nothing"
  [[ "$(m_artifact_state "$tok")" == "AVAILABLE UNVERIFIED" ]] || fail "after the true bytes $tok is '$(m_artifact_state "$tok")'"
  red "M2 -> tampered bytes refused at upload, artifact stays UPLOADING; the true bytes make it AVAILABLE"
}

m3_offline_midflight() {
  say "M3: device 2's network goes down at 50% of the 1.0.5 download and stays down 45 s (idle timeout 30 s)"
  local file="$images/ota-device-1.0.5" size held a pid0 boot0 applied0 bytes0 bytes1 served seen p
  size="$(wc -c <"$file" | tr -d ' ')"
  publish fw-m105 ota-device 1.0.5 "$file" ecdsa
  wait_payload 2 >/dev/null
  pid0="$(payload_pid 2)"; boot0="$(last_boot 2)"; applied0="$(um_metric update_reports_total 'verdict="APPLIED"')"
  bytes0="$(um_metric update_download_bytes_total '')"
  arm_outage 2 "$size" 50
  a="$(assign ota-dev-2 fw-m105)"
  mx_assignment="$a"
  await_outage "$size"
  held="$mx_held"
  note "device: network down holding $held of $size bytes ($((held * 100 / size))%)"
  expect_offline
  seen="$(hold_nonterminal "$a" 45)"
  note "platform: states over the 45 s outage: $seen"
  [[ " $seen " != *" UPDATED "* ]] || fail "the attempt finished while the device was offline"
  link_up
  expect_online
  j="$(wait_terminal "$a" 420)"
  [[ "$(jq -r .attempt.state <<<"$j")" == UPDATED ]] || fail "after reconnecting the update ended $(jq -c .attempt <<<"$j")"
  sleep 3
  check_updated 2 "$a" 1.0.5 "$pid0" "$boot0" "$applied0" >"$rig_tmp/check-m3" || fail "M3: $(cat "$rig_tmp/check-m3")"
  grep -v '^GREEN$' "$rig_tmp/check-m3"
  bytes1="$(um_metric update_download_bytes_total '')"
  served=$((bytes1 - bytes0))
  note "server: $served bytes streamed in all for a $size-byte image; the device held $held when cut"
  p="$(resume_problems "$size" "$held" "$served")"
  [[ -z "$p" ]] || fail "M3 was not a Range resume: $p"
  note "the download resumed: $((served - size)) bytes beyond the image (the server's in-flight writes), fewer than the $held held"
  red "M3 -> an offline period past the idle timeout mid-download: attempt held non-terminal, resumed by Range on reconnect, UPDATED on 1.0.5"
}

m4_offline_before_assign() {
  say "M4: device 1 is offline when 1.0.6 is assigned"
  local file="$images/ota-device-1.0.6" a j pid0 boot0 applied0 seen
  publish fw-m106 ota-device 1.0.6 "$file" ecdsa
  wait_payload 1 >/dev/null
  pid0="$(payload_pid 1)"; boot0="$(last_boot 1)"; applied0="$(um_metric update_reports_total 'verdict="APPLIED"')"
  link_down
  expect_offline
  a="$(assign ota-dev-1 fw-m106)"
  mx_assignment="$a"
  seen="$(hold_nonterminal "$a" 30)"
  note "platform: states over 30 s with the device offline: $seen"
  [[ "$seen" =~ ^(QUEUED|INITIATED)( (QUEUED|INITIATED))*$ ]] || fail "with the device offline the attempt should be held at QUEUED or INITIATED, it went through: $seen"
  link_up
  expect_online
  j="$(wait_terminal "$a" 420)"
  [[ "$(jq -r .attempt.state <<<"$j")" == UPDATED ]] || fail "after reconnecting the update ended $(jq -c .attempt <<<"$j")"
  sleep 3
  check_updated 1 "$a" 1.0.6 "$pid0" "$boot0" "$applied0" >"$rig_tmp/check-m4" || fail "M4: $(cat "$rig_tmp/check-m4")"
  grep -v '^GREEN$' "$rig_tmp/check-m4"
  red "M4 -> assigned to an offline device: held at ${seen// /, }; completed on reconnect, UPDATED on 1.0.6"
}

m5_cancellation() {
  say "M5a: cancel mid-download while device 3 is unreachable"
  local file="$images/ota-device-1.0.7" size held a j slot before after version0 seen bytes0 bytes1 id
  size="$(wc -c <"$file" | tr -d ' ')"
  publish fw-m107 ota-device 1.0.7 "$file" ecdsa
  wait_payload 3 >/dev/null
  version0="$(jq -r .version <<<"$(describe_running "$(payload_pid 3)")")"
  slot="$(inactive_slot 3)"; before="$(slot_hash 3 "$slot")"
  arm_outage 3 "$size" 50
  a="$(assign ota-dev-3 fw-m107)"
  mx_assignment="$a"
  await_outage "$size"
  held="$mx_held"
  note "device: network down holding $held of $size bytes"
  expect_offline
  updq 'mutation($t:String!){cancelUpdate(assignmentToken:$t){token attempt{state cancelRequested}}}' "$(jq -n --arg t "$a" '{t:$t}')" >"$rig_tmp/cancel.json" ||
    fail "cancelUpdate mid-download"
  note "platform: cancelUpdate answered $(jq -c .cancelUpdate.attempt "$rig_tmp/cancel.json")"
  seen="$(hold_nonterminal "$a" 20)"
  j="$(assignment_json "$a")"
  [[ "$(jq -r .attempt.cancelRequested <<<"$j")" == true ]] || fail "the cancel request was not recorded: $(jq -c .attempt <<<"$j")"
  note "platform: cancelRequested=true and the attempt stays $seen until the device confirms"
  link_up
  expect_online
  j="$(wait_terminal "$a" 420)"
  expect_green "M5a platform" check_attempt "$a" CANCELLED -
  wait_payload 3 >/dev/null
  id="$(describe_running "$(payload_pid 3)")"
  [[ "$(jq -r .version <<<"$id")" == "$version0" ]] || fail "device 3 runs $id after the cancel, it ran $version0 before"
  after="$(slot_hash 3 "$slot")"
  [[ "$before" == "$after" ]] || fail "the inactive slot changed: $before -> $after"
  dx test ! -e "/dev3/slots/$slot/ota-device.part" || fail "the cancelled download left a .part file in slot $slot"
  note "device: still $id, inactive slot untouched, no partial file left"
  expect_red "M5a checker negative control" "state" "*" "wanted UPDATED" check_attempt "$a" UPDATED -
  red "M5a -> cancel recorded while unreachable (cancelRequested, non-terminal), CANCELLED once the device confirmed; device on $version0, nothing left behind"

  say "M5b: cancel while queued for an offline device 4: cancelled at once, nothing downloaded"
  version0="$(jq -r .version <<<"$(describe_running "$(payload_pid 4)")")"
  link_down
  expect_offline
  bytes0="$(um_metric update_download_bytes_total '')"
  a="$(assign ota-dev-4 fw-m107)"
  mx_assignment="$a"
  updq 'mutation($t:String!){cancelUpdate(assignmentToken:$t){token attempt{state cancelRequested}}}' "$(jq -n --arg t "$a" '{t:$t}')" >"$rig_tmp/cancel.json" ||
    fail "cancelUpdate while queued"
  note "platform: cancelUpdate answered $(jq -c .cancelUpdate.attempt "$rig_tmp/cancel.json")"
  expect_green "M5b platform" check_attempt "$a" CANCELLED -
  link_up
  expect_online
  sleep 25
  bytes1="$(um_metric update_download_bytes_total '')"
  [[ "$bytes0" == "$bytes1" ]] || fail "the device downloaded $((bytes1 - bytes0)) bytes of a cancelled update"
  expect_green "M5b platform, after the reconnect" check_attempt "$a" CANCELLED -
  wait_payload 4 >/dev/null
  id="$(describe_running "$(payload_pid 4)")"
  [[ "$(jq -r .version <<<"$id")" == "$version0" ]] || fail "device 4 runs $id after the cancel, it ran $version0 before"
  note "device: still $id; the server streamed 0 bytes after the cancel"
  expect_red "M5b checker negative control" "state" "*" "wanted UPDATED" check_attempt "$a" UPDATED -
  red "M5b -> cancelled at once while queued; the device came back, downloaded nothing and kept $version0"
}

m6_timeout() {
  say "M6: the download-phase deadline cut to 20 s; device 4 goes silent mid-download"
  local file="$images/ota-device-1.0.8" size held a j rev deadline st version0 id late
  need helm
  size="$(wc -c <"$file" | tr -d ' ')"
  publish fw-m108 ota-device 1.0.8 "$file" ecdsa
  helm_ns="$(helm --kubeconfig "$kubeconfig" --kube-context "$kube_context" list -A -o json |
    jq -r --arg r "$helm_release" '.[] | select(.name == $r) | .namespace')"
  [[ -n "$helm_ns" ]] || fail "no helm release $helm_release on $cluster"
  rev="$(helm --kubeconfig "$kubeconfig" --kube-context "$kube_context" -n "$helm_ns" history "$helm_release" -o json | jq -r '.[-1].revision')"
  n7_restore_rev="$rev"
  helm --kubeconfig "$kubeconfig" --kube-context "$kube_context" -n "$helm_ns" upgrade "$helm_release" \
    "$(winpath "$repo_root/deploy/helm/devicechain")" --reuse-values \
    --set-json 'functionalAreas.update-management.config.updates.sweepIntervalSeconds=5' \
    --set-json 'functionalAreas.update-management.config.updates.deadlines.downloadSeconds=20' >/dev/null ||
    fail "the rig-only helm upgrade failed"
  wait_downloads_configured true
  note "platform: update-management rolled out with deadlines.downloadSeconds=20, sweepIntervalSeconds=5"
  wait_payload 4 >/dev/null
  version0="$(jq -r .version <<<"$(describe_running "$(payload_pid 4)")")"
  arm_outage 4 "$size" 50
  a="$(assign ota-dev-4 fw-m108)"
  mx_assignment="$a"
  await_outage "$size"
  held="$mx_held"
  note "device: network down holding $held of $size bytes"
  expect_offline
  deadline=$((SECONDS + 180)); st=""
  while ((SECONDS < deadline)); do
    j="$(assignment_json "$a")" || j=""
    st="$(jq -r '.attempt.state // empty' <<<"$j")"
    [[ "$st" == TIMED_OUT ]] && break
    terminal_state "$st" && fail "the silent device's attempt went $st, not TIMED_OUT: $(jq -c .attempt <<<"$j")"
    sleep 3
  done
  [[ "$st" == TIMED_OUT ]] || fail "the attempt never timed out (last state '$st')"
  note "platform: TIMED_OUT while the device was silent: $(jq -c .attempt <<<"$j")"
  expect_green "M6 platform" check_attempt "$a" TIMED_OUT '*'
  expect_red "M6 checker negative control" "state" "*" "wanted UPDATED" check_attempt "$a" UPDATED "*"
  # Give the deadline back before the device returns, so what follows is the product's behaviour.
  helm --kubeconfig "$kubeconfig" --kube-context "$kube_context" -n "$helm_ns" rollback "$helm_release" "$rev" >/dev/null ||
    fail "helm rollback to $rev failed"
  n7_restore_rev=""
  wait_downloads_configured true
  link_up
  expect_online
  sleep 90
  j="$(assignment_json "$a")"
  late="$(jq -c '.attempt' <<<"$j")"
  id="$(describe_running "$(payload_pid 4)")"
  note "platform: 90 s after the device returned the attempt is $late"
  note "device: runs $id (it ran $version0 before)"
  [[ "$(jq -r .attempt.state <<<"$j")" == TIMED_OUT ]] ||
    fail "the TIMED_OUT attempt changed to $(jq -r .attempt.state <<<"$j") after the device returned: a terminal verdict was reopened"
  [[ "$(jq -r .version <<<"$id")" == "$version0" ]] ||
    fail "the device installed $id after the platform declared the attempt TIMED_OUT: platform and device disagree"
  red "M6 -> TIMED_OUT while silent; after it returned the platform stayed TIMED_OUT and the device stayed on $version0"
}

m7_partial_rollout() {
  say "M7: four devices, maxInFlight=1, failureThreshold=1; the second target cannot take the image"
  local file="$images/ota-device-1.1.0" g=ota-rig-matrix r=rollout-m110 reqs j order second dev slot n deadline st id
  publish fw-m110 ota-device 1.1.0 "$file" ecdsa
  dmq 'mutation($r:EntityGroupCreateRequest){createEntityGroup(request:$r){token}}' \
    "$(jq -n --arg t "$g" '{r:{token:$t,name:"OTA rig matrix",memberType:"device",membershipMode:"static"}}')" >/dev/null ||
    fail "createEntityGroup $g"
  reqs="$(jq -n --arg g "$g" '[range(1;5) | {token:"\($g)-m\(.)",sourceType:"group",source:$g,targetType:"device",target:"ota-dev-\(.)",relationshipType:"member"}]')"
  dmq 'mutation($r:[EntityRelationshipCreateRequest!]!){createEntityRelationships(requests:$r){token}}' \
    "$(jq -n --argjson r "$reqs" '{r:$r}')" >/dev/null || fail "adding the four devices to $g"
  for n in 1 2 3 4; do wait_payload "$n" >/dev/null; done
  # The fault goes in BEFORE the start: an 11 MB image updates a device in a couple of seconds, so a
  # fault planted after the start arrives after the second device is done. The members were added
  # in order and the rollout freezes them in that order; that assumption is checked below against
  # the order the platform reports, and the case fails if it does not hold.
  second=ota-dev-2
  n=2
  slot="$(inactive_slot "$n")"
  # A NON-EMPTY directory where the partial file goes: the device removes an empty one.
  dx sh -c "mkdir -p /dev$n/slots/$slot/ota-device.part && touch /dev$n/slots/$slot/ota-device.part/keep"
  updq 'mutation($r:UpdateRolloutCreateRequest!){createUpdateRollout(request:$r){token state}}' \
    "$(jq -n --arg t "$r" --arg g "$g" '{r:{token:$t,name:"OTA rig 1.1.0",artifactToken:"fw-m110",groupToken:$g,maxInFlight:1,failureThreshold:1}}')" >/dev/null ||
    fail "createUpdateRollout"
  updq 'mutation($t:String!){startUpdateRollout(token:$t){state targetCount}}' "$(jq -n --arg t "$r" '{t:$t}')" >/dev/null || fail "startUpdateRollout"
  order="$(updq 'query($t:String!){updateRolloutTargets(token:$t,criteria:{pageNumber:1,pageSize:50}){results{deviceToken}}}' "$(jq -n --arg t "$r" '{t:$t}')" |
    jq -r '.updateRolloutTargets.results[].deviceToken' | paste -sd' ')"
  [[ "$(cut -d' ' -f2 <<<"$order")" == "$second" ]] ||
    fail "the rollout froze its targets as '$order', so $second is not the second: the rig's fault is on the wrong device"
  note "rollout: dispatch order $order; fault planted before the start on $second (slot $slot)"
  deadline=$((SECONDS + 600))
  while ((SECONDS < deadline)); do
    st="$(updq 'query($t:String!){updateRollout(token:$t){state}}' "$(jq -n --arg t "$r" '{t:$t}')" | jq -r .updateRollout.state)"
    [[ "$st" == PAUSED || "$st" == COMPLETED ]] && break
    sleep 3
  done
  [[ "$st" == PAUSED ]] || fail "the rollout ended $st, expected PAUSED at the failure threshold"
  expect_green "M7 paused" check_rollout "$r" PAUSED 1 1 2 FAILURE_THRESHOLD
  updq 'query($t:String!){updateRolloutTargets(token:$t,criteria:{pageNumber:1,pageSize:50}){results{deviceToken outcome}}}' "$(jq -n --arg t "$r" '{t:$t}')" |
    jq -r '.updateRolloutTargets.results[] | "    target \(.deviceToken): \(.outcome)"'
  j="$(updq 'query($d:String!){updateAssignments(criteria:{pageNumber:1,pageSize:5,deviceToken:$d,component:"firmware",currentOnly:true}){results{token attempt{state failure{stage code detail}}}}}' \
    "$(jq -n --arg d "$second" '{d:$d}')")"
  note "platform: $second's attempt: $(jq -c '.updateAssignments.results[0].attempt' <<<"$j")"
  [[ "$(jq -r '.updateAssignments.results[0].attempt.state' <<<"$j")" == FAILED ]] || fail "the faulted device's attempt is not FAILED"
  for dev in $order; do
    n="${dev##*-}"
    id="$(describe_running "$(wait_payload "$n")")"
    note "device $n ($dev): runs $id"
  done
  # Targets 3 and 4 were never dispatched: they must not run 1.1.0.
  read -ra undispatched <<<"$(cut -d' ' -f3- <<<"$order")"
  for dev in "${undispatched[@]}"; do
    n="${dev##*-}"
    [[ "$(jq -r .version <<<"$(describe_running "$(payload_pid "$n")")")" != 1.1.0 ]] || fail "$dev was never dispatched yet runs 1.1.0"
  done
  [[ "$(jq -r .version <<<"$(describe_running "$(payload_pid "${second##*-}")")")" != 1.1.0 ]] || fail "the faulted device runs 1.1.0"
  note "platform+devices: the halt held: the failed device and both undispatched devices are not on 1.1.0"
  expect_red "M7 checker negative control (paused is not completed)" "state counts reason" "*" "wanted COMPLETED" check_rollout "$r" COMPLETED 4 0 0 -

  say "M7: clear the fault, resume with failureThreshold=2: the rest is dispatched and the rollout completes with the failure still reported"
  dx rm -rf "/dev${second##*-}/slots/$(inactive_slot "${second##*-}")/ota-device.part"
  updq 'mutation($t:String!,$f:Int){resumeUpdateRollout(token:$t,failureThreshold:$f){state}}' "$(jq -n --arg t "$r" '{t:$t,f:2}')" >/dev/null || fail "resumeUpdateRollout"
  deadline=$((SECONDS + 600))
  while ((SECONDS < deadline)); do
    st="$(updq 'query($t:String!){updateRollout(token:$t){state}}' "$(jq -n --arg t "$r" '{t:$t}')" | jq -r .updateRollout.state)"
    [[ "$st" == COMPLETED || "$st" == PAUSED ]] && break
    sleep 3
  done
  [[ "$st" == COMPLETED ]] || fail "after the resume the rollout is $st, expected COMPLETED"
  expect_green "M7 completed" check_rollout "$r" COMPLETED 3 1 0 -
  read -ra unaffected <<<"$(cut -d' ' -f1,3- <<<"$order")"
  for dev in "${unaffected[@]}"; do
    n="${dev##*-}"
    id="$(describe_running "$(wait_payload "$n")")"
    [[ "$(jq -r .version <<<"$id")" == 1.1.0 ]] || fail "$dev runs $id after the rollout"
  done
  [[ "$(jq -r .version <<<"$(describe_running "$(payload_pid "${second##*-}")")")" != 1.1.0 ]] || fail "the device that failed runs 1.1.0 though its attempt is FAILED"
  note "devices: the three that succeeded run 1.1.0; the failed one still does not"
  expect_red "M7 checker negative control (completed is not paused)" "state reason" "counts" "wanted PAUSED" check_rollout "$r" PAUSED 3 1 0 FAILURE_THRESHOLD
  red "M7 -> PAUSED at the threshold with 2 undispatched and unchanged; after the resume COMPLETED 3 succeeded / 1 failed"
}

# mx_want <case>: DC_OTA_RIG_CASES ("m3 m7") narrows a run to those cases while one is being
# worked on; unset, every case runs, and the evidence for a release is always a full run.
mx_want() { [[ -z "${DC_OTA_RIG_CASES:-}" || " $DC_OTA_RIG_CASES " == *" $1 "* ]]; }

step8_matrix() {
  local c
  offline_controls_resume
  build_matrix_images
  for c in m1_bad_signature m2_hash_mismatch_at_upload m3_offline_midflight m4_offline_before_assign m5_cancellation m6_timeout m7_partial_rollout; do
    if mx_want "${c%%_*}"; then "$c"; fi
  done
}

# cmd_matrix runs the failure matrix alone, on a fresh tenant with four devices on 1.0.0.
cmd_matrix() {
  need kubectl; need docker; need jq; need curl; need openssl; need go; need helm
  cluster_exists || fail "kind cluster $cluster does not exist; run 'hack/ota-rig.sh up'"
  k get ns "$namespace" >/dev/null 2>&1 || fail "instance $instance is not on $cluster; run 'hack/ota-rig.sh up'"
  run_id="$(date -u +%H%M%S)"
  tenant="${DC_OTA_RIG_TENANT:-ota$run_id}"
  member_email="ota-rig@$tenant.test"
  say "matrix $run_id: tenant $tenant on instance $instance (cluster $cluster)"
  note "git HEAD $(git -C "$repo_root" rev-parse HEAD); $(git -C "$repo_root" status --porcelain | wc -l | tr -d ' ') dirty path(s)"
  note "hack/ota-rig.sh blob $(git -C "$repo_root" hash-object hack/ota-rig.sh)"
  note "update-management image: $(k -n "$namespace" get deploy update-management -o jsonpath='{.spec.template.spec.containers[0].image}')"
  discover
  step1_download_base
  step2_provision
  build_images
  start_container
  local n r0
  for n in 1 2 3 4; do create_device "$n"; done
  r0="$(reports_total)"
  for n in 1 2 3 4; do start_device "$n"; done
  for n in 1 2 3 4; do wait_payload "$n" >/dev/null; done
  wait_heard "$r0" 4
  step8_matrix
  say "MATRIX GREEN: M1-M7 each showed the platform's state and the device's, and each checker was shown red against a state it must refuse."
}

# cmd_noobject checks what an operator who evaluates the product on an instance built with
# `--object-store none` actually meets. Point it at such an instance (DC_OTA_RIG_INSTANCE, on the
# rig's cluster), made with `dcctl bootstrap local <name> --object-store none ...`. The service
# must run and say, in the API and not only in the install output, that it has no store, and an
# attempt to declare an artifact must be refused with a code that names the cause.
cmd_noobject() {
  need kubectl; need jq; need curl; need openssl
  cluster_exists || fail "kind cluster $cluster does not exist"
  k get ns "$namespace" >/dev/null 2>&1 || fail "instance $instance is not on $cluster"
  run_id="$(date -u +%H%M%S)"
  tenant="${DC_OTA_RIG_TENANT:-ota$run_id}"
  member_email="ota-rig@$tenant.test"
  say "no object store: tenant $tenant on instance $instance (cluster $cluster)"
  discover
  step2_provision
  local info out
  info="$(updq '{updateManagementInfo{objectStoreConfigured downloadsConfigured}}')" || fail "updateManagementInfo"
  note "platform: updateManagementInfo $(jq -c .updateManagementInfo <<<"$info")"
  [[ "$(jq -r .updateManagementInfo.objectStoreConfigured <<<"$info")" == false ]] ||
    fail "this instance reports an object store; point DC_OTA_RIG_INSTANCE at one bootstrapped with --object-store none"
  if out="$(updq 'mutation($r:ArtifactCreateRequest!){createArtifact(request:$r){token state}}' \
    "$(jq -n --arg p "$profile" '{r:{token:"fw-noobj",kind:"FIRMWARE",name:"ota-device",version:"1.0.1",component:"firmware",sizeBytes:"1024",
      digest:"sha256:0000000000000000000000000000000000000000000000000000000000000000",mediaType:"application/octet-stream",compatibleProfiles:[$p]}}')" 2>&1)"; then
    fail "createArtifact SUCCEEDED on an instance with no object store: $out"
  fi
  note "platform: createArtifact refused: $(head -c 400 <<<"$out")"
  [[ "$out" == *OBJECT_STORE_NOT_CONFIGURED* ]] || fail "the refusal does not carry the code OBJECT_STORE_NOT_CONFIGURED: $out"
  # Everything that needs no content still works: the service is not broken, it has no store.
  updq '{artifacts(criteria:{pageNumber:1,pageSize:5}){results{token}}}' >/dev/null || fail "listing artifacts failed on the storeless instance"
  pass "no object store: the service runs, says objectStoreConfigured=false, and refuses artifacts with OBJECT_STORE_NOT_CONFIGURED"
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
  # What was tested, recorded with the result: the tree, this script, and the image actually running.
  local image want_tag
  image="$(k -n "$namespace" get deploy update-management -o jsonpath='{.spec.template.spec.containers[0].image}')"
  note "git HEAD $(git -C "$repo_root" rev-parse HEAD); $(git -C "$repo_root" status --porcelain | wc -l | tr -d ' ') dirty path(s)"
  note "hack/ota-rig.sh blob $(git -C "$repo_root" hash-object hack/ota-rig.sh)"
  note "update-management image: $image"
  [[ -f "$state_dir/image-tag" ]] || fail "$state_dir/image-tag is missing: this instance was not brought up by this rig's 'up'"
  want_tag="$(tr -d '\r\n' <"$state_dir/image-tag")"
  [[ "${image##*:}" == "$want_tag" ]] ||
    fail "update-management runs tag '${image##*:}', but 'up' built '$want_tag': this is not the build under test"
  offline_controls
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
  step8_matrix
  say "OTA RIG GREEN: a separately built device took 1.0.1 over the instance's broker and ingress,
proved by three discriminating sources (the platform's UPDATED, the confirmed slot's hash, the
running binary's describe) and three liveness signals (PID, bootId, applied reports). Every
planted defect (N1-N7) turned its verdict red for the reason it was planted, while the device
kept running what it ran before; the four-device rollout completed 4/4 and was serial by its
own timestamps; and the failure matrix (M1-M7: bad signature, bad hash, offline mid-update and
before assignment, cancellation, timeout, a rollout paused by one failed device) held."
}

rig_cmd="${1:-}"
case "$rig_cmd" in
  up) cmd_up ;;
  run) cmd_run ;;
  all) cmd_up; cmd_run ;;
  down) cmd_down ;;
  matrix) cmd_matrix ;;
  noobject) cmd_noobject ;;
  controls) offline_controls; offline_controls_resume ;;
  *) fail "usage: hack/ota-rig.sh up | run | matrix | noobject | all | down | controls" ;;
esac
