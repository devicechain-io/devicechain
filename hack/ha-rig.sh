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
#   hack/ha-rig.sh verify    assert the HA claim from live broker (A) and
#                            relational-database (B) state
#   hack/ha-rig.sh control   THE NEGATIVE CONTROLS: the same two checks against a
#                            non-HA instance, both required to FAIL
#   hack/ha-rig.sh all       up + verify + control
#   hack/ha-rig.sh down      delete both clusters
#
# TWO CHECKS, TWO SUBSYSTEMS (ADR-020 A0 and A2.3):
#
#   CHECK A  the broker — JetStream streams, KV buckets, consumer groups, pods
#   CHECK B  the relational database — CloudNativePG instance count, pod spread,
#            the dc-postgresql alias Service, and PostgreSQL's own view of
#            synchronous replication
#
# 🔴 CHECK B DOES NOT COVER TimescaleDB. The event store is still a
# single-instance StatefulSet until A2.4, so a green run here means the broker
# and the control-plane database are replicated — not that the instance is.
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

# The output helpers are defined HERE, above their first use. The DC_VERSION
# branch below calls say(), and a function defined later in the file does not
# exist yet when top-level code runs: under `set -e` that path died on
# "say: command not found" before the rig printed anything at all.
say() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not on PATH"
}

# Image source: HEAD, built from source, BY DEFAULT.
#
# The first draft defaulted to the published release so a run would not also
# depend on a working build chain. That was wrong, and the first live run showed
# why: the checker is built from the working tree and its expectations come from
# HEAD's stream inventory, while v0.8.5 predates the ADR-030 capture stream. The
# check duly reported harig_device-events-capture MISSING — a finding about
# version skew wearing the costume of a replication defect, and it cost real time
# to run down.
#
# A validation rig has to test the code under test. DC_VERSION=<tag> deliberately
# checks a published release instead, which is a different and also useful run —
# just never the default, because the confound is invisible in the output.
if [[ -n "${DC_VERSION:-}" ]]; then
  image_args=(--version "$DC_VERSION")
  say "checking PUBLISHED images $DC_VERSION — note the checker is built from the
working tree, so any stream added since that tag will read as MISSING"
else
  image_args=(--build)
fi

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

  # There is deliberately NO schedulable-node pre-check here. dcctl's own
  # checkHaNodeCapacity already counts them — excluding cordoned and
  # NoSchedule/NoExecute-tainted nodes, which is the subtlety — refuses before
  # anything is provisioned, and is unit-tested. A second copy of that rule in
  # shell is a second place for it to be wrong, and the first draft of this script
  # proved the point: it counted 0 schedulable nodes on a cluster with 3.

  # --no-escrow: this rig's instances exist to be torn down, so there is no root
  # key worth a second copy. Bootstrap escrows by default and REFUSES to run
  # non-interactively without a passphrase, which is the correct default for an
  # instance anyone might keep — and the wrong one for this.
  say "bootstrapping --ha"
  "$dcctl" bootstrap local "$instance" --ha --yes --no-escrow \
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

  # CHECK B — the DATABASE half of the same claim (ADR-020 A2.3).
  #
  # A separate command rather than more assertions inside `ha verify` because the
  # two collect from different subsystems entirely: check A talks JetStream, this
  # talks the Kubernetes API and PostgreSQL. What they share is the discipline —
  # observed state only, vacuity guards, counts printed whether or not it passed,
  # and a negative control below.
  #
  # --require-synchronous is stated here rather than read from the Cluster spec,
  # and that is the whole point of the check. Helm accepts an unknown field and
  # the API server prunes it silently, so a one-character slip in the chart
  # template produces a Cluster whose spec asks for nothing, three healthy pods,
  # and asynchronous replication. A spec-derived check reads that spec and agrees.
  #
  # --instances 3 is STATED here, not left to default. Without it the expected
  # count is read from the same Cluster spec the check is judging, so B1/B2/B7
  # compare the spec against itself and pass trivially. The flag's own help text
  # warns about this, and it was originally wired only on the negative control —
  # which is the half where it is easier to remember and less valuable.
  say "CHECK B — asserting the DATABASE replication claim from live PostgreSQL state"
  "$dcctl" ha verify-db --cluster dc-rdb --instances 3 --require-synchronous \
    --kube-context "kind-$ha_cluster" \
    || fail "the relational database does not hold the replication it declares (see the findings above)"
  say "CHECK B PASSED"
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

  # --compact, and it does NOT weaken the control.
  #
  # It lowers JetStream/KV ceilings and volume sizes and drops the monitoring stack
  # and cert-manager. None of that touches a replica factor, which is the only
  # thing under test here — and it removes the two slowest, most network-dependent
  # components of the bring-up. Three separate runs of this rig were lost to
  # transient failures downloading them (an npm reset, an IPv6 route to
  # charts.jetstack.io), each costing a full bring-up to discover.
  #
  # It does NOT change which services run — that stays on --profile — so the
  # control still exercises the same 10 functional areas creating the same streams
  # and buckets as the HA side.
  say "bootstrapping the negative control (no --ha, single node)"
  "$dcctl" bootstrap local "$control_instance" --yes --compact --no-escrow \
    --kube-context "kind-$control_cluster" --host localhost --no-tls \
    "${image_args[@]}"

  say "NEGATIVE CONTROL — the same check, against an instance that is NOT replicated"
  # --replicas 3 states the claim the instance does not hold. --expect-fail
  # inverts the exit status: this command succeeds only when the verifier FAILS.
  #
  # The settle loop is deliberately LEFT ON here even though a failure is what we
  # want. It re-checks for its full window, which means the control gives this
  # instance every chance to look replicated before the run is recorded as a
  # negative control that held — and removes the one explanation that would
  # otherwise weaken the result, that the check simply ran too early.
  "$dcctl" ha verify --instance "$control_instance" \
    --kube-context "kind-$control_cluster" --probe-mqtt \
    --replicas 3 --expect-fail \
    || fail "THE NEGATIVE CONTROL DID NOT HOLD, or could not run — READ THE OUTPUT ABOVE,
because those are different results and this exit status cannot tell them apart.

  'NEGATIVE CONTROL FAILED: this instance PASSED a check it was expected to fail'
      is the catastrophic one: the verifier does not detect the state it exists to
      detect, so CHECK A proves nothing and no HA result may be recorded from this run.

  anything else (a connection error, a missing Secret, a probe failure) means the
      control did not RUN. That is inconclusive, not a pass and not a failure — fix it
      and re-run rather than reading it either way."
  say "NEGATIVE CONTROL HELD (broker)"

  # The same control for the database half. The control instance's store is a
  # genuine single-instance CloudNativePG Cluster with no synchronous block —
  # which is the SUPPORTED non-HA topology, not a broken one (decision D4). That
  # is what makes it the right subject: it is a real deployment that must fail a
  # three-instance synchronous claim.
  #
  # --instances 3 is load-bearing and easy to leave off. Without it the expected
  # count is read from the live Cluster spec, so a single-instance store expects
  # one instance, has one, and PASSES — a negative control that can never hold.
  # Measured on a probe cluster before this was wired.
  say "NEGATIVE CONTROL — the same database check, against a store that is NOT replicated"
  "$dcctl" ha verify-db --cluster dc-rdb --instances 3 --require-synchronous \
    --kube-context "kind-$control_cluster" --expect-fail \
    || fail "THE DATABASE NEGATIVE CONTROL DID NOT HOLD, or could not run — READ THE OUTPUT
ABOVE, because those are different results and this exit status cannot tell them apart.
The same reading applies as for the broker control above: a PASS where a failure was
expected means CHECK B proves nothing, while a connection or lookup error means the
control never ran and the result is inconclusive."
  say "NEGATIVE CONTROL HELD (database)"

  # A SECOND database control, isolating the synchronous axis — and it is not
  # redundant with the one above, which is over-determined.
  #
  # `--instances 3` against a single-instance store fails on B1, B2, B3, B6, B7
  # and B8 all at once. So that control would still "hold" if every
  # RequireSynchronous code path were deleted: B1/B2/B7 carry it on their own.
  # A regression that killed only the synchronous checks would leave the rig
  # green in both directions, which is the failure mode this whole rig exists to
  # make impossible.
  #
  # Stating the store's ACTUAL instance count removes B1/B2/B7 from the picture,
  # so this can only pass if B3/B6/B8 — the synchronous assertions themselves —
  # still fire. Verified to produce exactly those three and nothing else.
  say "NEGATIVE CONTROL — isolating the SYNCHRONOUS axis, so it cannot rot behind the count checks"
  "$dcctl" ha verify-db --cluster dc-rdb --instances 1 --require-synchronous \
    --kube-context "kind-$control_cluster" --expect-fail \
    || fail "THE SYNCHRONOUS-AXIS CONTROL DID NOT HOLD. Read the output above: if the store
PASSED, then the synchronous assertions (B3/B6/B8) are no longer detecting an asynchronous
database, and CHECK B's synchronous half proves nothing even though the topology control
above may still be holding on the instance-count assertions."
  say "NEGATIVE CONTROL HELD (synchronous axis)"
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
    say "CHECKS A AND B COMPLETE: the instance holds its declared replication in
BOTH the broker and the relational database, and each verifier was shown to reject
an instance that does not. This is the evidence the HA claim rests on.

WHAT IS STILL NOT COVERED, so this is not read as more than it is: TimescaleDB
remains a single-instance StatefulSet until A2.4, so the EVENT store on this
instance is not replicated and nothing here claims otherwise."
    ;;
  *) fail "unknown command ${1}; try up | verify | control | all | down" ;;
esac
