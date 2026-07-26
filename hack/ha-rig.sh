#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The ADR-020 A0 high-availability validation rig.
#
# WHAT THIS GATES
#
# Not the merge. Everything the workstream builds is landable and CI-tested
# without a cluster, and it is. What needs a real multi-node broker is the CLAIM
# — and until this passes, WITH its negative control, nothing in the roadmap, the
# ADRs or the operator-facing docs should say a DeviceChain instance is highly
# available.
#
# The distinction matters because the failure A0 fixes was not a broken
# mechanism. It was a green result that meant nothing: an HA toggle that produced
# three NATS servers, three healthy pods and sixteen single-replica streams. So
# the rig is built around one rule — a check is worth nothing until it has been
# shown to fail.
#
#   hack/ha-rig.sh up        create the 4-node kind cluster and bootstrap --ha
#   hack/ha-rig.sh verify    assert the HA claim from live broker state
#   hack/ha-rig.sh control   THE NEGATIVE CONTROL: same check, non-HA instance,
#                            required to FAIL
#   hack/ha-rig.sh all       up + verify + control
#   hack/ha-rig.sh down      delete both clusters
#
# `all` is the one worth running. `verify` on its own reports on a check whose
# ability to fail has not been demonstrated in this session, which is the exact
# thing A0 is about.
#
# Requires: kind, kubectl, docker, `tofu` OR `terraform` on PATH, and a Go
# toolchain (the script builds dcctl itself).
# Four kind nodes plus the platform is a real load on a developer box; run
# `down` afterwards.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ha_cluster="devicechain-ha"
control_cluster="devicechain-ha-control"
# Instance names, and they are NOT both "default" for a reason that is easy to
# miss: dcctl keeps OpenTofu state per INSTANCE (~/.devicechain/<instance>/infra),
# not per cluster. Two instances of the same name on two clusters share one state
# directory, so bootstrapping the control would reconcile the rig cluster's
# recorded infrastructure against a different cluster entirely. Distinct names
# also keep the rig clear of any `default` instance already on this machine.
instance="${DC_INSTANCE:-harig}"
control_instance="${DC_CONTROL_INSTANCE:-hactl}"
dcctl="$repo_root/backend/cli/build/dcctl"

# Image source. The rig defaults to the published images so a run does not also
# depend on a working local build chain; DC_BUILD=1 switches to --build for
# testing the checked-out tree.
if [[ "${DC_BUILD:-0}" == "1" ]]; then
  image_args=(--build)
else
  image_args=(--version "${DC_VERSION:-v0.8.5}")
fi

say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"
}

build_dcctl() {
  say "building dcctl"
  make -C "$repo_root/backend/cli" build >/dev/null
  [[ -x "$dcctl" ]] || fail "dcctl was not built at $dcctl"
}

# create_cluster is idempotent: an existing cluster of the same name is reused
# rather than recreated, because a full bring-up is long enough that losing one
# to a re-run is its own deterrent to running the rig at all.
create_cluster() {
  local name="$1" config="$2"
  if kind get clusters 2>/dev/null | grep -qx "$name"; then
    say "kind cluster $name already exists; reusing it"
    return
  fi
  say "creating kind cluster $name"
  kind create cluster --name "$name" --config "$config" --wait 120s
}

cmd_up() {
  need kind; need kubectl; need docker
  build_dcctl
  create_cluster "$ha_cluster" "$repo_root/deploy/local/kind-cluster-ha.yaml"

  # The spread constraint is HARD, so the bring-up fails outright rather than
  # degrading if the cluster cannot place one server per node. Prove the cluster
  # can before spending twenty minutes finding out.
  local schedulable
  schedulable=$(kubectl --context "kind-$ha_cluster" get nodes \
    -o jsonpath='{range .items[*]}{.spec.taints[*].effect}{"\n"}{end}' \
    | grep -cv 'NoSchedule\|NoExecute' || true)
  if (( schedulable < 3 )); then
    fail "the rig cluster has $schedulable schedulable node(s); --ha needs 3. kind only
removes the control-plane taint on a SINGLE-node cluster, so three workers are
required — check deploy/local/kind-cluster-ha.yaml"
  fi
  say "$schedulable schedulable nodes"

  say "bootstrapping --ha"
  "$dcctl" bootstrap local "$instance" --ha --yes \
    --kube-context "kind-$ha_cluster" --host localhost --no-tls \
    "${image_args[@]}"
}

cmd_verify() {
  build_dcctl
  say "CHECK A — asserting the HA claim from live broker state"
  # --probe-mqtt because no device has ever connected to this broker, so the
  # $MQTT_* streams do not exist yet and their replica factor — the one
  # nats-server chooses rather than the platform — has not been observable. The
  # probe opens one connection so it is.
  "$dcctl" ha verify --instance "$instance" \
    --kube-context "kind-$ha_cluster" --probe-mqtt \
    || fail "the instance does not hold the replication it declares (see the findings above)"
  say "CHECK A PASSED"
}

# cmd_control is the check on the check, and it is the reason this script exists
# rather than a paragraph in a runbook.
#
# It brings up a SEPARATE single-node instance — genuinely not HA — and runs the
# identical verifier against it demanding R3. If that passes, every green result
# the verifier has ever produced is worthless, and the rig says so in those words.
#
# A separate cluster rather than a second instance on the HA one: the object under
# test is the BROKER, and both instances would share it.
cmd_control() {
  need kind; need kubectl
  build_dcctl
  create_cluster "$control_cluster" "$repo_root/deploy/local/kind-cluster-ha-control.yaml"

  say "bootstrapping the negative control (no --ha, single node)"
  "$dcctl" bootstrap local "$control_instance" --yes \
    --kube-context "kind-$control_cluster" --host localhost --no-tls \
    "${image_args[@]}"

  say "NEGATIVE CONTROL — the same check, against an instance that is NOT replicated"
  # --replicas 3 states the claim the instance does not hold. --expect-fail
  # inverts the exit status: this command succeeds only when the verifier FAILS.
  "$dcctl" ha verify --instance "$control_instance" \
    --kube-context "kind-$control_cluster" --probe-mqtt \
    --replicas 3 --expect-fail \
    || fail "THE NEGATIVE CONTROL DID NOT HOLD. A single-replica instance passed an R3
check, so the verifier is not detecting the state it exists to detect and CHECK A
above proves nothing. Do not record an HA result from this run."
  say "NEGATIVE CONTROL HELD"
}

cmd_down() {
  need kind
  for c in "$ha_cluster" "$control_cluster"; do
    if kind get clusters 2>/dev/null | grep -qx "$c"; then
      say "deleting kind cluster $c"
      kind delete cluster --name "$c"
    fi
  done
}

case "${1:-all}" in
  up) cmd_up ;;
  verify) cmd_verify ;;
  control) cmd_control ;;
  down) cmd_down ;;
  all)
    cmd_up
    cmd_verify
    cmd_control
    say "A0 CHECK A COMPLETE: the instance holds its declared replication, and the
verifier was shown to reject an instance that does not. This is the evidence the
HA claim rests on."
    ;;
  *) fail "unknown command ${1}; try up | verify | control | all | down" ;;
esac
