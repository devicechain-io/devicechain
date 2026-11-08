#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Many variables below are read only by hack/conformance/steps.sh, which this script sources.
# shellcheck disable=SC2034
#
# The transport CONFORMANCE rig: third-party clients drive a real instance, and the results are what
# docs/docs/reference/transport-matrix.md renders its cells from.
#
# WHAT THIS GATES
#
# Every filled circle on the transport matrix is a claim about behaviour a customer's device will meet.
# The platform's own tests drive the platform with code the platform's authors wrote, so an encoder and
# a decoder that agree with each other and with nobody else pass. This rig drives a default-profile
# instance, built from this tree, with clients somebody else wrote:
#
#   Eclipse Paho (Java, MQTT 3.1.1)   QoS 0 and QoS 1 publish; a command subscription
#   curl                              HTTP ingest: 202, 429, and 503 with Retry-After
#   Eclipse Leshan (LwM2M)            register, observe, read, write; and a LwM2M 1.0 TLV-only client
#   Eclipse Tahu (Sparkplug B)        a node's birth and data, through an operator-owned broker
#
# Each check is judged by STORE STATE and the platform's COUNTERS, never by what the client says: a
# client that lies or no-ops cannot manufacture a pass. Checks are PURE functions of a record (see
# CHECKERS), so a control can hand one a record that must not pass and require it to go red.
#
# THE NEGATIVE CONTROLS. A check is worth nothing until it has been shown to fail, so every check has a
# control and the suite does not count until every control has failed the check it names:
#
#   N1  a dropped command subscription: the device never subscribes, and the command check must go red
#   N2  a decoder off-by-one: a Sparkplug NDATA whose alias is one past the birth's, so the platform's
#       decoder attributes the value to the neighbouring metric, and the data check must go red
#   N3  LwM2M TLV-only data: a LwM2M 1.0 client that cannot produce SenML. The telemetry check must go
#       red AND the limit check ("a 1.0-only client yields no telemetry") must be green: that pair is
#       what keeps the matrix cell the honest half-filled circle
#   R*  for the rest, the checker is shown red on a fabricated record of the opposite verdict, first,
#       offline, before the cluster is touched (`controls`)
#
# THE RESULT. `run` writes hack/conformance/results.json: the commit SHA of the tree under test, the
# time, every check's verdict and every control's. backend/tools/transportmatrix renders the matrix
# cells from it and REFUSES a file that is stale, was made on a dirty tree, or whose controls did not all
# go red. See hack/conformance/README.md.
#
#   hack/conformance-rig.sh up        create the kind cluster dc-conformance (own kubeconfig, own port),
#                                     an operator-owned MQTT broker for Sparkplug, and a default
#                                     instance from images built from THIS tree
#   hack/conformance-rig.sh run       the steps below; writes the results file
#   hack/conformance-rig.sh all       up + run
#   hack/conformance-rig.sh controls  the offline checker controls only (no cluster)
#   hack/conformance-rig.sh down      remove the cluster, the instance, and dcctl's local state for it
#
# ISOLATION. The cluster has its own kubeconfig (under DC_CONF_RIG_STATE) and every kubectl, helm, kind
# and dcctl call is given it; this rig never reads or changes another kubeconfig's current context.
# `down` deletes only this rig's cluster and containers. It never runs a host-wide docker kill, stop or
# prune.
#
# Requires: kind, kubectl, helm, docker, jq, openssl, curl, ko, `tofu` OR `terraform`, a Go toolchain.
# The clients build in containers (Maven), so no JDK is needed on the host.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="${DC_CONF_RIG_CLUSTER:-dc-conformance}"
kube_context="kind-$cluster"
instance="${DC_CONF_RIG_INSTANCE:-confrig}"
namespace="dci-$instance"
host="${DC_CONF_RIG_HOST:-$instance.devicechain.test}"
https_port="${DC_CONF_RIG_HTTPS_PORT:-18643}"
state_dir="${DC_CONF_RIG_STATE:-$repo_root/.conformance-rig}"
min_free_gb="${DC_CONF_RIG_MIN_FREE_GB:-60}"
tenant="${DC_CONF_RIG_TENANT:-conf}"
member_email="conformance@$tenant.test"
profile="conformance-profile"
dtype="conformance-type"
mqtt_node_port=31883
broker_node_port=31884 # the operator-owned broker (hack/conformance/mosquitto.yaml)
http_node_port=31881   # a NodePort the rig adds in front of event-sources' plain HTTP port
lwm2m_node_port=31684  # a NodePort the rig adds in front of lwm2m-ingest's CoAPS port
# The ingest stream ceiling. Small on purpose: HTTP's 503 is the platform refusing events when the
# consumer's unread backlog reaches 90% of what the stream holds, and the check has to reach that. It
# holds only with the streams' time-based sizing off (streamDesignRate 0 in the values below).
stream_max_msgs="${DC_CONF_RIG_STREAM_MAX_MSGS:-300}"
results_out="${DC_CONF_RIG_RESULTS:-$repo_root/hack/conformance/results.json}"
# Pinned by digest. Bump by hand.
mosquitto_image="eclipse-mosquitto:2@sha256:860aeb5d63ce509508f6dcfb2bc8a1c1dd2bc60555b562b45ce90e52d8dabb0f"
curl_image="${DC_CONF_RIG_CURL_IMAGE:-curlimages/curl:8.10.1}"
client_image="dc-conformance-client:local"
leshan_image="dc-conformance-leshan:local"
exe="$(go env GOEXE)"
dcctl="$repo_root/backend/cli/build/dcctl$exe"

winpath() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }
mkdir -p "$state_dir"
kubeconfig="$(winpath "$state_dir/kubeconfig")"
export KUBECONFIG="$kubeconfig"

# NEVER the shared developer cluster: `down` deletes the cluster it is pointed at.
[[ "$cluster" != devicechain ]] || { echo "refusing: DC_CONF_RIG_CLUSTER=devicechain is the shared developer cluster" >&2; exit 2; }

run_log="$state_dir/run-$(date -u +%Y%m%dT%H%M%SZ).log"
exec > >(tee -a "$run_log") 2>&1

rig_tmp="$(mktemp -d "${TMPDIR:-/tmp}/dc-conf-rig.XXXXXX")"
work="$rig_tmp/work"
mkdir -p "$work"
dm_scaled_from=""
cleanup() {
  local rc=$?
  # A run that stopped while the platform was held in backpressure (check H3) must not leave it so.
  if [[ -n "$dm_scaled_from" ]]; then
    echo "cleanup: restoring device-management to $dm_scaled_from replica(s)"
    kubectl --kubeconfig "$kubeconfig" --context "$kube_context" -n "$namespace" scale deploy device-management --replicas "$dm_scaled_from" >/dev/null 2>&1 ||
      echo "cleanup: could not restore device-management"
  fi
  if [[ "${DC_CONF_RIG_KEEP_WORK:-}" == 1 ]]; then
    echo "conformance-rig: kept $rig_tmp (DC_CONF_RIG_KEEP_WORK=1); it holds throwaway keys" >&2
  else
    rm -rf "$rig_tmp"
  fi
  printf 'conformance-rig: %s exited with status %d\n' "${rig_cmd:-?}" "$rc" >>"$run_log"
}
trap cleanup EXIT

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
pass() { printf '\033[1;32m    PASS %s\033[0m\n' "$*"; }
red() { printf '\033[1;33m    RED (as designed) %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }
warn() { printf '\n\033[1;33mWARN: %s\033[0m\n' "$*" >&2; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }
# openssl on Windows ends its output with CRLF, and stripping only the newline keeps the CR: a secret
# with a stray carriage return in it is a different secret, and Paho refuses to send one as an MQTT
# password ("Invalid UTF-8 char: [d]").
rand_hex() { openssl rand -hex "$1" | tr -d '\r\n'; }
rand_b64() { openssl rand -base64 "$1" | tr -d '\r\n'; }
redact() { sed -u -E 's/^([[:space:]]*password:[[:space:]]*).+$/\1[redacted]/'; }

# jq on Windows writes CRLF unless told not to, and "true\r" is not "true". -b is that switch.
if command jq -b -n 1 >/dev/null 2>&1; then jq() { command jq -b "$@"; }; fi

k() { kubectl --kubeconfig "$kubeconfig" --context "$kube_context" "$@"; }
dk() { MSYS_NO_PATHCONV=1 docker "$@"; }

[[ "$instance" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || fail "DC_CONF_RIG_INSTANCE must be a DNS label, got '$instance'"

# ---------------------------------------------------------------------------
# up / down
# ---------------------------------------------------------------------------

build_dcctl() {
  say "building dcctl"
  make -C "$repo_root/backend/cli" build >/dev/null
  [[ -x "$dcctl" ]] || fail "dcctl was not built at $dcctl"
}

disk_guard() {
  local free
  free="$(df -Pk "$HOME" | awk 'NR==2 {printf "%d", $4/1024/1024}')"
  [[ "$free" -ge "$min_free_gb" ]] ||
    fail "only ${free} GB free on the disk holding $HOME; this rig needs at least $min_free_gb (DC_CONF_RIG_MIN_FREE_GB)"
  note "disk: ${free} GB free (floor $min_free_gb)"
}

cluster_exists() { grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; }

create_cluster() {
  if cluster_exists; then
    k get ns kube-system >/dev/null 2>&1 ||
      fail "kind cluster $cluster exists but is not in $kubeconfig; it was not made by this rig (pick DC_CONF_RIG_CLUSTER)"
    note "kind cluster $cluster already exists; reusing it"
    return
  fi
  say "creating kind cluster $cluster (kubeconfig $kubeconfig, https on 127.0.0.1:$https_port)"
  cat >"$rig_tmp/kind.yaml" <<EOK
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
EOK
  kind create cluster --name "$cluster" --kubeconfig "$kubeconfig" --config "$(winpath "$rig_tmp/kind.yaml")" --wait 120s
}

# The identities the LwM2M service authenticates: one for the SenML client (observe, read, write), one
# for the LwM2M 1.0 TLV-only client (N3). Both auto-register under the rig's tenant and device type, which
# the run step creates afterwards. The PSKs are generated here and live in the state directory and in the
# Secret dcctl writes; they authenticate nothing but this rig's instance.
write_lwm2m_identities() {
  local f="$1" psk_a psk_b
  psk_a="$(rand_b64 24)"
  psk_b="$(rand_b64 24)"
  jq -n --arg ta "$tenant" --arg ty "$dtype" --arg pa "$psk_a" --arg pb "$psk_b" '[
    {identity:"conf-lwm2m-a", psk:$pa, tenant:$ta, externalId:"conf-lwm2m-a", deviceTypeToken:$ty, autoRegister:true},
    {identity:"conf-lwm2m-tlv", psk:$pb, tenant:$ta, externalId:"conf-lwm2m-tlv", deviceTypeToken:$ty, autoRegister:true}]' >"$f"
}

write_sparkplug_source() {
  jq -n --arg t "$tenant" --arg ty "$dtype" \
    --arg u "tcp://mosquitto.conformance-broker.svc.cluster.local:1883" '[
    {tenant:$t, hostId:"conformance-host", broker:{url:$u}, groups:["conf"], deviceTypeToken:$ty, autoRegister:true}]' >"$1"
}

cmd_up() {
  need kind; need kubectl; need docker; need jq; need ko; need go; need openssl
  disk_guard
  create_cluster
  if k get ns "$namespace" >/dev/null 2>&1; then
    note "instance $instance already exists on $cluster; reusing it"
    return
  fi
  [[ ! -e "${HOME:?}/.devicechain/instances/$instance" ]] ||
    fail "$HOME/.devicechain/instances/$instance exists but the instance is not on $cluster: it belongs to something else; pick DC_CONF_RIG_INSTANCE"
  say "the operator-owned MQTT broker for Sparkplug"
  sed "s|@IMAGE@|$mosquitto_image|" "$repo_root/hack/conformance/mosquitto.yaml" | k apply -f - >/dev/null
  k -n conformance-broker rollout status deploy/mosquitto --timeout=180s
  build_dcctl
  local tag
  tag="cf-$(git -C "$repo_root" rev-parse --short=8 HEAD)-$(date -u +%Y%m%dT%H%M%SZ)"
  printf '%s\n' "$tag" >"$state_dir/image-tag"
  say "installing the cluster prerequisites (operator image $tag built from this tree)"
  "$dcctl" install local --yes --cluster "$cluster" --no-monitoring --build --version "$tag" 2>&1 | redact

  write_lwm2m_identities "$state_dir/lwm2m-identities.json"
  write_sparkplug_source "$state_dir/sparkplug-source.json"
  cat >"$state_dir/values.yaml" <<EOV
instance:
  config:
    infrastructure:
      nats:
        streamMaxMsgs: $stream_max_msgs
        # dcctl sizes the event streams by time (design rate x runway), and a stream sized by time
        # has its message ceiling raised to what the runway holds: 900,000 on the default profile,
        # which no check reaches. A design rate of 0 turns that sizing off, so the ceiling above is
        # the one the inbound stream is created with.
        streamDesignRate: 0
EOV
  say "bootstrapping $instance on the DEFAULT profile, with lwm2m-ingest and sparkplug-ingest added"
  # A fresh bootstrap given --values records the overlay on the declaration with a compare-and-swap of
  # a read taken just after declaring it, and the operator's first status write on the new declaration
  # can land in between: dcctl then stops with "changed while the values overlay was being recorded".
  # The declaration is in place by then and a second bootstrap carries on from it, so the rig tries once
  # more for exactly that message and for nothing else.
  local attempt rc
  for attempt in 1 2; do
    rc=0
    "$dcctl" bootstrap local "$instance" --yes --no-escrow --cluster "$cluster" --host "$host" --build --version "$tag" \
      --lwm2m-identities "$(winpath "$state_dir/lwm2m-identities.json")" \
      --sparkplug-source "$(winpath "$state_dir/sparkplug-source.json")" \
      --values "$(winpath "$state_dir/values.yaml")" 2>&1 | redact | tee "$rig_tmp/bootstrap.log" || rc=$?
    ((rc != 0)) || break
    if ((attempt == 1)) && grep -q 'changed while the values overlay was being recorded' "$rig_tmp/bootstrap.log"; then
      warn "dcctl bootstrap raced the operator's first status write while recording the values overlay; bootstrapping again"
      continue
    fi
    fail "dcctl bootstrap exited with status $rc"
  done
  say "UP COMPLETE: run 'hack/conformance-rig.sh run'"
}

remove_cluster_state() {
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
  local uid="" rec="${HOME:?}/.devicechain/instances/$instance/instance.json" recctx
  if cluster_exists; then
    uid="$(k get ns kube-system -o jsonpath='{.metadata.uid}' 2>/dev/null)" ||
      fail "kind cluster $cluster exists but $kubeconfig does not reach it: it is not this rig's; refusing to delete it"
    [[ -n "$uid" ]] || fail "cannot read the kube-system UID of $cluster through $kubeconfig; refusing to delete it"
    if [[ -f "$rec" ]]; then
      recctx="$(jq -r '.kubeContext // empty' "$rec")"
      [[ "$recctx" == "$kube_context" ]] ||
        fail "$rec names context '$recctx', not $kube_context: instance $instance is not this rig's; refusing to destroy it"
      [[ -x "$dcctl" ]] || build_dcctl
      say "destroying instance $instance"
      "$dcctl" destroy local "$instance" --yes 2>&1 | redact || warn "dcctl destroy exited non-zero; deleting the cluster anyway"
    fi
    say "deleting kind cluster $cluster"
    kind delete cluster --name "$cluster" --kubeconfig "$kubeconfig"
  fi
  remove_cluster_state "$uid"
  rm -f "$state_dir/kubeconfig" "$state_dir/image-tag"
}

# The checkers, controls and steps live next to the fixtures they use.
# shellcheck source=conformance/steps.sh
source "$repo_root/hack/conformance/steps.sh"

rig_cmd="${1:-}"
case "$rig_cmd" in
  up) cmd_up ;;
  run) cmd_run ;;
  all) cmd_up; cmd_run ;;
  controls) offline_controls ;;
  down) cmd_down ;;
  *) fail "usage: hack/conformance-rig.sh up | run | all | controls | down" ;;
esac
