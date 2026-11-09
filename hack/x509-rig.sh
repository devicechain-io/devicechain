#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The X.509 (mTLS) device-authentication rig: certificate devices take a real instance's broker,
# end to end, ten planted defects are refused, and the OCSP check meets a live responder (N11-N17).
#
# WHAT THIS GATES
#
# Chain-validated client-certificate authentication. Every piece is CI-tested on its own: the
# chain verifier (core/x509trust), the auth callout against an embedded broker, the registration
# model on Postgres, the simulator's minting and connecting. What no test can show is the CLAIM
# that on a default instance, built from this tree, a certificate device is admitted by the
# DeviceChain broker image the chart actually runs, over the real MQTT node port, and that every
# way of presenting a certificate that must NOT be admitted is refused by the platform's own
# counters AND by the device failing to connect. That needs a cluster, so it is a manual rig, like
# hack/ota-rig.sh, hack/ha-rig.sh and hack/dr-rig.sh, and it follows their one rule: a check is
# worth nothing until it has been shown to fail. Nine negative controls (N1-N9) plant a defect
# each; every one must turn the platform's verdict red AND leave the device out. N10 is the
# issuer pin (below), which is judged by the platform's gauges and counters rather than by a device.
#
#   hack/x509-rig.sh up      create the kind cluster dc-x509rig (its own kubeconfig, its own host
#                            port), install, and bootstrap an instance (default profile) from
#                            images built from THIS tree under a unique tag
#   hack/x509-rig.sh run     the steps below
#   hack/x509-rig.sh all     up + run
#   hack/x509-rig.sh down    remove the device container, destroy the instance, delete the cluster
#                            and dcctl's local state for it
#
# THE STEPS `run` TAKES (each prints a PASS line naming its evidence):
#
#   1  the broker is OUR image and requests client certificates: dc_broker_info exists on every
#      broker pod, dc_broker_mqtt_tls and dc_broker_mqtt_client_auth_request both read 1
#   2  tenant (with a small registration budget), device type, and a TOKEN device that publishes
#      telemetry and has it persisted. The token device is re-proven after every later phase
#   3  PINNED: the simulator mints a CA, registers it as an anchor, creates N devices with pinned
#      credentials; they connect, publish telemetry through the same certificates, and the events
#      persist. callout_x509_total{result="ok"} rises by N
#   4  JIT: a certificate profile (ALLOW_NEW) on the anchor; M never-registered certificates
#      connect and exactly M devices are created, each with the token the CN derives
#   5  NEGATIVE CONTROLS, each judged by the platform's counter (callout_x509_total{result}), by
#      the device being refused at CONNACK, by the store (no event persisted, no device created)
#      and, for sessions, by the broker's own connection list:
#        N1  a certificate from a different CA, with the CN (so the client id) of a real device
#        N2  an expired certificate (a live session ends AT the expiry; a reconnect is refused)
#        N3  a certificate with no clientAuth extended key usage (its twin WITH it registers)
#        N4  a revoked (disabled) credential, connecting immediately after the disable
#        N5  an anchor disabled while its sessions are live: the broker ends them
#        N6  a client id that does not match the derived token (pinned, and unregistered)
#        N7  a name-constrained intermediate (its unconstrained twin registers; the constrained
#            one is also refused as an anchor)
#        N8  JIT under a disabled profile (re-enabled, the SAME certificate registers)
#        N9  the tenant's registration budget exceeded: jit_rate_limited is counted, the refused
#            devices do not exist
#        N10 the issuer pin (6): every certificate connect above was admitted by a callout that
#            pins requests to the broker's live server ids, learned over the system account.
#            The pin must hold the live broker servers, its system connection must be up, and
#            no genuine request may have been counted issuer_unpinned or issuer_pin_unavailable
#            (issuer_pin_pending, a request that met a broker pod before the pin had learned it, is
#            reported but tolerated: the device retries).
#            The checker is shown red on a pin that holds no servers, one whose system connection
#            is down, and one that refused the broker's own requests. A request FORGED onto the
#            callout subject is not driven from here: the broker's publish-deny refuses it before
#            the pin sees it (and that, with the pin refusing the same request when the deny is
#            absent, is held by the device-management tests on an embedded broker with its real
#            permissions).
#   7  OCSP, against a LIVE responder (backend/tools/ocspresponder, in a container of this rig's
#      own). It runs in a tenant of its own, so the registration budget N9 spent is not its
#      concern. The responder answers for an openssl CA the rig registers as an anchor with a
#      certificate profile; every device below is a never-seen certificate that registers on its
#      first connect, so "admitted" means a device was created and its telemetry stored, and
#      "refused" means none was. The responder sits on a docker network with a NON-private
#      subnet the kind node joins (DC_X509_RIG_OCSP_SUBNET): the egress boundary refuses every
#      private address, so a responder on the kind network would be refused before it was asked
#      (which is itself N17). First the instrument: OpenSSL's own client asks the responder from
#      the host and must read good, revoked and unknown off /ok, a verify failure off /wrongkey
#      and invalid status times off /stale, so the planted defects are what they claim to be
#      before the platform meets them. Then, the documented way:
#        - with the instance switch off, the API refuses to switch the anchor on (N16), and a
#          device under the anchor (mode OFF) is admitted with NO request reaching the responder
#          (the OCSP trace checker is shown red on [asked] by that very record: it can tell a
#          device the responder was asked about from one it never saw)
#        - `dcctl upgrade --values` turns on deviceManagement.callout.ocsp.enabled (and
#          allowRequireCurrent); the device-management log must say OCSP is checked
#        - the anchor goes to ENFORCE_KNOWN with its ocspResponderUrl, later REQUIRE_CURRENT
#      What the documentation says, and therefore what each case must show (callout_x509_total,
#      x509_ocsp_unavailable_allowed_total{reason} and the responder's own request log):
#        GOOD         admitted in both modes; the responder was asked; no "admitted unchecked"
#                     reason moved
#        N11 REVOKED  refused in both modes (ocsp_revoked); the responder was asked
#        N12 UNKNOWN  "no answer": ENFORCE_KNOWN admits, counting unknown_cert; REQUIRE_CURRENT
#                     refuses (ocsp_unavailable)
#        N13 WRONG KEY  a good answer signed by an embedded delegated responder that a FAKE CA
#                     issued, the fake CA carrying the real CA's exact subject DN under its own
#                     key: every by-name check passes, only the delegate's signature against the
#                     real CA's key fails. Unverifiable: ENFORCE_KNOWN admits counting unverified;
#                     REQUIRE_CURRENT refuses. A forged GOOD must never admit there
#        N14 STALE    a good answer whose nextUpdate passed before it was sent: unverifiable, so
#                     the same split (unverified)
#        N15 DOWN     the responder stopped: ENFORCE_KNOWN admits counting unreachable (fail
#                     open, by design, and counted); REQUIRE_CURRENT refuses (fail closed). Then
#                     the responder comes back and a good device is admitted again
#        N16 SWITCH   the API refuses ENFORCE_KNOWN while the instance switch is off (above)
#        N17 PRIVATE  the anchor's URL pointed at the responder's PRIVATE (kind network) address:
#                     the egress boundary refuses the dial, ENFORCE_KNOWN admits counting
#                     blocked, and the responder sees nothing
#      Every refusal is shown red through the admit checker on the same record, and every
#      admitted no-answer case is shown red through the GOOD device's OCSP trace checker
#      ([allowed]), so the check that guards each case is seen to fail on its defect.
#
# RELEASED IMAGES. DC_X509_RIG_RELEASE=v1.3.0 runs the rig against a published release instead of
# this tree: `up` downloads that release's dcctl (for this host) and its linux/amd64 dc-simulator
# from the GitHub release, checks both against the release's checksums, and installs and
# bootstraps with them (no --build), so every service image is the published one. The OCSP
# responder is the only thing still built from this tree.
#
#   hack/x509-rig.sh ocsp    the checkers offline, steps 1 and 2, then step 7 alone
#
# THE CHECKERS (see "CHECKERS"). Each attempt is a RECORD (what the broker answered, the counters
# before and after, whether the event persisted, whether the device exists); the checkers are pure
# functions of a record. check_admitted is shown to go RED on a refused attempt, and check_refused
# on an admitted one, with the tags that say WHICH source disagreed; and both are shown offline,
# first, on fabricated records, so a checker that cannot go red cannot reach the cluster.
#
# EVIDENCE. `run` prints the git HEAD, the number of dirty paths, `git hash-object` of this script
# and the images actually running, and refuses a device-management image whose tag is not the one
# `up` recorded. Every invocation writes .x509-rig/run-<UTC timestamp>.log with its real exit
# status last.
#
# THE DEVICES RUN IN A CONTAINER. The mosquitto image (pinned by digest) carries mosquitto_pub, and
# the simulator is copied into it. It sits on the kind network, so it reaches the broker's MQTT
# node port and the ingress by the instance host, exactly as a device outside the cluster does. The
# rig's own GraphQL calls go through a loopback-only host port (DC_X509_RIG_HTTPS_PORT).
#
# ISOLATION. The cluster has its own kubeconfig file (under DC_X509_RIG_STATE) and every kubectl,
# helm, kind and dcctl call is given it explicitly; this rig never reads or changes another
# kubeconfig's current context. `down` deletes only this rig's cluster and container. It never
# runs a host-wide docker kill, stop or prune.
#
# Requires: kind, kubectl, helm, docker, jq, openssl (3.x), curl, ko, `tofu` OR `terraform`, and a
# Go toolchain. It pushes images under a unique tag (prefix `x5-`) to the local registry at
# localhost:5000 (dcctl's developer path); it never removes anything from it. See hack/ota-rig.sh
# for pruning only this rig's tags from that shared registry.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cluster="${DC_X509_RIG_CLUSTER:-dc-x509rig}"
kube_context="kind-$cluster"
instance="${DC_X509_RIG_INSTANCE:-x509rig}"
namespace="dci-$instance"
host="${DC_X509_RIG_HOST:-$instance.devicechain.test}"
https_port="${DC_X509_RIG_HTTPS_PORT:-18543}"
state_dir="${DC_X509_RIG_STATE:-$repo_root/.x509-rig}"
device_container="${DC_X509_RIG_DEVICE_CONTAINER:-$cluster-devices}"
device_image="${DC_X509_RIG_DEVICE_IMAGE:-eclipse-mosquitto:2@sha256:860aeb5d63ce509508f6dcfb2bc8a1c1dd2bc60555b562b45ce90e52d8dabb0f}"
min_free_gb="${DC_X509_RIG_MIN_FREE_GB:-100}"
n_pinned="${DC_X509_RIG_PINNED:-5}"
m_jit="${DC_X509_RIG_JIT:-4}"
# The tenant's registration budget: this many in a burst, refilled at a rate too small to matter
# over a run. N9 spends it; every registration the rig makes before then is counted against it.
jit_burst="${DC_X509_RIG_JIT_BURST:-10}"
tenant="${DC_X509_RIG_TENANT:-x509rig}"
member_email="x509-rig@$tenant.test"
profile="x509-rig-profile"
dtype="x509-rig-type"
mqtt_node_port=31883
exe="$(go env GOEXE)"
dcctl="$repo_root/backend/cli/build/dcctl$exe"
# A published release to run against instead of this tree (see RELEASED IMAGES), e.g. v1.3.0.
release="${DC_X509_RIG_RELEASE:-}"
[[ -z "$release" || "$release" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || { echo "DC_X509_RIG_RELEASE must be a release tag like v1.3.0, got '$release'" >&2; exit 2; }
# Step 7 (OCSP). DC_X509_RIG_OCSP=0 skips it in `run`.
ocsp_on="${DC_X509_RIG_OCSP:-1}"
ocsp_container="${DC_X509_RIG_OCSP_CONTAINER:-$cluster-ocsp}"
ocsp_net="${DC_X509_RIG_OCSP_NET:-$cluster-ocsp-pub}"
# NOT a private range, on purpose: the egress boundary refuses every private address before it
# dials, and a real CA's responder is on the public internet. The range is routed only inside
# this docker network (the kind node and the responder). On Docker Desktop the host never routes
# it; on native Linux Docker the host itself gets a connected route for the subnet while the
# network exists (`down` removes it), so pick DC_X509_RIG_OCSP_SUBNET away from anything the host
# must reach.
ocsp_subnet="${DC_X509_RIG_OCSP_SUBNET:-11.255.250.0/24}"
ocsp_ip="${DC_X509_RIG_OCSP_IP:-11.255.250.10}"
ocsp_host_port="${DC_X509_RIG_OCSP_HOST_PORT:-18580}"

# Windows binaries (kind, kubectl, helm, dcctl) are handed paths in the mixed C:/... form, which
# Git Bash's own tools read too. dk and dx set MSYS_NO_PATHCONV so Git Bash does not rewrite the
# in-container paths they are given (/work/...) into Windows ones. It is not exported: make and go
# need the conversion.
winpath() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s' "$1"; fi; }
mkdir -p "$state_dir"
kubeconfig="$(winpath "$state_dir/kubeconfig")"
export KUBECONFIG="$kubeconfig"

# 🔴 NEVER the shared developer cluster. Other lanes build on `devicechain` (and its registry);
# `down` deletes the cluster it is pointed at, so the name is refused before anything runs.
[[ "$cluster" != devicechain ]] || { echo "refusing: DC_X509_RIG_CLUSTER=devicechain is the shared developer cluster" >&2; exit 2; }

# Every invocation keeps its own log, ending in its real exit status (written by the EXIT trap,
# so a `fail` deep inside a step is recorded as the failure it is).
run_log="$state_dir/run-$(date -u +%Y%m%dT%H%M%SZ).log"
exec > >(tee -a "$run_log") 2>&1

rig_tmp="$(mktemp -d "${TMPDIR:-/tmp}/dc-x509-rig.XXXXXX")"
work="$rig_tmp/work"
mkdir -p "$work"
profile_disabled=""
cleanup() {
  local rc=$?
  # N8 holds the certificate profile disabled; a failure while it does must not leave it so.
  if [[ -n "$profile_disabled" ]]; then
    echo "cleanup: re-enabling profile $profile_disabled (N8 did not finish)"
    dmq 'mutation($t:String!){updateProvisioningProfile(token:$t,request:{enabled:true}){token}}' \
      "$(jq -n --arg t "$profile_disabled" '{t:$t}')" >/dev/null 2>&1 || echo "cleanup: could not re-enable it"
  fi
  # Held simulator sessions belong to this rig's container only.
  if docker inspect "$device_container" >/dev/null 2>&1; then
    MSYS_NO_PATHCONV=1 docker exec "$device_container" pkill dc-simulator 2>/dev/null || true
  fi
  # The CA and device private keys live only here. Removed however the run ends, unless
  # DC_X509_RIG_KEEP_WORK=1 keeps them to diagnose a failure by hand (they are throwaway: they
  # are trusted by nothing but this rig's instance).
  if [[ "${DC_X509_RIG_KEEP_WORK:-}" == 1 ]]; then
    echo "x509-rig: kept $rig_tmp (DC_X509_RIG_KEEP_WORK=1); it holds throwaway CA and device keys" >&2
  else
    rm -rf "$rig_tmp"
  fi
  printf 'x509-rig: %s exited with status %d\n' "${rig_cmd:-?}" "$rc" >>"$run_log"
}
trap cleanup EXIT

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
pass() { printf '\033[1;32m    PASS %s\033[0m\n' "$*"; }
red() { printf '\033[1;33m    RED (as designed) %s\033[0m\n' "$*"; }
fail() {
  printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2
  [[ ! -s "${work:-/nonexistent}/sim.err" ]] || { echo '---- simulator log (last 8 lines) ----' >&2; tail -8 "$work/sim.err" >&2; }
  exit 1
}
warn() { printf '\n\033[1;33mWARN: %s\033[0m\n' "$*" >&2; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"; }

# Nothing this rig prints may carry a password.
redact() { sed -u -E 's/^([[:space:]]*password:[[:space:]]*).+$/\1[redacted]/'; }

# jq on Windows writes CRLF unless told not to, and "true\r" is not "true". -b is that switch.
if command jq -b -n 1 >/dev/null 2>&1; then jq() { command jq -b "$@"; }; fi

k() { kubectl --kubeconfig "$kubeconfig" --context "$kube_context" "$@"; }
dx() { MSYS_NO_PATHCONV=1 docker exec "$device_container" "$@"; }
dk() { MSYS_NO_PATHCONV=1 docker "$@"; }

[[ "$instance" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || fail "DC_X509_RIG_INSTANCE must be a DNS label, got '$instance'"

# ---------------------------------------------------------------------------
# up / down
# ---------------------------------------------------------------------------

release_dir="$state_dir/release-$release"
rel_sim=""
host_os="$(go env GOHOSTOS)"
host_arch="$(go env GOHOSTARCH)"
if [[ -n "$release" ]]; then
  dcctl="$release_dir/dcctl$([[ "$host_os" != windows ]] || printf .exe)"
  rel_sim="$release_dir/dc-simulator"
fi
release_fetched=""

# fetch_release puts the release's dcctl for this host and its linux/amd64 simulator in
# $release_dir, once per invocation. Each archive must hash to what the release's own checksum
# files (downloaded afresh every time) name AND carry the release workflow's build provenance
# (`gh attestation verify`); an archive already on disk is held to the same two checks, and the
# binaries are always unpacked again from the archives just verified, into a temporary directory
# that is renamed into place, so a binary left on disk is never trusted on its own.
fetch_release() {
  [[ -z "$release_fetched" ]] || return 0
  need gh
  local v="${release#v}" base f sum want tmp dcctl_archive
  base="https://github.com/devicechain-io/devicechain/releases/download/$release"
  mkdir -p "$release_dir"
  say "fetching and verifying the published dcctl and dc-simulator of $release"
  for f in checksums.txt dc-simulator_checksums.txt; do
    curl -fsSL --max-time 120 "${curl_revoke[@]}" -o "$release_dir/$f" "$base/$f" || fail "could not download $base/$f"
  done
  if [[ "$host_os" == windows ]]; then dcctl_archive="dcctl_${v}_windows_${host_arch}.zip"; else dcctl_archive="dcctl_${v}_${host_os}_${host_arch}.tar.gz"; fi
  for f in "dc-simulator_${v}_linux_amd64.tar.gz" "$dcctl_archive"; do
    want="$(awk -v f="$f" '($2 == f || $2 == "*" f) && w == "" { w = $1 } END { print w }' "$release_dir/checksums.txt" "$release_dir/dc-simulator_checksums.txt")"
    [[ -n "$want" ]] || fail "$f: the release's checksum files do not name it"
    sum=""
    [[ ! -f "$release_dir/$f" ]] || sum="$(sha256sum "$release_dir/$f" | awk '{ print $1 }')"
    if [[ "$sum" != "$want" ]]; then
      curl -fsSL --max-time 600 "${curl_revoke[@]}" -o "$release_dir/$f" "$base/$f" || fail "could not download $base/$f"
      sum="$(sha256sum "$release_dir/$f" | awk '{ print $1 }')"
    fi
    [[ "$sum" == "$want" ]] || fail "$f: sha256 $sum, the release's checksums say '$want'"
    # Bound to the release workflow AT this tag: provenance from another workflow or ref of the same
    # repository is not the release's.
    gh attestation verify "$(winpath "$release_dir/$f")" --repo devicechain-io/devicechain \
      --signer-workflow devicechain-io/devicechain/.github/workflows/release.yml --source-ref "refs/tags/$release" >/dev/null ||
      fail "$f: its hash matches, but gh attestation verify finds no provenance from release.yml at refs/tags/$release for it"
    note "$f: sha256 $sum matches the release's checksums, and its build provenance verifies"
  done
  tmp="$(mktemp -d "$release_dir/unpack.XXXXXX")"
  # Relative names, from inside the directory: GNU tar reads "D:/..." as a remote host.
  (cd "$tmp" && tar xzf "../dc-simulator_${v}_linux_amd64.tar.gz" dc-simulator) || fail "could not unpack the simulator"
  if [[ "$host_os" == windows ]]; then
    (cd "$tmp" && unzip -o -q "../$dcctl_archive" dcctl.exe) || fail "could not unpack dcctl"
  else
    (cd "$tmp" && tar xzf "../$dcctl_archive" dcctl) || fail "could not unpack dcctl"
  fi
  [[ -s "$tmp/dc-simulator" && -s "$tmp/${dcctl##*/}" ]] || fail "the release archives did not hold dc-simulator and ${dcctl##*/}"
  mv -f "$tmp/dc-simulator" "$rel_sim"
  mv -f "$tmp/${dcctl##*/}" "$dcctl"
  rmdir "$tmp"
  chmod +x "$dcctl"
  release_fetched=1
  "$dcctl" version | sed 's/^/    /'
}

# ensure_dcctl: a dcctl to drive the instance with. A release's is verified again (fetch_release);
# this tree's is built only when there is none.
ensure_dcctl() {
  if [[ -n "$release" ]]; then fetch_release; elif [[ ! -x "$dcctl" ]]; then build_dcctl; fi
}

build_dcctl() {
  if [[ -n "$release" ]]; then fetch_release; return; fi
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
    fail "only ${free} GB free on the disk holding $HOME; this rig needs at least $min_free_gb (DC_X509_RIG_MIN_FREE_GB)"
  note "disk: ${free} GB free (floor $min_free_gb)"
}

cluster_exists() { grep -qx -- "$cluster" <<<"$(kind get clusters 2>/dev/null)"; }

create_cluster() {
  if cluster_exists; then
    k get ns kube-system >/dev/null 2>&1 ||
      fail "kind cluster $cluster exists but is not in $kubeconfig; it was not made by this rig (pick DC_X509_RIG_CLUSTER)"
    note "kind cluster $cluster already exists; reusing it"
    return
  fi
  say "creating kind cluster $cluster (kubeconfig $kubeconfig, https on 127.0.0.1:$https_port)"
  # deploy/local/kind-cluster.yaml, less its host ports 80 and 1883 and with 443 moved to a
  # loopback port of this rig's own: those host ports belong to whichever cluster took them first.
  # The devices reach the node directly on the kind network and need none of them.
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
    fail "$HOME/.devicechain/instances/$instance exists but the instance is not on $cluster: it belongs to something else; pick DC_X509_RIG_INSTANCE"
  build_dcctl
  if [[ -n "$release" ]]; then
    printf '%s\n' "$release" >"$state_dir/image-tag"
    say "installing the cluster prerequisites from the PUBLISHED release $release"
    "$dcctl" install local --yes --cluster "$cluster" --no-monitoring --version "$release" 2>&1 | redact
    say "bootstrapping $instance on the DEFAULT profile from the published $release images"
    "$dcctl" bootstrap local "$instance" --yes --no-escrow --cluster "$cluster" --host "$host" --version "$release" 2>&1 | redact
    say "UP COMPLETE: run 'DC_X509_RIG_RELEASE=$release hack/x509-rig.sh run'"
    return
  fi
  # A unique tag: the chart pulls IfNotPresent, so a reused tag serves a node's cached layer.
  local tag
  tag="x5-$(git -C "$repo_root" rev-parse --short=8 HEAD)-$(date -u +%Y%m%dT%H%M%SZ)"
  printf '%s\n' "$tag" >"$state_dir/image-tag"
  say "installing the cluster prerequisites (operator image $tag built from this tree)"
  # --no-monitoring: the rig reads the services' own /metrics and the broker's through the API
  # server, and the monitoring stack is the largest thing a cluster install pulls.
  #
  # --cluster, NOT --kube-context: see hack/ota-rig.sh for why a named cluster is adopted as a
  # local kind cluster dcctl owns.
  "$dcctl" install local --yes --cluster "$cluster" --no-monitoring --build --version "$tag" 2>&1 | redact
  say "bootstrapping $instance on the DEFAULT profile (no flag turns X.509 on: it comes with the default)"
  # --no-escrow: this instance exists to be torn down.
  "$dcctl" bootstrap local "$instance" --yes --no-escrow --cluster "$cluster" --host "$host" --build --version "$tag" 2>&1 | redact
  say "UP COMPLETE: run 'hack/x509-rig.sh run'"
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
  if docker inspect "$ocsp_container" >/dev/null 2>&1; then
    say "removing the OCSP responder container $ocsp_container"
    docker rm -f "$ocsp_container" >/dev/null
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
      ensure_dcctl
      say "destroying instance $instance"
      "$dcctl" destroy local "$instance" --yes 2>&1 | redact ||
        warn "dcctl destroy exited non-zero; deleting the cluster anyway"
    fi
    say "deleting kind cluster $cluster"
    kind delete cluster --name "$cluster" --kubeconfig "$kubeconfig"
  fi
  # The responder's network, once nothing is attached to it: only the one this rig labelled.
  if [[ "$(docker network inspect "$ocsp_net" -f '{{index .Labels "devicechain.io/x509-rig"}}' 2>/dev/null || true)" == "$cluster" ]]; then
    say "removing the OCSP responder network $ocsp_net"
    docker network rm "$ocsp_net" >/dev/null
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
em="$api/api/event-management"
ingress_ca=""
broker_ca=""
node_ip=""
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
emq() { gql "$em/graphql" "$tenant_token" "$@"; }

login() {
  gql "$um/graphql" - 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
    "$(jq -n --arg e "$1" --arg p "$2" '{e:$e,p:$p}')" | jq -r '.login.identityToken'
}

# ---------------------------------------------------------------------------
# Reading the platform: the callout's counters, the broker's gauges and connection list
# ---------------------------------------------------------------------------

dm_selector=""
dm_port=""
broker_pods=() # "namespace/pod"

discover() {
  k -n "$namespace" get deploy device-management >/dev/null 2>&1 || fail "the instance has no device-management Deployment"
  dm_selector="$(k -n "$namespace" get deploy device-management -o json |
    jq -r '.spec.selector.matchLabels | to_entries | map("\(.key)=\(.value)") | join(",")')"
  dm_port="$(k -n "$namespace" get deploy device-management -o json |
    jq -r '[.spec.template.spec.containers[0].ports[] | select(.name == "graphql" or .name == "http" or .name == "metrics")][0].containerPort // empty')"
  [[ -n "$dm_port" ]] || fail "device-management exposes no graphql/http/metrics port to read metrics from"
  local secret d
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
  mapfile -t broker_pods < <(k get pods -A -l 'app.kubernetes.io/name=nats,app.kubernetes.io/component=nats' \
    --field-selector=status.phase=Running -o json | jq -r '.items[] | select(.metadata.deletionTimestamp == null) | "\(.metadata.namespace)/\(.metadata.name)"')
  ((${#broker_pods[@]} > 0)) || fail "no running broker (nats) pod found"
}

dm_pods() {
  k -n "$namespace" get pods -l "$dm_selector" --field-selector=status.phase=Running -o json |
    jq -r '.items[] | select(.metadata.deletionTimestamp == null) | .metadata.name'
}

# dm_snap prints callout_x509_total as "result=count" lines, summed over every live
# device-management pod. A pod whose metrics cannot be read FAILS the rig: reading it as zero
# would let a control "see" a number it never measured. Every series exists at zero (the counter is
# registered through main.go with the closed result list), so an absent result is an error too.
dm_snap() { dm_counter callout_x509_total result ok; }

# dm_counter <metric> <label> <a value of that label that must exist> prints the counter as
# "value=count" lines, summed over every live device-management pod, under dm_snap's rules.
dm_counter() {
  local pod body pods all=""
  pods="$(dm_pods)"
  [[ -n "$pods" ]] || fail "no live device-management pod to read metrics from"
  for pod in $pods; do
    body="$(k get --raw "/api/v1/namespaces/$namespace/pods/$pod:$dm_port/proxy/metrics")" ||
      fail "could not read the metrics of $pod"
    all+="$(awk -v m="$1" -v l="$2" '
      /^#/ { next }
      { i = index($1, "{"); nm = (i ? substr($1, 1, i - 1) : $1)
        if (nm !~ (m "$")) next
        if (match($1, l "=\"[^\"]*\"")) { r = substr($1, RSTART + length(l) + 2, RLENGTH - length(l) - 3); print r "=" $NF } }' <<<"$body")"$'\n'
  done
  [[ "$all" == *"$3="* ]] || fail "device-management exposes no $1{$2=\"$3\"} series; the metric was renamed or never registered"
  awk -F= 'NF == 2 { s[$1] += $2 } END { for (r in s) printf "%s=%d\n", r, s[r] }' <<<"$all" | sort
}

# snap_get <snapshot> <result> prints that result's count, 0 for a result with no series.
snap_get() { awk -F= -v r="$2" '$1 == r { n = $2 } END { print n + 0 }' <<<"$1"; }
# snap_delta <before> <after> <result>
snap_delta() { echo $(($(snap_get "$2" "$3") - $(snap_get "$1" "$3"))); }
# snap_changes <before> <after> prints "result +n" for every result that moved: what the platform said
# while a step ran, for the message of a step that failed.
snap_changes() {
  local r out=""
  while read -r r; do
    (($(snap_delta "$1" "$2" "$r") == 0)) || out+="$r +$(snap_delta "$1" "$2" "$r") "
  done < <(awk -F= '{ print $1 }' <<<"$2")
  printf '%s' "${out:-nothing moved}"
}
# The results that mean a certificate was ADMITTED (a granted session). A refused connect must
# move none of them.
admit_results="ok jit_created jit_adopted jit_renewed jit_replayed"

# broker_metric <pod> <metric> prints the value of a series of the broker binary's own /metrics.
broker_metric() {
  local ns="${1%%/*}" pod="${1##*/}" body
  body="$(k get --raw "/api/v1/namespaces/$ns/pods/$pod:7778/proxy/metrics")" || return 2
  awk -v m="$2" '/^#/ { next } { i = index($1, "{"); nm = (i ? substr($1, 1, i - 1) : $1); if (nm == m) { v = $NF; found = 1 } } END { if (!found) exit 3; print v }' <<<"$body"
}

# connz_count <client id> prints how many broker connections hold exactly that MQTT client id,
# summed over the broker pods. The broker's monitor port is read from the pod spec.
connz_count() {
  local p ns pod port n total=0 json
  for p in "${broker_pods[@]}"; do
    ns="${p%%/*}"; pod="${p##*/}"
    port="$(k -n "$ns" get pod "$pod" -o json | jq -r '[.spec.containers[].ports[]? | select(.name == "monitor")][0].containerPort // 8222')"
    json="$(k get --raw "/api/v1/namespaces/$ns/pods/$pod:$port/proxy/connz?limit=1024&state=open")" || fail "cannot read $pod's connz"
    n="$(jq --arg c "$1" '[.connections[]? | select(.mqtt_client == $c)] | length' <<<"$json")"
    total=$((total + n))
  done
  echo "$total"
}

# ---------------------------------------------------------------------------
# Devices: the container, the simulator, mosquitto_pub
# ---------------------------------------------------------------------------

sim_bin=""
build_sim() {
  sim_bin="$work/dc-simulator"
  if [[ -n "$release" ]]; then
    fetch_release
    say "using the PUBLISHED dc-simulator of $release"
    cp "$rel_sim" "$sim_bin"
    return
  fi
  say "building dc-simulator (linux/amd64, static)"
  (cd "$repo_root/backend/sims/dc-simulator" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$sim_bin" .)
  [[ -s "$sim_bin" ]] || fail "the simulator was not built"
}

start_container() {
  dk rm -f "$device_container" >/dev/null 2>&1 || true
  dk run -d --init --name "$device_container" --network kind --add-host "$host:$node_ip" \
    --label devicechain.io/x509-rig="$cluster" -v "$(winpath "$work"):/work" \
    --entrypoint /bin/sleep "$device_image" infinity >/dev/null
  cp "$broker_ca" "$work/broker-ca.pem"
  cp "$ingress_ca" "$work/ingress-ca.pem"
  dx chmod 0755 /work/dc-simulator
  dx sh -c 'command -v mosquitto_pub' >/dev/null || fail "the device image has no mosquitto_pub"
}

# write_handshake: the simulator's record of the tenant, the member it signs in as, and the
# endpoints. The broker's certificate is issued for the instance's names, not the node address the
# device reaches it at, so its verification is the one the handshake records as skipped.
write_handshake() {
  jq -n --arg t "$tenant" --arg e "$member_email" --arg p "$member_password" --arg o "$origin" --arg i "$instance" \
    --arg b "ssl://$node_ip:$mqtt_node_port" \
    '{tenant:$t, simEmail:$e, simPassword:$p, instanceId:$i, manifestId:"x509-rig", seed:1, mqttTLSInsecure:true,
      endpoints:{userGraphQL:($o+"/api/user-management/graphql"), deviceMgmtGraphQL:($o+"/api/device-management/graphql"),
                 ingress:($o+"/api/event-sources"), eventMgmtWS:("wss://"+($o|sub("https://";""))+"/api/event-management/graphql"), mqttBroker:$b}}' \
    >"$work/handshake.json"
}

# sim <flags...> runs the simulator in X.509 mode in the device container, in the foreground.
sim() {
  dx env SSL_CERT_FILE=/work/ingress-ca.pem /work/dc-simulator --handshake /work/handshake.json --x509 "$@"
}
# sim_bg <out name> <flags...> holds sessions in the background; stdout (the summary) and stderr go to /work/<out>.*
sim_bg() {
  local out="$1"
  shift
  dk exec -d "$device_container" sh -c "SSL_CERT_FILE=/work/ingress-ca.pem exec /work/dc-simulator --handshake /work/handshake.json --x509 $* >/work/$out.json 2>/work/$out.err"
}

# entry <pki dir> <kind> <index> <field> prints a manifest entry's field (index from 0).
entry() { jq -r --arg k "$2" --argjson i "$3" --arg f "$4" '[.devices[] | select(.kind == $k)][$i][$f]' "$work/$1/manifest.json"; }
entries() { jq -r --arg k "$2" '[.devices[] | select(.kind == $k)] | length' "$work/$1/manifest.json"; }
client_id() { printf '%s:%s:%s' "$instance" "$tenant" "$1"; }

# pub_x509 <cert file> <key file> <client id> <device token> <value>: connect with a client
# certificate (username {tenant}:@x509, no password) and publish one measurement. Sets
# A_RC and A_OUT. The exit status is mosquitto_pub's: 0 only if the broker accepted the connect
# AND the publish was acknowledged.
A_RC=0
A_OUT=""
pub_x509() {
  A_RC=0
  A_OUT="$(dx timeout 25 mosquitto_pub -h "$node_ip" -p "$mqtt_node_port" -V mqttv311 --cafile /work/broker-ca.pem --insecure \
    --cert "$1" --key "$2" -i "$3" -u "$tenant:@x509" -q 1 \
    -t "$instance/$tenant/devices/$4/events" \
    -m "{\"device\":\"$4\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$5\"}}]}}" 2>&1)" || A_RC=$?
  A_OUT="${A_OUT//$'
'/ }"
}
# pub_token <device token> <credential id> <secret file> <value>: the same, as a token device.
pub_token() {
  A_RC=0
  A_OUT="$(dx timeout 25 mosquitto_pub -h "$node_ip" -p "$mqtt_node_port" -V mqttv311 --cafile /work/broker-ca.pem --insecure \
    -i "$(client_id "$1")" -u "$tenant:$2" -P "$(cat "$3")" -q 1 \
    -t "$instance/$tenant/devices/$1/events" \
    -m "{\"device\":\"$1\",\"eventType\":\"Measurement\",\"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$4\"}}]}}" 2>&1)" || A_RC=$?
  A_OUT="${A_OUT//$'
'/ }"
}

# next_value prints a number no earlier publish used: the event it names can only be this one. The
# counter lives in a file because every caller is a command substitution, whose variables die with
# it; a counter kept in a shell variable repeats its first value for ever, and a "this value was
# stored" check then reads an EARLIER event as proof.
next_value() {
  local n=0
  [[ ! -f "$work/value-seq" ]] || n="$(<"$work/value-seq")"
  n=$((n + 1))
  printf '%d' "$n" >"$work/value-seq"
  printf '%d.%02d' $((1000 + n)) $((n % 100))
}

# event_persisted <device token> <value> prints 1 if a stored measurement of the device has that value.
event_persisted() {
  local r
  r="$(emq 'query($c:EventSearchCriteria!){measurementEvents(criteria:$c){results{deviceToken name value}}}' \
    "$(jq -n --arg d "$1" '{c:{pageNumber:1,pageSize:200,deviceToken:$d}}')")" || { echo 0; return; }
  jq -r --argjson v "$2" '[.measurementEvents.results[]? | select(.value == $v)] | length | if . > 0 then 1 else 0 end' <<<"$r"
}
# wait_persisted <device token> <value> <seconds>
wait_persisted() {
  local deadline=$((SECONDS + $3))
  while ((SECONDS < deadline)); do
    [[ "$(event_persisted "$1" "$2")" == 1 ]] && { echo 1; return; }
    sleep 2
  done
  echo 0
}
# device_exists <token> prints 1 or 0.
device_exists() {
  dmq 'query($t:[String!]!){devicesByToken(tokens:$t){token}}' "$(jq -n --arg t "$1" '{t:[$t]}')" |
    jq -r '.devicesByToken | length | if . > 0 then 1 else 0 end'
}
device_total() {
  dmq 'query($c:DeviceSearchCriteria!){devices(criteria:$c){pagination{totalRecords}}}' "$(jq -n --arg p "${1:-}" '{c:({pageNumber:1,pageSize:1}+(if $p == "" then {} else {provisioningProfile:$p} end))}')" |
    jq -r '.devices.pagination.totalRecords'
}

# ---------------------------------------------------------------------------
# CHECKERS
#
# An attempt is a RECORD: what the broker answered (R_RC, 0 only if the device got in and its
# publish was acknowledged), the callout's counters before and after (R_BEFORE, R_AFTER), whether
# the published value was stored (R_EVENTS), and whether the device exists (R_EXISTS, or "-" when
# the attempt does not speak for it). A checker is a PURE function of the record, so a control can
# hand it a record that must not pass and require it to go red. Its verdict is its last line:
#
#   GREEN               every source agreed
#   RED: <tag> <tag>    these sources disagreed, each explained on a line of its own (status 1)
#
# A checker evaluates EVERY source before it answers, so the tags say which sources disagreed, not
# merely the first.
# ---------------------------------------------------------------------------

R_RC=0
R_EXPECT=""
R_BEFORE=""
R_AFTER=""
R_EVENTS=0
R_EXISTS="-"

# check_admitted: the device got in. connect: the broker accepted it; counter: the callout counted
# R_EXPECT; events: the event persisted; exists (only when R_EXISTS is not "-"): the device is in the store.
check_admitted() {
  local -a tags=()
  ((R_RC == 0)) || { tags+=(connect); echo "  connect: mosquitto_pub exited $R_RC; the broker did not take the device"; }
  (($(snap_delta "$R_BEFORE" "$R_AFTER" "$R_EXPECT") >= 1)) ||
    { tags+=(counter); echo "  counter: callout_x509_total{result=\"$R_EXPECT\"} did not rise"; }
  [[ "$R_EVENTS" == 1 ]] || { tags+=(events); echo "  events: the published value was not stored"; }
  [[ "$R_EXISTS" == "-" || "$R_EXISTS" == 1 ]] || { tags+=(exists); echo "  exists: the device is not in the store"; }
  if ((${#tags[@]})); then echo "RED: ${tags[*]}"; return 1; fi
  echo "admitted: CONNACK accepted; callout_x509_total{result=\"$R_EXPECT\"} +$(snap_delta "$R_BEFORE" "$R_AFTER" "$R_EXPECT"); the event is stored"
  echo GREEN
}

# check_refused: the device did NOT get in. connect: the broker took it (bad); counter: the callout
# did not count R_EXPECT; leaked: a result that means ADMITTED rose; events: its value was stored;
# exists: the device is in the store though the attempt must not have created it.
check_refused() {
  local -a tags=()
  local r leaked=0
  ((R_RC != 0)) || { tags+=(connect); echo "  connect: the broker ACCEPTED the connect and the publish"; }
  (($(snap_delta "$R_BEFORE" "$R_AFTER" "$R_EXPECT") >= 1)) ||
    { tags+=(counter); echo "  counter: callout_x509_total{result=\"$R_EXPECT\"} did not rise"; }
  for r in $admit_results; do leaked=$((leaked + $(snap_delta "$R_BEFORE" "$R_AFTER" "$r"))); done
  ((leaked == 0)) || { tags+=(leaked); echo "  leaked: an admitting result rose by $leaked"; }
  [[ "$R_EVENTS" != 1 ]] || { tags+=(events); echo "  events: the published value was STORED"; }
  [[ "$R_EXISTS" != 1 ]] || { tags+=(exists); echo "  exists: a device was created"; }
  if ((${#tags[@]})); then echo "RED: ${tags[*]}"; return 1; fi
  echo "refused: CONNACK refused (mosquitto_pub exit $R_RC); callout_x509_total{result=\"$R_EXPECT\"} +$(snap_delta "$R_BEFORE" "$R_AFTER" "$R_EXPECT"); no admitting result moved; nothing stored"
  echo GREEN
}

# expect_red <label> <required tags> <exact: yes|no> <checker...>: run a checker that MUST go red, and
# red for the right reason: every required tag present, and (exact) no other tag.
expect_red() {
  local label="$1" need="$2" exact="$3" out rc=0 tags t
  shift 3
  out="$("$@" 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    | /'
  ((rc != 0)) || fail "$label: the checker PASSED a state it must refuse: it cannot go red"
  tags="$(sed -n 's/^RED: //p' <<<"$out" | tail -n 1)"
  [[ -n "$tags" ]] || fail "$label: the checker stopped without a verdict (status $rc)"
  for t in $need; do
    [[ " $tags " == *" $t "* ]] || fail "$label: red for the wrong reason: [$tags] lacks '$t'"
  done
  if [[ "$exact" == yes ]]; then
    for t in $tags; do [[ " $need " == *" $t "* ]] || fail "$label: red for the wrong reason: '$t' in [$tags], wanted exactly [$need]"; done
  fi
  note "$label: red on [$tags]"
}

set_record() { R_RC="$1"; R_EXPECT="$2"; R_BEFORE="$3"; R_AFTER="$4"; R_EVENTS="$5"; R_EXISTS="${6:--}"; }

# attempt_record <expect> <kind admit|deny> <device token to read the store for> <exists-check yes|no> <value>:
# turns the last pub (A_RC) and the counters taken around it into the record, reading the store.
snap_before=""
attempt_record() {
  local expect="$1" kind="$2" dev="$3" chk="$4" val="$5" after ev ex="-"
  after="$(dm_snap)"
  if [[ "$kind" == admit ]]; then
    ev="$(wait_persisted "$dev" "$val" 60)"
  else
    # A refused device's value must not appear even given time to: let the pipeline settle first.
    sleep 6
    ev="$(event_persisted "$dev" "$val")"
  fi
  [[ "$chk" != yes ]] || ex="$(device_exists "$dev")"
  set_record "$A_RC" "$expect" "$snap_before" "$after" "$ev" "$ex"
}

# offline_controls: the checkers against fabricated records, with no cluster. A checker that cannot
# go red must not reach the platform.
offline_controls() {
  say "OFFLINE CONTROLS: the checkers, on fabricated records"
  local b a_ok a_ci a_leak a_rev
  b=$'ok=3\nchain_invalid=1\nrevoked=0\njit_created=0'
  a_ok=$'ok=4\nchain_invalid=1\nrevoked=0\njit_created=0'
  a_ci=$'ok=3\nchain_invalid=2\nrevoked=0\njit_created=0'
  a_leak=$'ok=4\nchain_invalid=2\nrevoked=0\njit_created=0'
  a_rev=$'ok=3\nchain_invalid=1\nrevoked=1\njit_created=0'
  local out
  set_record 0 ok "$b" "$a_ok" 1 -
  out="$(check_admitted)" || fail "check_admitted rejects an admitted record: $out"
  note "admitted record: accepted by check_admitted"
  set_record 5 chain_invalid "$b" "$a_ci" 0 0
  out="$(check_refused)" || fail "check_refused rejects a refused record: $out"
  note "refused record: accepted by check_refused"
  set_record 5 chain_invalid "$b" "$a_ci" 0 0
  expect_red "a refused record through check_admitted" "connect events" no check_admitted
  red "check_admitted: a refused record -> red on connect and events"
  set_record 0 ok "$b" "$a_ok" 1 -
  expect_red "an admitted record through check_refused" "connect leaked events" no check_refused
  red "check_refused: an admitted record -> red on connect, leaked, events"
  set_record 5 chain_invalid "$b" "$a_rev" 0 0
  expect_red "a refused record counted under the wrong result" "counter" yes check_refused
  red "check_refused: refused, but counted as revoked instead of chain_invalid -> red on counter alone"
  set_record 5 chain_invalid "$b" "$a_leak" 0 0
  expect_red "a refused connect that also moved ok" "leaked" yes check_refused
  red "check_refused: the broker refused but \"ok\" rose -> red on leaked alone"
  set_record 5 chain_invalid "$b" "$a_ci" 0 1
  expect_red "a refused connect that created a device" "exists" yes check_refused
  red "check_refused: refused, but the device exists -> red on exists alone"
  set_record 0 jit_created "$b" "$a_ok" 1 1
  expect_red "an admitted device whose counter did not move" "counter" yes check_admitted
  red "check_admitted: admitted and stored, but jit_created did not rise -> red on counter alone"
  expect_red "a pin that holds no servers" "servers" yes check_pin_state 1 0 3 0 0
  expect_red "a pin whose system connection is down" "connected" yes check_pin_state 0 3 3 0 0
  expect_red "a pin that refused the broker's own requests" "unpinned" yes check_pin_state 1 3 3 2 0
  expect_red "a pin that could not vouch" "unavailable" yes check_pin_state 1 3 3 0 4
  out="$(check_pin_state 1 3 3 0 0)" || fail "check_pin_state rejects a healthy pin: $out"
  note "healthy pin: accepted by check_pin_state"
  ocsp_offline_controls
}

# ---------------------------------------------------------------------------
# Step 1: the broker is ours and requests client certificates
# ---------------------------------------------------------------------------

# check_broker_gauges <info: 1|absent> <tls> <client-auth-request>: a checker (see CHECKERS) over what
# one broker pod reports. info: dc_broker_info exists, which only the DeviceChain broker build
# exports; tls: the MQTT listener terminates TLS; auth: it asks the client for a certificate.
check_broker_gauges() {
  local -a tags=()
  [[ "$1" == 1 ]] || { tags+=(info); echo "  info: dc_broker_info is $1: this is not the DeviceChain broker build"; }
  [[ "$2" == 1 ]] || { tags+=(tls); echo "  tls: dc_broker_mqtt_tls is $2"; }
  [[ "$3" == 1 ]] || { tags+=(auth); echo "  auth: dc_broker_mqtt_client_auth_request is $3: the listener does not request certificates"; }
  if ((${#tags[@]})); then echo "RED: ${tags[*]}"; return 1; fi
  echo GREEN
}

step1_broker() {
  say "STEP 1: the broker is the DeviceChain build and requests client certificates"
  local p info tls auth image out
  # The gauge check must be able to go red: a stock broker has no dc_broker_info, and a reload
  # that lost the request reads 0.
  expect_red "a stock broker (no dc_broker_info)" "info" no check_broker_gauges absent 1 1
  expect_red "a broker whose listener stopped requesting certificates" "auth" yes check_broker_gauges 1 1 0
  for p in "${broker_pods[@]}"; do
    info="$(broker_metric "$p" dc_broker_info)" || info=absent
    tls="$(broker_metric "$p" dc_broker_mqtt_tls)" || tls=absent
    auth="$(broker_metric "$p" dc_broker_mqtt_client_auth_request)" || auth=absent
    image="$(k -n "${p%%/*}" get pod "${p##*/}" -o json | jq -r '[.spec.containers[] | select(.name == "nats")][0].image // .spec.containers[0].image')"
    out="$(check_broker_gauges "$info" "$tls" "$auth")" || fail "broker $p: $out"
    pass "broker $p runs $image: dc_broker_info=$info dc_broker_mqtt_tls=$tls dc_broker_mqtt_client_auth_request=$auth"
  done
}

# ---------------------------------------------------------------------------
# Step 2: tenant, type, a token device
# ---------------------------------------------------------------------------

member_password=""
tokdev="x509-rig-token-1"
tok_secret=""

# token_alive <label>: the token device still connects with its password and its event is stored.
token_alive() {
  local val
  val="$(next_value)"
  pub_token "$tokdev" "$tokdev" "$tok_secret" "$val"
  ((A_RC == 0)) || fail "token device ($1): mosquitto_pub exited $A_RC: $A_OUT"
  [[ "$(wait_persisted "$tokdev" "$val" 60)" == 1 ]] || fail "token device ($1): its event $val was not stored"
  pass "token device alive ($1): password connect accepted, event $val stored"
}

# make_tenant <registration burst>: the tenant $tenant (fresh per run), a member who administers
# it ($member_email), signed in as that member (tenant_token), with the device profile and type
# the devices use. The superuser signs in on the first call.
make_tenant() {
  local burst="$1" pw tier ident
  if [[ -z "$admin_token" ]]; then
    pw="$(k -n "$namespace" get secret "dci-$instance-superuser" -o jsonpath='{.data.password}' | base64 -d)"
    [[ -n "$pw" ]] || fail "Secret $namespace/dci-$instance-superuser holds no superuser password"
    admin_token="$(login "superuser@devicechain.local" "$pw")"
    [[ -n "$admin_token" && "$admin_token" != null ]] || fail "could not sign in as the superuser"
  fi
  tier="$(adm '{tenantTiers{token}}' | jq -r '.tenantTiers[0].token')"
  [[ -n "$tier" && "$tier" != null ]] || fail "the instance has no tenant tier"
  # The tenant's own override of its registration ceiling: a burst of $burst, refilled at a rate
  # too small to matter over a run. The callout charges the SAME limiter the self-registration
  # endpoint does.
  adm 'mutation($r:AdminTenantCreateRequest!){createTenant(request:$r){token}}' \
    "$(jq -n --arg t "$tenant" --arg tier "$tier" --argjson b "$burst" \
      '{r:{token:$t,name:"X509 rig",tierToken:$tier,provisioningRegistrationsPerSecond:0.001,provisioningBurst:$b}}')" >/dev/null ||
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
    "$(jq -n --arg t "$profile" '{r:{token:$t,name:"X509 rig device"}}')" >/dev/null || fail "createDeviceProfile"
  dmq 'mutation($t:String!){publishDeviceProfile(token:$t){version}}' "$(jq -n --arg t "$profile" '{t:$t}')" >/dev/null ||
    fail "publishDeviceProfile"
  dmq 'mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}' \
    "$(jq -n --arg t "$dtype" --arg p "$profile" '{r:{token:$t,name:"X509 rig device",profileToken:$p}}')" >/dev/null ||
    fail "createDeviceType"
}

step2_setup() {
  say "STEP 2: tenant $tenant (registration budget: burst $jit_burst), device type, a token device"
  make_tenant "$jit_burst"

  tok_secret="$work/secret-token"
  openssl rand -hex 24 | tr -d '\n' >"$tok_secret"
  dmq 'mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}' \
    "$(jq -n --arg t "$tokdev" --arg ty "$dtype" '{r:{token:$t,name:$t,deviceTypeToken:$ty}}')" >/dev/null || fail "createDevice $tokdev"
  dmq 'mutation($r:DeviceCredentialCreateRequest){createDeviceCredential(request:$r){token}}' \
    "$(jq -n --arg t "$tokdev-mqtt" --arg d "$tokdev" --arg c "$tokdev" --rawfile s "$tok_secret" \
      '{r:{token:$t,deviceToken:$d,credentialType:"MQTT_BASIC",credentialId:$c,credentialValue:$s,enabled:true}}')" >/dev/null ||
    fail "createDeviceCredential $tokdev"

  build_sim
  start_container
  write_handshake
  note "device container $device_container ($device_image) on the kind network; broker ssl://$node_ip:$mqtt_node_port"
  note "clock: host $(date -u +%s), device container $(dx date -u +%s) (epoch seconds; a certificate minted here must be valid there)"

  # The token checker must be able to fail: the wrong password is refused.
  local save="$tok_secret"
  printf 'not-the-password-0000' >"$work/secret-wrong"
  pub_token "$tokdev" "$tokdev" "$work/secret-wrong" 1
  ((A_RC != 0)) || fail "the token device connected with a WRONG password: token_alive cannot go red"
  red "token device with a wrong password -> refused (mosquitto_pub exit $A_RC)"
  tok_secret="$save"
  token_alive "baseline, before any certificate exists"
}

# ---------------------------------------------------------------------------
# Running one attempt and judging it
# ---------------------------------------------------------------------------

# attempt <label> <expect> <admit|deny> <cert> <key> <client id> <device token> <exists-check yes|no>
# presents a certificate, builds the record and requires the right checker to be GREEN. For a denial
# it also requires the OPPOSITE checker to be red on the very same record, naming connect and events.
attempt() {
  local label="$1" expect="$2" kind="$3" cert="$4" key="$5" cid="$6" dev="$7" chk="$8" val out
  val="$(next_value)"
  snap_before="$(dm_snap)"
  pub_x509 "$cert" "$key" "$cid" "$dev" "$val"
  attempt_record "$expect" "$kind" "$dev" "$chk" "$val"
  if [[ "$kind" == admit ]]; then
    out="$(check_admitted)" || fail "$label: $out ${A_OUT:+(mosquitto_pub: ${A_OUT:0:200})}"
    pass "$label: $(head -n1 <<<"$out")"
  else
    out="$(check_refused)" || fail "$label: $out ${A_OUT:+(mosquitto_pub: ${A_OUT:0:200})}"
    pass "$label: $(head -n1 <<<"$out") [mosquitto: ${A_OUT:0:60}]"
    expect_red "$label: the admit checker on this very record" "connect events" no check_admitted
  fi
}

# attempt_entry <label> <expect> <kind> <pki dir> <jit|pinned> <index> <exists-check> [client id override]
attempt_entry() {
  local label="$1" expect="$2" kind="$3" dir="$4" ek="$5" idx="$6" chk="$7" cid="${8:-}" tok
  tok="$(entry "$dir" "$ek" "$idx" token)"
  [[ -n "$cid" ]] || cid="$(client_id "$tok")"
  attempt "$label" "$expect" "$kind" "/work/$dir/$(entry "$dir" "$ek" "$idx" certFile)" "/work/$dir/$(entry "$dir" "$ek" "$idx" keyFile)" "$cid" "$tok" "$chk"
}

simp() { local d="$1" p="$2" a="$3"; shift 3; sim --x509-dir "/work/$d" --x509-prefix "$p" --x509-anchor "$a" "$@"; }
simp_bg() { local o="$1" d="$2" p="$3" a="$4"; shift 4; sim_bg "$o" --x509-dir "/work/$d" --x509-prefix "$p" --x509-anchor "$a" "$@"; }

# ---------------------------------------------------------------------------
# Step 3: the pinned path
# ---------------------------------------------------------------------------

main_dir=pki-main
main_prefix=x5r-
main_anchor=x509rig-ca
jit_profile=x509-rig-jit

step3_pinned() {
  say "STEP 3: PINNED: the simulator mints a CA, registers it, creates $n_pinned devices with pinned credentials"
  local out before after i tok cnt
  simp "$main_dir" "$main_prefix" "$main_anchor" --x509-pinned "$n_pinned" --x509-register --x509-device-type "$dtype" --x509-mint-only 2>>"$work/sim.err" ||
    fail "the simulator could not mint and register the pinned fleet"
  cnt="$(dmq 'query($t:[String!]!){deviceTrustAnchorsByToken(tokens:$t){token enabled credentialCount}}' "$(jq -n --arg t "$main_anchor" '{t:[$t]}')" |
    jq -r '.deviceTrustAnchorsByToken[0] | "\(.enabled) \(.credentialCount)"')"
  [[ "$cnt" == "true $n_pinned" ]] || fail "the anchor should be enabled with $n_pinned credentials, the platform says '$cnt'"
  pass "anchor $main_anchor registered by the simulator: enabled, credentialCount=$cnt"

  before="$(dm_snap)"
  out="$(simp "$main_dir" "$main_prefix" "$main_anchor" --x509-pinned "$n_pinned" --x509-hold 3s 2>>"$work/sim.err" | grep '^{' | tail -n1)" || true
  after="$(dm_snap)"
  jq -e --argjson n "$n_pinned" '.pinned.connected == $n and .pinned.refused == 0' <<<"$out" >/dev/null ||
    fail "the pinned fleet did not all connect (callout_x509_total moved: $(snap_changes "$before" "$after")): $out"
  (($(snap_delta "$before" "$after" ok) == n_pinned)) ||
    fail "callout_x509_total{result=\"ok\"} rose by $(snap_delta "$before" "$after" ok), wanted $n_pinned"
  pass "$n_pinned pinned devices connected (simulator summary $(jq -c '.pinned' <<<"$out")); callout_x509_total{result=\"ok\"} +$n_pinned"

  for ((i = 0; i < n_pinned; i++)); do
    attempt_entry "pinned device $((i + 1))/$n_pinned publishes telemetry through its certificate" ok admit "$main_dir" pinned "$i" no
  done
  token_alive "after the pinned fleet"
}

# ---------------------------------------------------------------------------
# Step 4: the JIT path
# ---------------------------------------------------------------------------

step4_jit() {
  say "STEP 4: JIT: a certificate profile (ALLOW_NEW) on the anchor; $m_jit never-registered certificates connect"
  local out before after total0 total1 i tok cn ext got
  dmq 'mutation($r:ProvisioningProfileCreateRequest){createProvisioningProfile(request:$r){profile{token trustAnchor{token}}}}' \
    "$(jq -n --arg t "$jit_profile" --arg ty "$dtype" --arg a "$main_anchor" \
      '{r:{token:$t,name:"X509 rig JIT",provisionKey:"x509-rig-jit-key",strategy:"ALLOW_NEW",deviceTypeToken:$ty,credentialType:"X509_CERTIFICATE",enabled:true,trustAnchorToken:$a}}')" >/dev/null ||
    fail "createProvisioningProfile $jit_profile"
  simp "$main_dir" "$main_prefix" "$main_anchor" --x509-pinned "$n_pinned" --x509-jit "$m_jit" --x509-mint-only 2>>"$work/sim.err" || fail "the simulator could not mint the JIT certificates"
  for ((i = 0; i < m_jit; i++)); do
    tok="$(entry "$main_dir" jit "$i" token)"
    [[ "$(device_exists "$tok")" == 0 ]] || fail "$tok exists before it connected: the JIT certificates must be devices the platform has never seen"
  done
  total0="$(device_total "")"
  before="$(dm_snap)"
  out="$(simp "$main_dir" "$main_prefix" "$main_anchor" --x509-pinned "$n_pinned" --x509-jit "$m_jit" --x509-hold 3s 2>>"$work/sim.err" | grep '^{' | tail -n1)" || true
  after="$(dm_snap)"
  jq -e --argjson n "$n_pinned" --argjson m "$m_jit" '.jit.connected == $m and .jit.refused == 0 and .pinned.connected == $n' <<<"$out" >/dev/null ||
    fail "the fleet did not connect (want $m_jit JIT and $n_pinned pinned; callout_x509_total moved: $(snap_changes "$before" "$after")): $out"
  (($(snap_delta "$before" "$after" jit_created) == m_jit)) ||
    fail "callout_x509_total{result=\"jit_created\"} rose by $(snap_delta "$before" "$after" jit_created), wanted exactly $m_jit"
  total1="$(device_total "")"
  ((total1 - total0 == m_jit)) || fail "the tenant has $total1 devices, was $total0: exactly $m_jit were to be created"
  got="$(device_total "$jit_profile")"
  [[ "$got" == "$m_jit" ]] || fail "$got devices hold a credential from profile $jit_profile, wanted $m_jit"
  for ((i = 0; i < m_jit; i++)); do
    tok="$(entry "$main_dir" jit "$i" token)"
    cn="$(entry "$main_dir" jit "$i" externalId)"
    ext="$(dmq 'query($t:[String!]!){devicesByToken(tokens:$t){token externalId}}' "$(jq -n --arg t "$tok" '{t:[$t]}')" | jq -r '.devicesByToken[0] | "\(.token) \(.externalId)"')"
    [[ "$ext" == "$tok $cn" ]] || fail "JIT device $i: the store has '$ext', wanted token $tok (derived) with externalId $cn"
    [[ "$tok" == pv-* ]] || fail "JIT device token '$tok' is not the derived pv- token"
  done
  pass "$m_jit JIT certificates connected: jit_created +$m_jit, tenant devices $total0 -> $total1, profile $jit_profile holds $got, each created under its derived pv- token with the CN as externalId"
  attempt_entry "JIT device 1 publishes telemetry through its certificate" ok admit "$main_dir" jit 0 yes
  token_alive "after JIT registration"
}

# ---------------------------------------------------------------------------
# Step 5: negative controls
# ---------------------------------------------------------------------------

# MSYS2_ARG_CONV_EXCL keeps Git Bash from rewriting the /CN=... subject into a Windows path; every file
# argument still gets the conversion the native openssl needs.
ossl() { MSYS2_ARG_CONV_EXCL='/CN=' openssl "$@"; }
mk_key() { ossl ecparam -name prime256v1 -genkey -noout | ossl pkcs8 -topk8 -nocrypt -out "$1"; }
mk_root() { # <dir>
  mk_key "$1/root.key"
  ossl req -x509 -new -key "$1/root.key" -subj "/CN=x509-rig root $(date -u +%s)" -days 30 -sha256 \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:1" -addext "keyUsage=critical,keyCertSign,cRLSign" -out "$1/root.pem" 2>/dev/null
}
# mk_inter <dir> <name> <constrained yes|no> <signer pem> <signer key>
mk_inter() {
  local d="$1" n="$2"
  mk_key "$d/$n.key"
  ossl req -new -key "$d/$n.key" -subj "/CN=x509-rig intermediate $n" -out "$d/$n.csr" 2>/dev/null
  {
    echo "basicConstraints=critical,CA:TRUE,pathlen:0"
    echo "keyUsage=critical,keyCertSign,cRLSign"
    [[ "$3" != yes ]] || echo "nameConstraints=critical,permitted;DNS:.devices.example.test"
  } >"$d/$n.ext"
  ossl x509 -req -in "$d/$n.csr" -CA "$4" -CAkey "$5" -set_serial "0x$(openssl rand -hex 8)" -days 30 -sha256 -extfile "$d/$n.ext" -out "$d/$n.pem" 2>/dev/null
}
# mk_leaf <dir> <cn> <eku yes|no> <signer pem> <signer key> <out crt> <out key>
mk_leaf() {
  local d="$1" cn="$2"
  mk_key "$7"
  ossl req -new -key "$7" -subj "/CN=$cn" -out "$d/leaf.csr" 2>/dev/null
  {
    echo "basicConstraints=critical,CA:FALSE"
    echo "keyUsage=critical,digitalSignature"
    [[ "$3" != yes ]] || echo "extendedKeyUsage=clientAuth"
  } >"$d/leaf.ext"
  ossl x509 -req -in "$d/leaf.csr" -CA "$4" -CAkey "$5" -set_serial "0x$(openssl rand -hex 8)" -days 30 -sha256 -extfile "$d/leaf.ext" -out "$6" 2>/dev/null
}

# craft_pair <index> <cn-owner dir> then the cert/key are written over the simulator-minted files, so the
# manifest's token (the derivation of the CN) and the file names stay the simulator's.
cn_of() { entry pki-craft jit "$1" externalId; }
cert_of() { printf '%s/pki-craft/%s' "$work" "$(entry pki-craft jit "$1" certFile)"; }
key_of() { printf '%s/pki-craft/%s' "$work" "$(entry pki-craft jit "$1" keyFile)"; }

step5_controls() {
  say "STEP 5: NEGATIVE CONTROLS"
  local m_total tok i p0 p1 val before after wait_s na now out
  # More JIT certificates for the controls to spend: top-up, never re-keys the ones that connected.
  # M (client id), M+1 (disabled profile) and M+2.. (the budget).
  local n9=12
  m_total=$((m_jit + 2 + n9))
  simp "$main_dir" "$main_prefix" "$main_anchor" --x509-pinned "$n_pinned" --x509-jit "$m_total" --x509-mint-only 2>>"$work/sim.err" || fail "could not mint more JIT certificates"

  # ---- N1: a certificate from a different CA, carrying a real device's CN (so its client id) ----
  say "N1: a certificate from a different CA, with the CN (and so the client id) of pinned device 2"
  simp pki-other "$main_prefix" x509rig-other-ca --x509-pinned 2 --x509-mint-only 2>>"$work/sim.err" || fail "could not mint the other CA"
  [[ "$(entry pki-other pinned 1 token)" == "$(entry "$main_dir" pinned 1 token)" ]] || fail "the impersonating certificate does not carry the real device's token"
  [[ "$(entry pki-other pinned 1 fingerprintSha256)" != "$(entry "$main_dir" pinned 1 fingerprintSha256)" ]] || fail "the other CA minted the very same certificate"
  attempt_entry "N1 other CA, device 2's identity" chain_invalid deny pki-other pinned 1 no "$(client_id "$(entry "$main_dir" pinned 1 token)")"
  attempt_entry "N1 control: device 2's own certificate is admitted" ok admit "$main_dir" pinned 1 no

  # ---- N3 + N7 need certificates the simulator does not mint: an own root, with and without an
  # intermediate, signed here with openssl. The simulator mints the names (so the derived tokens).
  say "crafting certificates with openssl: a root R, its anchor and profile, and leaves"
  simp pki-craft x5c- x509rig-craft-unused --x509-pinned 0 --x509-jit 5 --x509-mint-only 2>>"$work/sim.err" || fail "could not mint the craft names"
  local cdir="$work/openssl"
  mkdir -p "$cdir"
  mk_root "$cdir"
  local rpem rkey
  rpem="$cdir/root.pem"; rkey="$cdir/root.key"
  dmq 'mutation($r:DeviceTrustAnchorCreateRequest!){createDeviceTrustAnchor(request:$r){token}}' \
    "$(jq -n --rawfile p "$rpem" '{r:{token:"x509rig-r-ca",name:"openssl root",certificatePem:$p,enabled:true}}')" >/dev/null ||
    fail "createDeviceTrustAnchor x509rig-r-ca (the openssl root): is this host's clock ahead of the cluster's?"
  dmq 'mutation($r:ProvisioningProfileCreateRequest){createProvisioningProfile(request:$r){profile{token}}}' \
    "$(jq -n --arg ty "$dtype" '{r:{token:"x509rig-jit-r",name:"X509 rig JIT (openssl root)",provisionKey:"x509-rig-jit-key-r",strategy:"ALLOW_NEW",deviceTypeToken:$ty,credentialType:"X509_CERTIFICATE",enabled:true,trustAnchorToken:"x509rig-r-ca"}}')" >/dev/null ||
    fail "createProvisioningProfile x509rig-jit-r"
  mk_inter "$cdir" plain no "$rpem" "$rkey"
  mk_inter "$cdir" nc yes "$rpem" "$rkey"
  # c0: a leaf directly under R, with clientAuth: the control that the openssl certificates are sound.
  mk_leaf "$cdir" "$(cn_of 0)" yes "$rpem" "$rkey" "$(cert_of 0)" "$(key_of 0)"
  # c1: no clientAuth extended key usage.
  mk_leaf "$cdir" "$(cn_of 1)" no "$rpem" "$rkey" "$(cert_of 1)" "$(key_of 1)"
  # c2: under the name-constrained intermediate; c3: under the plain one. The presented file is leaf + intermediate.
  mk_leaf "$cdir" "$(cn_of 2)" yes "$cdir/nc.pem" "$cdir/nc.key" "$cdir/leaf2.crt" "$(key_of 2)"
  cat "$cdir/leaf2.crt" "$cdir/nc.pem" >"$(cert_of 2)"
  mk_leaf "$cdir" "$(cn_of 3)" yes "$cdir/plain.pem" "$cdir/plain.key" "$cdir/leaf3.crt" "$(key_of 3)"
  cat "$cdir/leaf3.crt" "$cdir/plain.pem" >"$(cert_of 3)"

  # ---- N3 ----
  say "N3: no clientAuth extended key usage"
  attempt_entry "N3 no clientAuth EKU" chain_invalid deny pki-craft jit 1 yes
  attempt_entry "N3 control: the same construction WITH clientAuth registers" jit_created admit pki-craft jit 0 yes

  # ---- N7 ----
  say "N7: a name-constrained intermediate"
  attempt_entry "N7 leaf under a name-constrained intermediate" chain_invalid deny pki-craft jit 2 yes
  attempt_entry "N7 control: the same chain under an UNconstrained intermediate registers" jit_created admit pki-craft jit 3 yes
  if dmq 'mutation($r:DeviceTrustAnchorCreateRequest!){createDeviceTrustAnchor(request:$r){token}}' \
    "$(jq -n --rawfile p "$cdir/nc.pem" '{r:{token:"x509rig-nc-anchor",certificatePem:$p}}')" >"$work/nc-anchor.out" 2>"$work/nc-anchor.err"; then
    fail "N7: the platform ACCEPTED a name-constrained intermediate as a trust anchor"
  fi
  grep -qi "name constraint" "$work/nc-anchor.err" || fail "N7: the anchor was refused, but not for name constraints: $(cat "$work/nc-anchor.err")"
  pass "N7: the same intermediate is refused as a trust anchor (\"$(jq -r '.[0].message' "$work/nc-anchor.err" | cut -c1-110)...\")"

  # ---- N2 ----
  say "N2: an expired certificate (valid 80 s)"
  simp pki-exp x5e- x509rig-exp-ca --x509-pinned 1 --x509-validity 80s --x509-register --x509-device-type "$dtype" --x509-mint-only 2>>"$work/sim.err" ||
    fail "could not mint and register the short-lived certificate"
  tok="$(entry pki-exp pinned 0 token)"
  na="$(date -d "$(entry pki-exp pinned 0 notAfter)" +%s)"
  before="$(dm_snap)"
  simp_bg n2 pki-exp x5e- x509rig-exp-ca --x509-pinned 1 --x509-validity 80s --x509-hold 300s
  wait_connz "$(client_id "$tok")" 1 40 || fail "N2: the short-lived device never showed on the broker (see $(dx cat /work/n2.err | tail -3))"
  after="$(dm_snap)"
  (($(snap_delta "$before" "$after" ok) >= 1)) || fail "N2: ok did not rise for the live short-lived device"
  pass "N2 control: the certificate is accepted while valid: ok +$(snap_delta "$before" "$after" ok), the broker holds its session"
  now="$(date +%s)"
  wait_s=$((na - now + 15))
  ((wait_s > 0)) || wait_s=1
  note "waiting $wait_s s for the certificate to expire (notAfter $(entry pki-exp pinned 0 notAfter))"
  sleep "$wait_s"
  wait_connz "$(client_id "$tok")" 0 30 || fail "N2: the session outlived its certificate: the broker still holds $(client_id "$tok")"
  pass "N2: the broker ended the live session at the certificate's notAfter (connz 0)"
  dx pkill dc-simulator 2>/dev/null || true
  attempt_entry "N2 expired certificate" chain_invalid deny pki-exp pinned 0 no

  # ---- N4 ----
  say "N4: a revoked (disabled) credential, connecting immediately after the disable"
  tok="$(entry "$main_dir" pinned 0 token)"
  attempt_entry "N4 control: the credential is live and the device connects" ok admit "$main_dir" pinned 0 no
  dmq 'mutation($t:String!,$r:DeviceCredentialUpdateRequest!){updateDeviceCredential(token:$t,request:$r){token enabled}}' \
    "$(jq -n --arg t "$tok-x509" '{t:$t,r:{enabled:false}}')" >/dev/null || fail "could not disable credential $tok-x509"
  attempt_entry "N4 revoked credential (no sleep between the disable and the connect)" revoked deny "$main_dir" pinned 0 no

  # ---- N5 ----
  say "N5: an anchor disabled while its sessions are live"
  simp pki-n5 x5n-n5- x509rig-n5-ca --x509-pinned 2 --x509-register --x509-device-type "$dtype" --x509-mint-only 2>>"$work/sim.err" || fail "could not mint the N5 fleet"
  simp_bg n5 pki-n5 x5n-n5- x509rig-n5-ca --x509-pinned 2 --x509-hold 600s
  p0="$(client_id "$(entry pki-n5 pinned 0 token)")"
  p1="$(client_id "$(entry pki-n5 pinned 1 token)")"
  wait_connz "$p0" 1 40 || fail "N5: device 1 never showed on the broker ($(dx tail -3 /work/n5.err))"
  wait_connz "$p1" 1 40 || fail "N5: device 2 never showed on the broker"
  pass "N5 control: both sessions of anchor x509rig-n5-ca are live on the broker (connz 1 and 1)"
  out="$(dmq 'mutation($t:String!){updateDeviceTrustAnchor(token:$t,request:{enabled:false}){token enabled}}' "$(jq -n '{t:"x509rig-n5-ca"}')" 2>&1)" ||
    note "the disable answered: ${out:0:200}"
  wait_connz "$p0" 0 60 && wait_connz "$p1" 0 60 || fail "N5: the anchor was disabled and the broker STILL holds a session of its devices"
  pass "N5: the anchor disabled -> the broker ended both live sessions (connz 0 and 0)"
  dx pkill dc-simulator 2>/dev/null || true
  attempt_entry "N5 reconnect under the disabled anchor" anchor_disabled deny pki-n5 pinned 0 no
  token_alive "after an anchor was disabled"

  # ---- N6 ----
  say "N6: a client id that does not match the derived token"
  attempt_entry "N6 pinned device 3's certificate, device 4's client id" client_id_mismatch deny "$main_dir" pinned 2 no "$(client_id "$(entry "$main_dir" pinned 3 token)")"
  attempt_entry "N6 unregistered JIT certificate, another token's client id (no device may be created)" client_id_mismatch deny "$main_dir" jit "$m_jit" yes "$(client_id "$(entry "$main_dir" pinned 3 token)")"

  # ---- N8 ----
  say "N8: JIT under a disabled profile"
  profile_disabled="$jit_profile"
  dmq 'mutation($t:String!){updateProvisioningProfile(token:$t,request:{enabled:false}){token}}' "$(jq -n --arg t "$jit_profile" '{t:$t}')" >/dev/null ||
    fail "could not disable profile $jit_profile"
  attempt_entry "N8 JIT certificate under a disabled profile" jit_provisioning_failed deny "$main_dir" jit $((m_jit + 1)) yes
  dmq 'mutation($t:String!){updateProvisioningProfile(token:$t,request:{enabled:true}){token}}' "$(jq -n --arg t "$jit_profile" '{t:$t}')" >/dev/null ||
    fail "could not re-enable profile $jit_profile"
  profile_disabled=""
  attempt_entry "N8 control: the SAME certificate registers once the profile is enabled again" jit_created admit "$main_dir" jit $((m_jit + 1)) yes

  # ---- N9 ----
  say "N9: the tenant's registration budget (burst $jit_burst) exceeded"
  local created limited busy b9 a9 refused_exists=0 admitted_missing=0 rc9
  b9="$(dm_snap)"
  for ((i = 0; i < n9; i++)); do
    tok="$(entry "$main_dir" jit $((m_jit + 2 + i)) token)"
    val="$(next_value)"
    if ((i == n9 - 1)); then snap_before="$(dm_snap)"; fi
    pub_x509 "/work/$main_dir/$(entry "$main_dir" jit $((m_jit + 2 + i)) certFile)" "/work/$main_dir/$(entry "$main_dir" jit $((m_jit + 2 + i)) keyFile)" "$(client_id "$tok")" "$tok" "$val"
    rc9="$A_RC"
    if ((rc9 == 0)); then
      [[ "$(device_exists "$tok")" == 1 ]] || admitted_missing=$((admitted_missing + 1))
    else
      [[ "$(device_exists "$tok")" == 0 ]] || refused_exists=$((refused_exists + 1))
    fi
  done
  # The last attempt is judged as a record: by now the bucket is empty and it refills at 0.001 per second.
  attempt_record jit_rate_limited deny "$tok" yes "$val"
  a9="$(dm_snap)"
  created="$(snap_delta "$b9" "$a9" jit_created)"
  limited="$(snap_delta "$b9" "$a9" jit_rate_limited)"
  busy="$(snap_delta "$b9" "$a9" jit_busy)"
  out="$(check_refused)" || fail "N9 last attempt: $out"
  pass "N9: $(head -n1 <<<"$out")"
  expect_red "N9: the admit checker on the last attempt" "connect events" no check_admitted
  ((limited >= 1)) || fail "N9: jit_rate_limited did not rise (created $created, busy $busy) after $n9 attempts against a burst of $jit_burst"
  ((created + limited == n9)) || fail "N9: $n9 attempts but created $created + rate-limited $limited (busy $busy): something else answered some"
  ((refused_exists == 0 && admitted_missing == 0)) || fail "N9: $refused_exists refused device(s) exist, $admitted_missing admitted device(s) missing"
  pass "N9: $n9 first connects against the tenant's burst of $jit_burst: jit_created +$created, jit_rate_limited +$limited; every refused device is absent, every admitted one exists"
  token_alive "after the budget was spent"
}

# dm_gauge <metric> prints "<pod> <value>" for each live device-management pod, FAILING when a pod does
# not export the series: reading an absent gauge as zero would let a pin that was never wired look
# like a pin that holds no servers (and the reverse).
dm_gauge() {
  local pod body v
  for pod in $(dm_pods); do
    body="$(k get --raw "/api/v1/namespaces/$namespace/pods/$pod:$dm_port/proxy/metrics")" ||
      fail "could not read the metrics of $pod"
    v="$(awk -v m="$1" '/^#/ { next } { i = index($1, "{"); nm = (i ? substr($1, 1, i - 1) : $1); if (nm ~ (m "$")) { v = $NF; found = 1 } } END { if (!found) exit 3; print v }' <<<"$body")" ||
      fail "$pod exports no $1 series: the issuer pin was never wired"
    printf '%s %s\n' "$pod" "$v"
  done
}

# check_pin_state <connected 0|1> <servers> <broker pods> <issuer_unpinned> <issuer_pin_unavailable>:
# a checker (see CHECKERS) over what one device-management pod reports about its issuer pin.
check_pin_state() {
  local -a tags=()
  [[ "$1" == 1 ]] || { tags+=(connected); echo "  connected: callout_issuer_pin_connected is $1: the pin's system-account connection is down, so every certificate connect is refused"; }
  (($2 >= $3 && $2 > 0)) || { tags+=(servers); echo "  servers: the pin holds $2 server id(s) but the broker runs $3 pod(s): it cannot vouch for a genuine request"; }
  (($4 == 0)) || { tags+=(unpinned); echo "  unpinned: $4 request(s) were refused as not signed by a live broker server, and this rig sends no forged request"; }
  (($5 == 0)) || { tags+=(unavailable); echo "  unavailable: $5 certificate connect(s) were refused because the pin could not vouch for the server ids"; }
  if ((${#tags[@]})); then echo "RED: ${tags[*]}"; return 1; fi
  echo GREEN
}

step6_issuer_pin() {
  say "STEP 6 / N10: the issuer pin held for every genuine request"
  local snap pod conn servers unpinned unavail out n=0
  snap="$(dm_snap)"
  unpinned="$(snap_get "$snap" issuer_unpinned)"
  unavail="$(snap_get "$snap" issuer_pin_unavailable)"
  # issuer_pin_pending is a genuine request refused for want of a ping (a broker pod that joined during a
  # roll, a rate-limited re-ping): the device retried and got in. It is reported, never red.
  pending="$(snap_get "$snap" issuer_pin_pending)"
  # The per-pod gauges are paired by pod; the counters are summed over pods, which is the stricter reading.
  while read -r pod conn; do
    servers="$(dm_gauge callout_issuer_pin_servers | awk -v p="$pod" '$1 == p { print $2 }')"
    out="$(check_pin_state "${conn%.*}" "${servers%.*}" "${#broker_pods[@]}" "$unpinned" "$unavail")" ||
      fail "N10: device-management pod $pod: $out"
    pass "N10: $pod holds ${servers%.*} server id(s) of ${#broker_pods[@]} broker pod(s), its system connection is up, and no genuine request was refused by the pin (issuer_unpinned=$unpinned issuer_pin_unavailable=$unavail; issuer_pin_pending=$pending is tolerated: those devices retried)"
    n=$((n + 1))
  done < <(dm_gauge callout_issuer_pin_connected)
  ((n > 0)) || fail "N10: no device-management pod reported the pin"
}

# wait_connz <client id> <count> <seconds>: wait until the broker holds exactly that many sessions of the id.
wait_connz() {
  local deadline=$((SECONDS + $3)) n
  while ((SECONDS < deadline)); do
    n="$(connz_count "$1")"
    [[ "$n" == "$2" ]] && return 0
    sleep 2
  done
  return 1
}

# ---------------------------------------------------------------------------
# Step 7: OCSP against a live responder (N11-N17)
# ---------------------------------------------------------------------------

# The OCSP part of a record: x509_ocsp_unavailable_allowed_total before and after (O_BEFORE,
# O_AFTER, "reason=count" lines), how many requests for the certificate's serial the responder
# logged meanwhile (O_ASKED), and what the case expects: the one "admitted although unanswered"
# reason that must rise ("-": none may move) and whether the responder must have been asked.
O_BEFORE=""
O_AFTER=""
O_ASKED=0
O_WANT_REASON="-"
O_WANT_ASKED=yes
# snap_has <snapshot> <name>: the snapshot carries a line for that name.
snap_has() { awk -F= -v r="$2" '$1 == r { f = 1 } END { exit !f }' <<<"$1"; }
set_trace() { O_BEFORE="$1"; O_AFTER="$2"; O_ASKED="$3"; O_WANT_REASON="$4"; O_WANT_ASKED="$5"; }

# check_ocsp_trace: a checker (see CHECKERS) over the OCSP part of a record. asked: the responder
# saw (or, when none was to reach it, did not see) a request for the certificate; allowed: exactly
# the expected reason rose on x509_ocsp_unavailable_allowed_total, or nothing moved when none was
# expected. A connect admitted with no usable answer is ONLY visible here: the broker, the
# admission counter and the store all read exactly as a checked, good connect does.
check_ocsp_trace() {
  local -a tags=()
  local r d moved="" others=""
  # unread: the counter was not read in full at both ends. Every reason is registered at zero, so a
  # real read always names unreachable, and the same reasons at both ends; a side that is empty or
  # names other reasons is a read that failed (or lost a series) and was not stopped, and "nothing
  # moved" would then be a guess about a reason never measured.
  snap_has "$O_BEFORE" unreachable && snap_has "$O_AFTER" unreachable &&
    [[ "$(awk -F= '{ print $1 }' <<<"$O_BEFORE" | sort)" == "$(awk -F= '{ print $1 }' <<<"$O_AFTER" | sort)" ]] ||
    { tags+=(unread); echo "  unread: x509_ocsp_unavailable_allowed_total was not read before and after (before: '${O_BEFORE:0:40}', after: '${O_AFTER:0:40}')"; }
  if [[ "$O_WANT_ASKED" == yes ]]; then
    ((O_ASKED >= 1)) || { tags+=(asked); echo "  asked: the responder never saw a request for this certificate"; }
  else
    ((O_ASKED == 0)) || { tags+=(asked); echo "  asked: the responder answered $O_ASKED request(s) for this certificate, and none was to reach it"; }
  fi
  while read -r r; do
    [[ -n "$r" ]] || continue
    d="$(snap_delta "$O_BEFORE" "$O_AFTER" "$r")"
    ((d == 0)) && continue
    moved+="$r+$d "
    [[ "$r" == "$O_WANT_REASON" ]] || others+="$r+$d "
  done < <(awk -F= '{ print $1 }' <<<"$O_AFTER")
  if [[ "$O_WANT_REASON" == "-" ]]; then
    [[ -z "$moved" ]] || { tags+=(allowed); echo "  allowed: x509_ocsp_unavailable_allowed_total moved (${moved% }): the connect was admitted WITHOUT a usable answer"; }
  else
    (($(snap_delta "$O_BEFORE" "$O_AFTER" "$O_WANT_REASON") >= 1)) && [[ -z "$others" ]] ||
      { tags+=(allowed); echo "  allowed: wanted x509_ocsp_unavailable_allowed_total{reason=\"$O_WANT_REASON\"} to rise alone, saw ${moved:-nothing move}"; }
  fi
  if ((${#tags[@]})); then echo "RED: ${tags[*]}"; return 1; fi
  echo "responder asked $O_ASKED time(s); x509_ocsp_unavailable_allowed_total ${moved:-unmoved}"
  echo GREEN
}

# ocsp_offline_controls: the OCSP trace checker on fabricated records.
ocsp_offline_controls() {
  local b=$'unknown_cert=0\nunverified=0\nunreachable=0\nblocked=0' out
  set_trace "$b" "$b" 1 - yes
  out="$(check_ocsp_trace)" || fail "check_ocsp_trace rejects a checked, good connect: $out"
  note "a checked good connect: accepted by check_ocsp_trace"
  set_trace "$b" $'unknown_cert=2\nunverified=0\nunreachable=0\nblocked=0' 1 unknown_cert yes
  out="$(check_ocsp_trace)" || fail "check_ocsp_trace rejects an unknown answer counted as unknown_cert: $out"
  note "an unknown answer, admitted and counted: accepted by check_ocsp_trace"
  set_trace "$b" "$b" 0 - yes
  expect_red "a good connect the responder never saw" "asked" yes check_ocsp_trace
  set_trace "$b" $'unknown_cert=1\nunverified=0\nunreachable=0\nblocked=0' 1 - yes
  expect_red "a connect admitted on no usable answer, judged as a good one" "allowed" yes check_ocsp_trace
  set_trace "$b" $'unknown_cert=0\nunverified=1\nunreachable=0\nblocked=0' 1 unknown_cert yes
  expect_red "an unknown answer counted as unverified" "allowed" yes check_ocsp_trace
  set_trace "$b" $'unknown_cert=1\nunverified=1\nunreachable=0\nblocked=0' 1 unknown_cert yes
  expect_red "the right reason AND another one" "allowed" yes check_ocsp_trace
  set_trace "$b" $'unknown_cert=0\nunverified=0\nunreachable=0\nblocked=1' 1 blocked no
  expect_red "a refused dial whose responder was nonetheless asked" "asked" yes check_ocsp_trace
  set_trace "$b" "" 1 - yes
  expect_red "a good connect whose AFTER read of the counter came back empty" "unread" yes check_ocsp_trace
  set_trace "" "$b" 1 - yes
  expect_red "a good connect whose BEFORE read of the counter came back empty" "unread" yes check_ocsp_trace
  set_trace "$b" $'unverified=0\nunreachable=0\nblocked=0' 1 - yes
  expect_red "a good connect whose AFTER read lost a series (unknown_cert)" "unread" yes check_ocsp_trace
}

ocsp_summary=""
ocsp_n16=""
ocsp_dir=""
ocsp_private_ip=""
declare -A ocsp_serial=()

# ocsp_asked <serial> prints how many requests for that serial the responder has logged.
ocsp_asked() {
  [[ -f "$ocsp_dir/requests.log" ]] || { echo 0; return; }
  grep -c "\"serial\":\"$1\"" "$ocsp_dir/requests.log" || true
}
# ocsp_mark <serial> <good|revoked|unknown>: what the responder says about the serial from now on.
ocsp_mark() {
  local f="$ocsp_dir/status.json"
  [[ -s "$f" ]] || echo '{}' >"$f"
  jq --arg s "$1" --arg v "$2" '.[$s] = $v' "$f" >"$f.tmp" && mv "$f.tmp" "$f"
}
ocsp_snap() { dm_counter x509_ocsp_unavailable_allowed_total reason unreachable; }

# ocsp_responder_up starts the responder in its container and waits until it answers from the host.
ocsp_responder_up() {
  dk exec -d "$ocsp_container" sh -c 'exec /work/ocspresponder -listen :8080 -issuer /work/ocsp/root.pem -issuer-key /work/ocsp/root.key -wrong-cert /work/ocsp/wrong.pem -wrong-key /work/ocsp/wrong.key -status /work/ocsp/status.json -log /work/ocsp/requests.log >>/work/ocsp/responder.out 2>&1'
  local deadline=$((SECONDS + 30))
  until curl -fsS --max-time 3 "http://127.0.0.1:$ocsp_host_port/healthz" >/dev/null 2>&1; do
    ((SECONDS < deadline)) || fail "the OCSP responder did not come up: $(tail -3 "$ocsp_dir/responder.out" 2>/dev/null)"
    sleep 1
  done
}
# ocsp_responder_down stops it and waits until nothing answers.
ocsp_responder_down() {
  dk exec "$ocsp_container" pkill -f /work/ocspresponder || true
  local deadline=$((SECONDS + 30))
  while curl -fsS --max-time 3 "http://127.0.0.1:$ocsp_host_port/healthz" >/dev/null 2>&1; do
    ((SECONDS < deadline)) || fail "the OCSP responder would not stop"
    sleep 1
  done
}

# ocsp_container_up: the responder's network (a NON-private subnet the kind node joins) and its
# container, which also joins the kind network for N17.
ocsp_container_up() {
  if ! dk network inspect "$ocsp_net" >/dev/null 2>&1; then
    dk network create --subnet "$ocsp_subnet" --label "devicechain.io/x509-rig=$cluster" "$ocsp_net" >/dev/null
  fi
  [[ "$(dk network inspect "$ocsp_net" -f '{{index .Labels "devicechain.io/x509-rig"}}')" == "$cluster" ]] ||
    fail "docker network $ocsp_net exists and is not this rig's; pick DC_X509_RIG_OCSP_NET"
  if ! dk inspect "$cluster-control-plane" -f '{{json .NetworkSettings.Networks}}' | jq -e --arg n "$ocsp_net" 'has($n)' >/dev/null; then
    dk network connect "$ocsp_net" "$cluster-control-plane"
  fi
  dk rm -f "$ocsp_container" >/dev/null 2>&1 || true
  dk run -d --init --name "$ocsp_container" --network "$ocsp_net" --ip "$ocsp_ip" -p "127.0.0.1:$ocsp_host_port:8080" \
    --label devicechain.io/x509-rig="$cluster" -v "$(winpath "$work"):/work" \
    --entrypoint /bin/sleep "$device_image" infinity >/dev/null
  dk network connect kind "$ocsp_container"
  ocsp_private_ip="$(dk inspect "$ocsp_container" -f '{{with index .NetworkSettings.Networks "kind"}}{{.IPAddress}}{{end}}')"
  [[ -n "$ocsp_private_ip" ]] || fail "the responder container has no address on the kind network"
}

# ocsp_leaf <index> <good|revoked|unknown>: an openssl leaf under the OCSP CA for simulator name
# <index> (its CN, so its derived token), written over the simulator's files, with a serial of
# its own the responder is told about ("unknown" is told nothing: a serial it has never heard of).
ocsp_leaf() {
  local i="$1" serial cn
  serial="1$(openssl rand -hex 7)"
  cn="$(entry pki-ocsp jit "$i" externalId)"
  mk_key "$work/pki-ocsp/$(entry pki-ocsp jit "$i" keyFile)"
  ossl req -new -key "$work/pki-ocsp/$(entry pki-ocsp jit "$i" keyFile)" -subj "/CN=$cn" -out "$ocsp_dir/leaf.csr" 2>/dev/null
  printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=clientAuth\n' >"$ocsp_dir/leaf.ext"
  ossl x509 -req -in "$ocsp_dir/leaf.csr" -CA "$ocsp_dir/root.pem" -CAkey "$ocsp_dir/root.key" -set_serial "0x$serial" \
    -days 30 -sha256 -extfile "$ocsp_dir/leaf.ext" -out "$work/pki-ocsp/$(entry pki-ocsp jit "$i" certFile)" 2>/dev/null
  [[ "$2" == unknown ]] || ocsp_mark "$serial" "$2"
  ocsp_serial[$i]="$serial"
}

# ocsp_probe <cert> <path> <must match> <must NOT match> <label>: OpenSSL's own OCSP client asks
# the responder from the host; the instrument is believed only when an independent client reads it
# the way the case says.
ocsp_probe() {
  local out
  out="$(ossl ocsp -issuer "$ocsp_dir/root.pem" -cert "$1" -CAfile "$ocsp_dir/root.pem" -no_nonce \
    -url "http://127.0.0.1:$ocsp_host_port$2" 2>&1 || true)"
  grep -Eq "$3" <<<"$out" || fail "instrument, $5: openssl did not read /$2 as it must (/$3/): $out"
  ! grep -Eq "$4" <<<"$out" || fail "instrument, $5: openssl read /$2 as it must NOT (/$4/): $out"
  pass "instrument, $5: openssl reads ${2}: $(tr '\n' ' ' <<<"$out" | cut -c1-150)"
}

# ocsp_anchor <mode> [url]: switch the anchor, then read it back.
ocsp_anchor_token=x509rig-ocsp-ca
ocsp_anchor() {
  local got
  dmq 'mutation($t:String!,$r:DeviceTrustAnchorUpdateRequest!){updateDeviceTrustAnchor(token:$t,request:$r){token}}' \
    "$(jq -n --arg t "$ocsp_anchor_token" --arg m "$1" --arg u "${2:-}" '{t:$t,r:({ocspMode:$m}+(if $u == "" then {} else {ocspResponderUrl:$u} end))}')" >/dev/null ||
    fail "could not set anchor $ocsp_anchor_token to $1 ${2:-}"
  got="$(dmq 'query($t:[String!]!){deviceTrustAnchorsByToken(tokens:$t){ocspMode ocspResponderUrl}}' "$(jq -n --arg t "$ocsp_anchor_token" '{t:[$t]}')" |
    jq -r '.deviceTrustAnchorsByToken[0] | "\(.ocspMode) \(.ocspResponderUrl)"')"
  [[ "$got" == "$1 ${2:-}"* ]] || fail "anchor $ocsp_anchor_token reads back '$got', wanted '$1 ${2:-}'"
  note "anchor $ocsp_anchor_token: ocspMode $got"
}

# ocsp_attempt <label> <expect result> <admit|deny> <want reason|-> <want asked yes|no> <leaf index>
# presents an OCSP leaf, judges the record with the admission checker AND the OCSP trace checker,
# and shows the check that guards the case red on the very same record: the admit checker on a
# refusal, the GOOD device's trace on an admission the responder never vouched for.
ocsp_attempt() {
  local label="$1" expect="$2" kind="$3" wreason="$4" wasked="$5" i="$6" tok serial val out tr ob oa a0 a1
  tok="$(entry pki-ocsp jit "$i" token)"
  serial="${ocsp_serial[$i]}"
  val="$(next_value)"
  snap_before="$(dm_snap)"
  ob="$(ocsp_snap)"
  a0="$(ocsp_asked "$serial")"
  pub_x509 "/work/pki-ocsp/$(entry pki-ocsp jit "$i" certFile)" "/work/pki-ocsp/$(entry pki-ocsp jit "$i" keyFile)" "$(client_id "$tok")" "$tok" "$val"
  attempt_record "$expect" "$kind" "$tok" yes "$val"
  # Each read is ASSIGNED on its own line: a read that fails inside an argument's command
  # substitution ends only that subshell, and an empty "after" would then read as nothing moved.
  oa="$(ocsp_snap)"
  a1="$(ocsp_asked "$serial")"
  set_trace "$ob" "$oa" $((a1 - a0)) "$wreason" "$wasked"
  if [[ "$kind" == admit ]]; then
    out="$(check_admitted)" || fail "$label: $out ${A_OUT:+(mosquitto_pub: ${A_OUT:0:200})}"
  else
    out="$(check_refused)" || fail "$label: $out ${A_OUT:+(mosquitto_pub: ${A_OUT:0:200})}"
  fi
  tr="$(check_ocsp_trace)" || fail "$label (serial $serial): $tr"
  pass "$label: $(head -n1 <<<"$out"); $(head -n1 <<<"$tr")"
  if [[ "$kind" == deny ]]; then
    expect_red "$label: the admit checker on this very record" "connect events" no check_admitted
  elif [[ "$wreason" != "-" ]]; then
    local need=allowed
    [[ "$wasked" == yes ]] || need="allowed asked"
    O_WANT_REASON="-"
    O_WANT_ASKED=yes
    expect_red "$label: the GOOD device's OCSP trace on this very record" "$need" yes check_ocsp_trace
  fi
}

# ocsp_switch_state prints on, off or unknown: what device-management's own start-up log says.
ocsp_switch_state() {
  local logs
  logs="$(k -n "$namespace" logs deploy/device-management --tail=-1 2>/dev/null || true)"
  if grep -q 'checks OCSP for the anchors that ask for it' <<<"$logs"; then echo on
  elif grep -q 'OCSP is switched off' <<<"$logs"; then echo off
  else echo unknown; fi
}

# ocsp_switch_on: the documented way. The instance switch is fixed configuration, so it is an
# operator's `dcctl upgrade --values` that rolls device-management onto it.
ocsp_switch_on() {
  cat >"$work/ocsp-values.yaml" <<'EOF'
functionalAreas:
  device-management:
    config:
      callout:
        ocsp:
          enabled: true
          allowRequireCurrent: true
EOF
  local -a args=(upgrade local "$instance" --values "$(winpath "$work/ocsp-values.yaml")")
  if [[ -n "$release" ]]; then args+=(--version "$release"); else args+=(--registry localhost:5000 --version "$(tr -d '\r\n' <"$state_dir/image-tag")"); fi
  say "turning the instance's OCSP switch on: dcctl ${args[*]}"
  ensure_dcctl
  "$dcctl" "${args[@]}" 2>&1 | redact
  k -n "$namespace" rollout status deploy/device-management --timeout=600s
  local st
  st="$(ocsp_switch_state)"
  [[ "$st" == on ]] || fail "after the upgrade device-management's log does not say OCSP is checked (state: $st)"
  pass "device-management restarted with deviceManagement.callout.ocsp.enabled: its log says it checks OCSP for the anchors that ask"
}

# ocsp_warm <leaf index>: a device that is already registered reconnects until the broker takes it,
# after a restart (the callout's issuer pin re-learns the broker's servers).
ocsp_warm() {
  local i="$1" tok deadline=$((SECONDS + 120))
  tok="$(entry pki-ocsp jit "$i" token)"
  while :; do
    pub_x509 "/work/pki-ocsp/$(entry pki-ocsp jit "$i" certFile)" "/work/pki-ocsp/$(entry pki-ocsp jit "$i" keyFile)" "$(client_id "$tok")" "$tok" "$(next_value)"
    ((A_RC == 0)) && break
    ((SECONDS < deadline)) || fail "after the upgrade the registered device $tok cannot connect: $A_OUT"
    sleep 5
  done
  note "the platform takes certificate connects again (device $tok reconnected)"
}

step7_ocsp() {
  say "STEP 7: OCSP against a live responder (N11-N17)"
  local saved_tenant="$tenant" saved_email="$member_email" saved_token="$tenant_token" saved_pw="$member_password" out i
  tenant="${tenant}o"
  member_email="x509-rig@$tenant.test"
  make_tenant 100
  write_handshake
  note "OCSP runs in tenant $tenant (its own registration budget)"

  # ---- the PKI, the names, the responder ----
  ocsp_dir="$work/ocsp"
  mkdir -p "$ocsp_dir"
  echo '{}' >"$ocsp_dir/status.json"
  : >"$ocsp_dir/requests.log"
  mk_root "$ocsp_dir"
  # N13's signer: a delegated responder (OCSPSigning, digitalSignature, in its validity) issued by
  # a FAKE CA whose subject DN is byte-for-byte the real CA's, under a key of its own. Everything a
  # verifier can check by NAME says the real CA issued it, so the forgery is caught only by checking
  # the delegate's signature against the real CA's key: that is the check this plants against.
  local ca_dn
  ca_dn="$(MSYS2_ARG_CONV_EXCL='*' openssl x509 -in "$(winpath "$ocsp_dir/root.pem")" -noout -subject -nameopt compat)"
  ca_dn="${ca_dn#subject=}"
  [[ "$ca_dn" == /CN=* ]] || fail "cannot read the OCSP CA's subject DN (got '$ca_dn')"
  mk_key "$ocsp_dir/fakeca.key"
  ossl req -x509 -new -key "$ocsp_dir/fakeca.key" -subj "$ca_dn" -days 30 -sha256 \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:1" -addext "keyUsage=critical,keyCertSign,cRLSign" -out "$ocsp_dir/fakeca.pem" 2>/dev/null
  mk_key "$ocsp_dir/wrong.key"
  ossl req -new -key "$ocsp_dir/wrong.key" -subj "/CN=x509-rig OCSP delegate" -out "$ocsp_dir/wrong.csr" 2>/dev/null
  printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=OCSPSigning\n' >"$ocsp_dir/wrong.ext"
  ossl x509 -req -in "$ocsp_dir/wrong.csr" -CA "$ocsp_dir/fakeca.pem" -CAkey "$ocsp_dir/fakeca.key" -set_serial "0x$(openssl rand -hex 8)" \
    -days 30 -sha256 -extfile "$ocsp_dir/wrong.ext" -out "$ocsp_dir/wrong.pem" 2>/dev/null
  # The plant is worth something only if the delegate's issuer is byte-for-byte the real CA's subject.
  [[ "$(ossl x509 -in "$ocsp_dir/wrong.pem" -noout -issuer -nameopt RFC2253)" == "issuer=$(ossl x509 -in "$ocsp_dir/root.pem" -noout -subject -nameopt RFC2253 | sed 's/^subject=//')" ]] ||
    fail "the N13 delegate does not name the real CA as its issuer"
  local n_ocsp=17
  simp pki-ocsp x5o- x509rig-ocsp-unused --x509-pinned 0 --x509-jit "$n_ocsp" --x509-mint-only 2>>"$work/sim.err" || fail "could not mint the OCSP names"
  # Leaves 0-2 are the instrument's probes; 3 onward meet the platform.
  ocsp_leaf 0 good; ocsp_leaf 1 revoked; ocsp_leaf 2 unknown
  local L_OFF=3 E_GOOD=4 E_REV=5 E_UNK=6 E_WK=7 E_STALE=8 E_PRIV=9 E_DOWN=10 R_GOOD=11 R_REV=12 R_UNK=13 R_WK=14 R_STALE=15 R_DOWN=16
  ocsp_leaf $L_OFF good; ocsp_leaf $E_GOOD good; ocsp_leaf $E_REV revoked; ocsp_leaf $E_UNK unknown; ocsp_leaf $E_WK good
  ocsp_leaf $E_STALE good; ocsp_leaf $E_PRIV good; ocsp_leaf $E_DOWN good
  ocsp_leaf $R_GOOD good; ocsp_leaf $R_REV revoked; ocsp_leaf $R_UNK unknown; ocsp_leaf $R_WK good; ocsp_leaf $R_STALE good; ocsp_leaf $R_DOWN good

  say "the OCSP responder: built from this tree, on $ocsp_net ($ocsp_subnet) at $ocsp_ip, loopback port $ocsp_host_port"
  (cd "$repo_root/backend/tools/ocspresponder" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$work/ocspresponder" .) ||
    fail "could not build the OCSP responder"
  ocsp_container_up
  dk exec "$ocsp_container" chmod 0755 /work/ocspresponder
  ocsp_responder_up
  note "responder up; its kind-network (private) address is $ocsp_private_ip"

  # ---- the instrument, judged by OpenSSL ----
  say "the instrument: OpenSSL's OCSP client reads each planted answer the way the case says"
  local p0 p1 p2
  p0="$work/pki-ocsp/$(entry pki-ocsp jit 0 certFile)"; p1="$work/pki-ocsp/$(entry pki-ocsp jit 1 certFile)"; p2="$work/pki-ocsp/$(entry pki-ocsp jit 2 certFile)"
  ocsp_probe "$p0" /ok ': good' 'revoked|unknown|[Vv]erify [Ff]ailure|times invalid' "a good serial on /ok"
  grep -q 'Response verify OK' <<<"$(ossl ocsp -issuer "$ocsp_dir/root.pem" -cert "$p0" -CAfile "$ocsp_dir/root.pem" -no_nonce -url "http://127.0.0.1:$ocsp_host_port/ok" 2>&1)" ||
    fail "instrument: openssl does not verify a /ok answer against the CA"
  ocsp_probe "$p1" /ok ': revoked' ': good|[Vv]erify [Ff]ailure' "a revoked serial on /ok"
  ocsp_probe "$p2" /ok ': unknown' ': good|: revoked' "a serial the responder never heard of on /ok"
  ocsp_probe "$p0" /wrongkey '[Vv]erify [Ff]ailure' 'Response verify OK' "a good serial on /wrongkey"
  ocsp_probe "$p0" /stale '[Ss]tatus times invalid' 'Response [Vv]erify [Ff]ailure' "a good serial on /stale"

  # ---- the anchor and its certificate profile ----
  dmq 'mutation($r:DeviceTrustAnchorCreateRequest!){createDeviceTrustAnchor(request:$r){token}}' \
    "$(jq -n --rawfile p "$ocsp_dir/root.pem" --arg t "$ocsp_anchor_token" '{r:{token:$t,name:"OCSP rig CA",certificatePem:$p,enabled:true}}')" >/dev/null ||
    fail "createDeviceTrustAnchor $ocsp_anchor_token"
  dmq 'mutation($r:ProvisioningProfileCreateRequest){createProvisioningProfile(request:$r){profile{token}}}' \
    "$(jq -n --arg ty "$dtype" --arg a "$ocsp_anchor_token" '{r:{token:"x509rig-jit-ocsp",name:"X509 rig JIT (OCSP CA)",provisionKey:"x509-rig-jit-key-ocsp",strategy:"ALLOW_NEW",deviceTypeToken:$ty,credentialType:"X509_CERTIFICATE",enabled:true,trustAnchorToken:$a}}')" >/dev/null ||
    fail "createProvisioningProfile x509rig-jit-ocsp"
  local url_ok="http://$ocsp_ip:8080/ok"

  # ---- with the instance switch off ----
  local st
  st="$(ocsp_switch_state)"
  note "device-management's OCSP switch, by its own log: $st"
  if [[ "$st" == off ]]; then
    say "N16: the instance switch is off: the API must refuse to switch an anchor on"
    if dmq 'mutation($t:String!,$r:DeviceTrustAnchorUpdateRequest!){updateDeviceTrustAnchor(token:$t,request:$r){token}}' \
      "$(jq -n --arg t "$ocsp_anchor_token" --arg u "$url_ok" '{t:$t,r:{ocspMode:"ENFORCE_KNOWN",ocspResponderUrl:$u}}')" >/dev/null 2>"$work/n16.err"; then
      fail "N16: the API switched an anchor to ENFORCE_KNOWN while OCSP is off on the instance"
    fi
    grep -q 'switched off' "$work/n16.err" || fail "N16: the switch was refused, but not because OCSP is off: $(cat "$work/n16.err")"
    pass "N16: ENFORCE_KNOWN refused while the instance switch is off (\"$(jq -r '.[0].message' "$work/n16.err" | cut -c1-120)...\")"
    ocsp_n16=ran
  else
    warn "N16 NOT RUN: device-management's OCSP switch is already '$st' (an earlier run turned it on); it needs a fresh instance"
    ocsp_n16=skipped
  fi
  say "an anchor whose mode is OFF: a device is admitted and the responder is never asked"
  ocsp_attempt "OFF: a device under an anchor whose ocspMode is OFF" jit_created admit - no $L_OFF
  O_WANT_ASKED=yes
  expect_red "OFF: the GOOD device's OCSP trace on this very record (the responder was not consulted)" "asked" yes check_ocsp_trace

  # ---- the switch on, the documented way ----
  [[ "$st" == on ]] || ocsp_switch_on
  discover
  ocsp_warm $L_OFF

  # ---- ENFORCE_KNOWN ----
  say "ENFORCE_KNOWN at $url_ok"
  ocsp_anchor ENFORCE_KNOWN "$url_ok"
  ocsp_attempt "GOOD (ENFORCE_KNOWN)" jit_created admit - yes $E_GOOD
  ocsp_attempt "N11 REVOKED (ENFORCE_KNOWN)" ocsp_revoked deny - yes $E_REV
  ocsp_attempt "N12 UNKNOWN (ENFORCE_KNOWN admits, counted)" jit_created admit unknown_cert yes $E_UNK
  ocsp_anchor ENFORCE_KNOWN "http://$ocsp_ip:8080/wrongkey"
  ocsp_attempt "N13 WRONG KEY (ENFORCE_KNOWN admits, counted)" jit_created admit unverified yes $E_WK
  ocsp_anchor ENFORCE_KNOWN "http://$ocsp_ip:8080/stale"
  ocsp_attempt "N14 STALE (ENFORCE_KNOWN admits, counted)" jit_created admit unverified yes $E_STALE
  ocsp_anchor ENFORCE_KNOWN "http://$ocsp_private_ip:8080/ok"
  ocsp_attempt "N17 PRIVATE ADDRESS (the egress boundary refuses the dial)" jit_created admit blocked no $E_PRIV
  ocsp_anchor ENFORCE_KNOWN "$url_ok"
  ocsp_responder_down
  ocsp_attempt "N15 DOWN (ENFORCE_KNOWN fails open, counted)" jit_created admit unreachable no $E_DOWN
  ocsp_responder_up

  # ---- REQUIRE_CURRENT ----
  say "REQUIRE_CURRENT at $url_ok"
  ocsp_anchor REQUIRE_CURRENT "$url_ok"
  ocsp_attempt "GOOD (REQUIRE_CURRENT)" jit_created admit - yes $R_GOOD
  ocsp_attempt "N11 REVOKED (REQUIRE_CURRENT)" ocsp_revoked deny - yes $R_REV
  ocsp_attempt "N12 UNKNOWN (REQUIRE_CURRENT refuses)" ocsp_unavailable deny - yes $R_UNK
  ocsp_anchor REQUIRE_CURRENT "http://$ocsp_ip:8080/wrongkey"
  ocsp_attempt "N13 WRONG KEY: a forged GOOD (REQUIRE_CURRENT refuses)" ocsp_unavailable deny - yes $R_WK
  ocsp_anchor REQUIRE_CURRENT "http://$ocsp_ip:8080/stale"
  ocsp_attempt "N14 STALE GOOD (REQUIRE_CURRENT refuses)" ocsp_unavailable deny - yes $R_STALE
  ocsp_anchor REQUIRE_CURRENT "$url_ok"
  ocsp_responder_down
  ocsp_attempt "N15 DOWN (REQUIRE_CURRENT fails closed)" ocsp_unavailable deny - no $R_DOWN
  ocsp_responder_up
  # The responder is back: the device refused while it was down is admitted now, and asked about.
  # A failing responder is skipped for failureTtl (30 s by default) once it has failed often enough;
  # wait that out, so this reads the responder and not the breaker.
  sleep 35
  ocsp_attempt "N15 control: the responder is back, the device refused while it was down is admitted" jit_created admit - yes $R_DOWN

  tenant="$saved_tenant"
  member_email="$saved_email"
  tenant_token="$saved_token"
  member_password="$saved_pw"
  write_handshake
  token_alive "after the OCSP step, in the first tenant"
  local n16_said="the API refused to switch an anchor on while the instance switch was off (N16)"
  [[ "$ocsp_n16" == ran ]] || n16_said="N16 (the API refusing while the switch is off) did NOT run: the switch was already on"
  ocsp_summary="Against a live OCSP responder (step 7): $n16_said; with the switch on, a good certificate was
admitted and checked under ENFORCE_KNOWN and under REQUIRE_CURRENT, and a revoked one refused under both
(N11); an unknown answer (N12), a forged delegate under a same-name fake CA (N13), a stale answer (N14)
and a stopped responder (N15) were each admitted and counted by reason under ENFORCE_KNOWN and refused
under REQUIRE_CURRENT, and a good device was admitted again once the responder was back; a responder
URL at a private address was refused by the egress boundary under ENFORCE_KNOWN (N17, run in that mode
only)."
}

# ---------------------------------------------------------------------------
# run
# ---------------------------------------------------------------------------

cmd_run() {
  need kind; need kubectl; need docker; need jq; need curl; need openssl; need go
  cluster_exists || fail "kind cluster $cluster does not exist; run 'hack/x509-rig.sh up'"
  k get ns "$namespace" >/dev/null 2>&1 || fail "instance $instance is not on $cluster; run 'hack/x509-rig.sh up'"
  ((n_pinned >= 4)) || fail "DC_X509_RIG_PINNED must be at least 4 (N6 uses four devices)"
  ((m_jit >= 1)) || fail "DC_X509_RIG_JIT must be at least 1"
  # A fresh tenant per run: tokens are unique per tenant, so a re-run on the same instance starts clean.
  local run_id
  run_id="$(date -u +%H%M%S)"
  tenant="${DC_X509_RIG_TENANT:-x5$run_id}"
  member_email="x509-rig@$tenant.test"
  say "run $run_id: tenant $tenant on instance $instance (cluster $cluster)"
  run_evidence
  offline_controls
  discover
  step1_broker
  step2_setup
  step3_pinned
  step4_jit
  step5_controls
  step6_issuer_pin
  if [[ "$ocsp_on" == 1 ]]; then
    step7_ocsp
  else
    warn "STEP 7 (OCSP) NOT RUN: DC_X509_RIG_OCSP=$ocsp_on"
  fi
  say "X509 RIG GREEN: on a default instance ${release:+of the published $release images}${release:-built from this tree}, the DeviceChain broker requested client
certificates; $n_pinned pinned devices and $m_jit JIT-registered devices connected and had their telemetry
stored through their certificates while the token device kept working. Nine planted defects (N1-N9)
each turned the platform's own counter, the device's CONNACK and the store red for the reason they were
planted, the checkers were shown to go red on the opposite verdict, and the callout's issuer pin held
the broker's live servers without refusing one genuine request (N10).${ocsp_summary:+
$ocsp_summary}"
}

# run_evidence prints what is under test and refuses an instance whose device-management image is
# not the tag `up` recorded.
run_evidence() {
  local image want_tag running_tag
  image="$(k -n "$namespace" get deploy device-management -o jsonpath='{.spec.template.spec.containers[0].image}')"
  note "git HEAD $(git -C "$repo_root" rev-parse HEAD); $(git -C "$repo_root" status --porcelain | wc -l | tr -d ' ') dirty path(s)"
  note "hack/x509-rig.sh blob $(git -C "$repo_root" hash-object hack/x509-rig.sh)"
  note "device-management image: $image"
  [[ -f "$state_dir/image-tag" ]] || fail "$state_dir/image-tag is missing: this instance was not brought up by this rig's 'up'"
  want_tag="$(tr -d '\r\n' <"$state_dir/image-tag")"
  running_tag="${image%@*}"
  running_tag="${running_tag##*:}"
  [[ "$running_tag" == "$want_tag" ]] ||
    fail "device-management runs tag '$running_tag', but 'up' installed '$want_tag': this is not the build under test"
}

# cmd_run_ocsp: the checkers offline, the broker and the first tenant, then step 7 alone.
cmd_run_ocsp() {
  need kind; need kubectl; need docker; need jq; need curl; need openssl; need go
  cluster_exists || fail "kind cluster $cluster does not exist; run 'hack/x509-rig.sh up'"
  k get ns "$namespace" >/dev/null 2>&1 || fail "instance $instance is not on $cluster; run 'hack/x509-rig.sh up'"
  local run_id
  run_id="$(date -u +%H%M%S)"
  tenant="${DC_X509_RIG_TENANT:-x5$run_id}"
  member_email="x509-rig@$tenant.test"
  say "run $run_id (OCSP only): tenant $tenant on instance $instance (cluster $cluster)"
  run_evidence
  offline_controls
  discover
  step1_broker
  step2_setup
  step7_ocsp
  say "X509 RIG (OCSP) GREEN: $ocsp_summary"
}

rig_cmd="${1:-}"
case "$rig_cmd" in
  up) cmd_up ;;
  run) cmd_run ;;
  all) cmd_up; cmd_run ;;
  ocsp) cmd_run_ocsp ;;
  down) cmd_down ;;
  controls) offline_controls ;;
  *) fail "usage: hack/x509-rig.sh up | run | all | ocsp | down | controls" ;;
esac
