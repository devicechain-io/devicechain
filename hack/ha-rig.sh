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
#   CHECK B  BOTH databases — CloudNativePG instance count, pod spread, the
#            alias Services, PostgreSQL's own view of synchronous replication,
#            and (for the event store) TimescaleDB background-job health
#
# BOTH instances deploy ONE AREA BEYOND the `default` profile — lwm2m-ingest, with
# credentials — so this is deliberately not a stock default install. It is the only
# way CHECK A2 can assert on anything: nothing in `default` takes an ADR-070 lease,
# so the fence substrate does not exist on a default instance and A2 skips. The
# reasoning, and why deploying the area without credentials would make things worse
# rather than better, is at ensure_lease_identities.
#
# 🔴 A green run means the broker and both databases are replicated. It does NOT
# mean a node loss has been survived: nothing here kills a node. That is A1, and
# it is a different drill.
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

# Scratch space for the run's generated inputs (see ensure_lease_identities). Not
# under the repo: a rig that drops files into the working tree eventually gets one
# committed. Removed on exit however the run ends, including a failed check —
# `fail` exits non-zero and EXIT still fires.
rig_tmp="$(mktemp -d "${TMPDIR:-/tmp}/dc-ha-rig.XXXXXX")"
trap 'rm -rf "$rig_tmp"' EXIT

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

# THE LEASE-HOLDER ARGUMENT — why this rig deploys an area no default install runs.
#
# CHECK A2 asserts the replication of the ADR-070 fence substrate: the dc_leases KV
# bucket holding the lease that decides which pod may write. On the first run this
# rig ever completed end to end, A2 asserted NOTHING, and said so in as many words:
#
#   A2: the ADR-070 lease bucket (KV_harig_dc_leases) does not exist and no
#       deployed area takes a lease, so the fence substrate's replication was NOT
#       checked. Note this also means the JetStreamLeaseBucketNotReplicated alert
#       has no series to evaluate on this instance
#
# That was not a transient, and reading it as one is the mistake worth naming. Only
# lwm2m-ingest and sparkplug-ingest take leases; both are opt-in areas held out of
# the `default` profile this rig bootstraps. So A2 could not have asserted on ANY
# run this rig would ever do — a permanently dark axis inside a suite whose entire
# premise is that an unexercised check is worth nothing. The skip line was correct
# and honest every single time, which is exactly why it read as unremarkable.
#
# lwm2m-ingest rather than sparkplug-ingest because it is reachable from a
# supported flag: --lwm2m-identities provisions the credentials AND implies
# --enable-area lwm2m-ingest. Sparkplug's equivalent is a `sources` list with a
# per-tenant broker URL, which bootstrap has no flag for.
#
# 🔴 And the area alone is NOT enough. BOTH lease holders create the bucket only
# when CONFIGURED, not merely when deployed — lwm2m-ingest with no identities is a
# documented inert posture (bind, authenticate no one) that takes no lease, and
# sparkplug-ingest with no sources likewise. But the checker's expectation keys off
# DEPLOYED (core/messaging leaseHolderDeployed). So `--enable-area lwm2m-ingest` on
# its own would flip A2 from "not checked" to demanding a bucket nothing was ever
# going to create — trading a silent skip for a false failure, which is worse.
# Credentials are what make the assertion real.
#
# The credential is real but leads nowhere: nothing ever connects to this rig, so
# the identity exists only to put the adapter into its credentialed posture. It is
# generated per run rather than committed, and written into a 0700 dir at 0600 —
# a throwaway key is still a key, and a rig that models bad handling teaches it.
#
# `need openssl` is NOT here — it is on each command's dependency line, beside
# kind/kubectl/docker. This function runs after create_cluster, and a missing
# tool discovered two minutes into a kind bring-up is the posture this script
# rejects everywhere else: fail before anything is provisioned.
lease_identities_file=""
ensure_lease_identities() {
  lease_identities_file="$rig_tmp/lwm2m-identities.json"
  # A subshell so the umask does not leak into the rest of the run.
  (
    umask 077
    cat >"$lease_identities_file" <<EOF
[
  {
    "identity": "ha-rig-lease-probe",
    "psk": "$(openssl rand -base64 32)",
    "tenant": "ha-rig",
    "externalId": "ha-rig-lease-probe",
    "autoRegister": false
  }
]
EOF
  )
  # Checked rather than assumed — but this guard is convenience, not correctness,
  # and saying so is the point. Measured: bootstrap rejects an empty file, parsing
  # the JSON array and reporting EOF. So the guard buys one thing only: failing at
  # the line that wrote the file, rather than inside a bootstrap whose error names
  # a parse problem in a file the operator never wrote and cannot see. A full disk
  # reads as bad JSON.
  #
  # It does NOT protect against the false-A2 state described above, and it would be
  # wrong to think it does. An empty PATH (--lwm2m-identities "") parses to zero
  # identities, and the area implication is gated on the parsed COUNT rather than
  # on the flag being present — so the area is not deployed either, and A2 goes
  # back to its honest skip. The input that actually produces a false A2 is
  # `--enable-area lwm2m-ingest` with no identities: area deployed, no lease taken,
  # bucket demanded and never created. Nothing in this rig passes it.
  [[ -s "$lease_identities_file" ]] ||
    fail "could not write the LwM2M identities file at $lease_identities_file"
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
  need kind; need kubectl; need docker; need openssl
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
  # --lwm2m-identities deploys a credentialed lease holder so CHECK A2 has a fence
  # substrate to assert on at all. See ensure_lease_identities.
  ensure_lease_identities
  say "bootstrapping --ha"
  "$dcctl" bootstrap local "$instance" --ha --yes --no-escrow \
    --kube-context "kind-$ha_cluster" --host localhost --no-tls \
    --lwm2m-identities "$lease_identities_file" \
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
  say "CHECK B1 — asserting the RELATIONAL database replication claim from live PostgreSQL state"
  "$dcctl" ha verify-db --cluster dc-rdb --alias-service dc-postgresql \
    --instances 3 --require-synchronous --durability required \
    --kube-context "kind-$ha_cluster" \
    || fail "the relational database does not hold the replication it declares (see the findings above)"
  say "CHECK B1 PASSED"

  # The event store (ADR-020 A2.4). Same verifier, three deliberate differences.
  #
  # --durability preferred, because this store makes the opposite trade: a lost
  # standby degrades it to asynchronous replication rather than stalling ingest,
  # since events are held durably upstream in JetStream until they persist.
  # Checking it against "required" would report a correct configuration broken,
  # and a check that is red on a working system is one that gets switched off.
  #
  # --timescale-jobs, which is the assertion that only exists for this store. A
  # database whose background scheduler has stopped passes every other check
  # here — right instance count, right replication, right pod spread — and has
  # silently stopped aggregating.
  say "CHECK B2 — asserting the EVENT store replication claim and background-job health"
  "$dcctl" ha verify-db --cluster dc-tsdb --alias-service dc-timescaledb-single \
    --instances 3 --require-synchronous --durability preferred --timescale-jobs \
    --kube-context "kind-$ha_cluster" \
    || fail "the event store does not hold the replication it declares, or its background jobs are not healthy (see the findings above)"
  say "CHECK B2 PASSED"
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
  need kind; need kubectl; need openssl
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
  # control still exercises the same functional areas creating the same streams
  # and buckets as the HA side.
  #
  # --lwm2m-identities is passed HERE TOO, and matching the HA side is the whole
  # point: a control that omitted it would be a different deployment, and the one
  # axis it dropped would be the one A2 covers. With it, the control carries a
  # dc_leases bucket at 1 replica while claiming 3 — so A2 is not merely exercised
  # on the HA side, it is shown to FAIL on an instance that does not hold the
  # claim. That is the difference between an assertion and a decoration.
  say "bootstrapping the negative control (no --ha, single node)"
  ensure_lease_identities
  "$dcctl" bootstrap local "$control_instance" --yes --compact --no-escrow \
    --kube-context "kind-$control_cluster" --host localhost --no-tls \
    --lwm2m-identities "$lease_identities_file" \
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
  say "NEGATIVE CONTROL HELD (relational database)"

  # The same control for the EVENT store. Not redundant with the one above: the
  # two stores are separate Cluster objects reached through separate alias
  # Services, so a verifier that had stopped detecting an unreplicated event
  # store would leave the relational control holding and say nothing.
  say "NEGATIVE CONTROL — the same check, against an event store that is NOT replicated"
  "$dcctl" ha verify-db --cluster dc-tsdb --alias-service dc-timescaledb-single \
    --instances 3 --require-synchronous --durability preferred \
    --kube-context "kind-$control_cluster" --expect-fail \
    || fail "THE EVENT-STORE NEGATIVE CONTROL DID NOT HOLD, or could not run. Read the output
above: a PASS where a failure was expected means CHECK B2 proves nothing."
  say "NEGATIVE CONTROL HELD (event store)"

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

  # And the background-job axis, isolated for exactly the same reason.
  #
  # On the single-node control the event store has no timescaledb-carrying
  # database only if the extension is genuinely absent — which it is not, so this
  # control cannot lean on B9. It states the store's ACTUAL instance count and
  # drops --require-synchronous, so B1/B2/B3/B6/B7/B8 are all out of the picture.
  # What remains is the job axis alone.
  #
  # 🔴 Unlike the controls above, this one is expected to PASS: a healthy
  # single-node event store has healthy jobs. That is the point — it proves the
  # job checks RUN and can see real jobs, which is the half a --expect-fail
  # control cannot establish.
  #
  # What makes that more than a hope is B9, which fires when the extension is
  # nowhere OR when databases carry it and not one job was examined. An earlier
  # version of this comment said "the counts line in the report is the evidence",
  # which was wrong in the way this rig exists to prevent: nothing automated reads
  # that line, this step is `|| fail` on exit status, and a run examining zero
  # jobs exited 0. The evidence is the assertion, not the printout.
  say "JOB-AXIS COVERAGE — the background-job checks must RUN and see real jobs"
  "$dcctl" ha verify-db --cluster dc-tsdb --alias-service dc-timescaledb-single \
    --instances 1 --timescale-jobs \
    --kube-context "kind-$control_cluster" \
    || fail "THE BACKGROUND-JOB CHECKS FAILED on a healthy single-node event store. Either the
event store is genuinely broken, or (B9) no database carries the timescaledb extension, which
would mean the checks examined nothing and CHECK B2's job half proves nothing."
  say "JOB-AXIS COVERAGE CONFIRMED"
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
    say "CHECKS A AND B COMPLETE: the instance holds its declared replication in the
broker, the relational database and the event store, each verifier was shown to
reject an instance that does not, and the event store's background jobs were shown
to be running rather than merely present. This is the evidence the HA claim rests on.

WHAT IS STILL NOT COVERED, so this is not read as more than it is: nothing here
kills a node, so no failover has been survived under load — that is A1. Nothing
here backs anything up or restores it — that is A2.5. A green run means the
declared replication is real, not that a disaster has been rehearsed."
    ;;
  *) fail "unknown command ${1}; try up | verify | control | all | down" ;;
esac
