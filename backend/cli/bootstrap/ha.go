// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The --ha topology (ADR-020 A0).
//
// WHY THIS IS A STRUCT AND NOT TWO FLAGS. High availability for the messaging
// substrate is TWO levers that live in two different tools:
//
//   - the NATS SERVER COUNT, an OpenTofu variable (nats_cluster_replicas), and
//   - the PER-STREAM REPLICA FACTOR, a Helm value the services read
//     (instance.config.infrastructure.nats.streamReplicas).
//
// OpenTofu does not install the DeviceChain chart and the chart does not
// provision the broker, so nothing in either tool can see the other's half. Raise
// only the first and you get three NATS servers holding streams and KV buckets
// that are every one of them single-replica: a cluster that costs three times the
// compute, reports three healthy peers, and survives exactly zero node failures.
// Raise only the second and stream creation is refused (or, once the services
// clamp, quietly downgraded) because a stream cannot be replicated wider than the
// cluster hosting it.
//
// That is the false-HA trap A0 exists to close, and the durable fix is not a
// check — it is denying the two values the ability to be different. There is one
// value here and three renderers, exactly as compactSizing does for the footprint
// preset. A check would still be needed if someone edited the two by hand, which
// is why the preflight in stepHelmInstall reads the server count back from the
// APPLIED infrastructure rather than trusting this struct; but via dcctl the
// disagreement is not reachable.
//
// SCOPE. This is the messaging substrate only. It does not replicate Postgres or
// TimescaleDB (single-instance StatefulSets — ADR-028 covers their durability),
// and it does not raise the DeviceChain services' own replica counts: the
// stateful areas are pinned to one writer by the ADR-070 lease fence, and running
// two would be a correctness change, not a sizing one.
type haTopology struct {
	// ServerReplicas is the number of NATS servers — the OpenTofu half. It is the
	// CEILING on StreamReplicas, since a stream cannot have more replicas than
	// there are servers to hold them.
	ServerReplicas int

	// StreamReplicas is the per-stream/per-bucket replica factor — the Helm half.
	//
	// It equals ServerReplicas rather than sitting below it. A smaller factor would
	// be legal and cheaper per write, but it would mean the instance has servers
	// that hold no copy of a given stream, so the set of node losses it survives
	// depends on WHICH node dies — which is not a property anyone can reason about
	// from "HA is on". Three of three is the shape that makes the claim simple.
	StreamReplicas int
}

// Odd counts only: RAFT commits on a majority, so an even cluster tolerates no
// more failures than the odd size below it while costing an extra server and a
// wider quorum on every write. OpenTofu refuses even values independently (see
// nats_cluster_replicas), so these two agree by construction rather than by
// coincidence — but if this ever grows a knob, that validation is the backstop.
var (
	haEnabled  = haTopology{ServerReplicas: 3, StreamReplicas: 3}
	haDisabled = haTopology{ServerReplicas: 1, StreamReplicas: 1}
)

// haFor resolves the topology for the --ha flag.
func haFor(enabled bool) haTopology {
	if enabled {
		return haEnabled
	}
	return haDisabled
}

// Replicated reports whether this topology asks for more than one copy of
// anything. Used by the guards that only apply to a real cluster.
func (h haTopology) Replicated() bool { return h.ServerReplicas > 1 }

// infraVars renders the OpenTofu half.
//
// Both variables are emitted even for the single-node topology, and even though
// nats_cluster_replicas=0 would derive the same numbers from ha alone. Being
// explicit costs nothing and means the applied infrastructure states the server
// count outright — which is what the preflight reads back, and what someone
// debugging a `tofu show` needs to see without knowing the derivation rule.
func (h haTopology) infraVars() []string {
	return []string{
		fmt.Sprintf("ha=%t", h.Replicated()),
		fmt.Sprintf("nats_cluster_replicas=%d", h.ServerReplicas),
	}
}

// natsValues renders the Helm half as the block it occupies under
// instance.config.infrastructure.nats.
//
// Only the one key, and MERGED by the caller rather than assigned: the same map
// carries the broker TLS material and service credentials, and an assignment
// would drop whichever was written first — costing either replication (silently)
// or the ability to reach the broker at all.
func (h haTopology) natsValues() map[string]interface{} {
	return map[string]interface{}{"streamReplicas": h.StreamReplicas}
}

// summary is the one-line resolution printed in the bootstrap report.
func (h haTopology) summary() string {
	if !h.Replicated() {
		return "single-node NATS, unreplicated streams"
	}
	return fmt.Sprintf("%d NATS servers (one per node), streams and KV buckets at %d replicas",
		h.ServerReplicas, h.StreamReplicas)
}

// checkHaNodeCapacity refuses an HA bring-up onto a cluster that cannot place the
// servers one per node, BEFORE anything is provisioned.
//
// The NATS servers carry a hard topologySpreadConstraint (whenUnsatisfiable:
// DoNotSchedule — see modules/nats), because the soft form permits all three
// landing on one node, which is the most expensive way to run a single replica.
// The consequence is that a cluster with fewer schedulable nodes than servers
// leaves the surplus Pending forever, and the way an operator meets that today is
// a ten-minute helm wait on pods that will never become ready. This turns it into
// a sentence, before the volumes exist.
//
// It counts NODES rather than testing the context name against the local-cluster
// heuristic. The heuristic is what the shipped single-node kind config would have
// tripped, and it is the likely cause — which is why the message names it — but a
// name is not the precondition. Someone who has uncommented the workers in
// deploy/local/kind-cluster.yaml has a perfectly good 3-node kind cluster, and
// refusing them on the strength of "kind-" in the context name would be a guard
// that is wrong in exactly the case it was built for.
//
// Unschedulable nodes (cordoned, or carrying a NoSchedule taint) are excluded:
// counting them would let the check pass and the scheduler still fail, which is
// the failure this exists to move earlier, not to relabel.
// It runs on a DRY RUN too, unlike everything else in this step. The check is
// read-only — it lists nodes — and "this cluster cannot host the topology you
// asked for" is the single most useful thing a dry run can say about --ha. A
// dry-run that cheerfully describes an install which could never schedule is
// describing something that will not happen.
func checkHaNodeCapacity(ctx context.Context, st *State) error {
	ha := haFor(st.HA)
	if !ha.Replicated() {
		return nil
	}
	_, _, typed, err := kubeClients(st.KubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to verify it can host the --ha topology: %w", err)
	}
	nodes, err := typed.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing nodes to verify the cluster can host the --ha topology: %w", err)
	}
	return schedulableShortfall(ha, nodes.Items)
}

// schedulableShortfall is the counting rule, split from the cluster access above
// so it can be asserted on without a cluster — the arithmetic is where the guard
// can be wrong, and it is the half that would otherwise go untested.
func schedulableShortfall(ha haTopology, nodes []corev1.Node) error {
	if !ha.Replicated() {
		return nil
	}
	schedulable := 0
	for _, n := range nodes {
		if n.Spec.Unschedulable {
			continue
		}
		if slices.ContainsFunc(n.Spec.Taints, func(t corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		}) {
			continue
		}
		schedulable++
	}
	if schedulable >= ha.ServerReplicas {
		return nil
	}
	return fmt.Errorf(
		"--ha places %d NATS servers one per node, and this cluster has %d schedulable node(s). "+
			"The spread constraint is hard (DoNotSchedule) on purpose: allowing the servers to "+
			"share a node would give an instance the cost of replication and none of its "+
			"protection, so the surplus would sit Pending indefinitely instead. Add nodes, or "+
			"drop --ha. On a local kind cluster, uncomment the workers in "+
			"deploy/local/kind-cluster.yaml and recreate it",
		ha.ServerReplicas, schedulable)
}

// checkBrokerHostsReplication refuses to install the chart when the broker that
// was actually provisioned cannot host the replica factor the chart is about to
// ask for.
//
// This reads the server count back from the APPLIED infrastructure rather than
// from haTopology, and that distinction is the whole point. Via dcctl the two
// halves come from one struct and cannot disagree — so a check against that
// struct would be a check against our own intent, which is the shape of assertion
// that passes forever and proves nothing. What can still differ is the
// infrastructure: a tfvars override in the instance's working directory, a
// hand-edited state, a partially-failed apply, or an instance whose infra was
// last applied by an older dcctl. Those all produce a real cluster smaller than
// the request, and the symptom without this is stream creation failing inside
// services that have already been declared healthy by helm.
//
// Absence of the output is NOT treated as a failure: it means the infrastructure
// predates the output, which is a legitimate upgrade path, and refusing there
// would break instances that are fine. It is only checked when it is present and
// disagrees.
func checkBrokerHostsReplication(st *State) error {
	ha := haFor(st.HA)
	if !ha.Replicated() || st.DryRun {
		return nil
	}
	raw, ok := st.Values[natsClusterReplicasKey]
	if !ok || raw == "" {
		return nil
	}
	servers, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("the infrastructure reported an unreadable NATS server count %q: %w", raw, err)
	}
	if servers >= ha.StreamReplicas {
		return nil
	}
	return fmt.Errorf(
		"the applied infrastructure provisioned %d NATS server(s), but this install would set "+
			"instance.config.infrastructure.nats.streamReplicas=%d — a stream cannot be replicated "+
			"wider than the cluster hosting it. The two halves of the HA toggle disagree: "+
			"nats_cluster_replicas (OpenTofu) sizes the broker and streamReplicas (Helm) sizes the "+
			"data on it. Re-run the infrastructure apply with --ha, or drop --ha here. (dcctl sets "+
			"both from one value, so a disagreement means the infrastructure was applied by "+
			"something else — check for a terraform.tfvars in the instance's infra directory)",
		servers, ha.StreamReplicas)
}

// natsClusterReplicasKey is where applyInfra stashes the server count read back
// from the OpenTofu outputs, for checkBrokerHostsReplication to compare against.
const natsClusterReplicasKey = "natsClusterReplicas"

// HaSummary is the resolved topology, for the command layer to echo when --ha is
// given. The struct stays unexported for the same reason compactSizing does: it
// is a topology, and exposing it invites it being taken apart into per-value
// flags — which is precisely the ability to make the two halves disagree that
// this exists to remove.
func HaSummary(enabled bool) string { return haFor(enabled).summary() }
