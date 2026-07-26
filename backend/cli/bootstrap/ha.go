// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/hashicorp/terraform-exec/tfexec"
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
// heuristic, because a name is not the precondition: someone running a real
// multi-node cluster whose context happens to be called "kind-something" should
// not be refused, and someone on a single-node cluster named anything else should.
//
// Nodes that cannot host a server are excluded — cordoned, or carrying a
// NoSchedule/NoExecute taint. The NATS release sets no tolerations, so a tainted
// node genuinely cannot take a server; counting it would let this check pass and
// leave the scheduler to fail instead, which is the failure being moved earlier,
// not relabelled.
//
// 🔴 THAT EXCLUSION IS WHY A 3-NODE KIND CLUSTER IS NOT ENOUGH, and the reason is
// not obvious enough to leave to the reader. kind removes the control-plane
// NoSchedule taint ONLY when the cluster has exactly one node
// (kubeadminit/init.go: `if len(allNodes) == 1`). So a single-node kind cluster
// reports one SCHEDULABLE node, while a 1-control-plane + 2-worker cluster reports
// TWO — the control plane keeps its taint the moment a second node exists.
// Uncommenting two workers therefore does not satisfy a 3-way spread, and an
// earlier version of the error message told operators to do exactly that. Three
// workers.
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
		"--ha places %d NATS servers one per node, and this cluster has %d node(s) that can "+
			"actually take one (cordoned and NoSchedule/NoExecute-tainted nodes do not count — "+
			"the NATS release sets no tolerations). The spread constraint is hard "+
			"(DoNotSchedule) on purpose: letting the servers share a node would give an "+
			"instance the cost of replication and none of its protection, so the surplus sits "+
			"Pending instead. Add nodes, or drop --ha. On a local kind cluster you need %d "+
			"WORKERS in deploy/local/kind-cluster.yaml, not %d — kind only removes the "+
			"control-plane taint on a single-node cluster, so the control plane stops being "+
			"schedulable as soon as you add the first worker",
		ha.ServerReplicas, schedulable, ha.ServerReplicas, ha.ServerReplicas-1)
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

// outputReader is the slice of tfexec.Terraform the teardown guard needs, so the
// guard can be exercised without a tofu binary or a state file.
type outputReader interface {
	Output(ctx context.Context, opts ...tfexec.OutputOption) (map[string]tfexec.OutputMeta, error)
}

// checkHaNotTornDown refuses to shrink a NATS cluster that is already carrying
// replicated data.
//
// THE ASYMMETRY THIS FIXES. Raising the topology has two guards (node capacity,
// broker capability). Lowering it had none — and lowering it is the destructive
// direction. `--ha` is not persisted anywhere: State.HA lives for one process, and
// OpenTofu resolves an unpassed variable to its DEFAULT rather than to whatever
// the last apply used. So `dcctl bootstrap local prod --host new.example.com` on
// an instance that was built with `--ha` resolves to one server and scales the
// StatefulSet 3 -> 1. (Emitting nothing would not help: the defaults derive 1 the
// same way. The exposure is inherent to gaining the ability to build the cluster
// at all.)
//
// What makes that unrecoverable rather than merely wrong: the streams and buckets
// are still R3, and a 3-replica RAFT group with one surviving peer has no quorum —
// no meta leader, no writes, no stream creation. The services will not fix it,
// because the replica reconcile is deliberately upward-only (core/messaging
// applyStreamReplicas): a starting pod must never de-replicate. So the instance is
// wedged in a state that re-running bootstrap cannot repair, and the operator was
// never told, because omitting a flag is not an error.
//
// The posture therefore MIRRORS the runtime one: ratchet up, refuse to ratchet
// down, say so loudly. There is no --force here on purpose. A safe teardown means
// de-replicating the streams FIRST, while a quorum still exists to accept the
// update, and that ordering cannot be expressed as a flag on this command.
//
// It reads state, so it runs after Init and before Apply. Consequences: it does
// NOT run under --dry-run, which short-circuits before applyInfra — a dry run will
// not warn you about a teardown it would perform. Worth closing if dry-run ever
// grows a real plan step; not worth making dry-run require tofu state today.
func checkHaNotTornDown(ctx context.Context, st *State, tf outputReader) error {
	applied, known := appliedServerCount(ctx, tf)
	return haTeardownRefusal(haFor(st.HA), applied, known)
}

// appliedServerCount reads the provisioned NATS server count out of the current
// tofu outputs. known is false when there is nothing to compare against — a fresh
// instance with no state, or infrastructure applied before this output existed.
//
// A read failure is reported as UNKNOWN rather than propagated. The overwhelmingly
// likely cause is "no state file yet", i.e. a first bootstrap, and failing that
// because a guard could not find something to protect would be absurd. If state
// does exist and is unreadable, the Apply immediately after will say so far more
// precisely than this could.
func appliedServerCount(ctx context.Context, tf outputReader) (int, bool) {
	outputs, err := tf.Output(ctx)
	if err != nil {
		return 0, false
	}
	meta, ok := outputs["nats_cluster_replicas"]
	if !ok {
		return 0, false
	}
	var servers int
	if err := json.Unmarshal(meta.Value, &servers); err != nil || servers < 1 {
		return 0, false
	}
	return servers, true
}

// haTeardownRefusal is the rule, split from the IO so it can be tested directly.
func haTeardownRefusal(want haTopology, appliedServers int, known bool) error {
	if !known || appliedServers <= want.ServerReplicas {
		return nil
	}
	return fmt.Errorf(
		"this instance already has %d NATS servers and this run would provision %d, which "+
			"would tear down a broker cluster that is carrying replicated data. Its streams and "+
			"KV buckets are replicated across those %d servers, so shrinking to %d leaves every "+
			"RAFT group without a quorum: no writes, no stream creation, and no way back by "+
			"re-running, because the services deliberately never de-replicate a stream. "+
			"If you meant to keep this instance highly available, pass --ha. If you really "+
			"intend to collapse it, de-replicate the streams FIRST (`nats stream update -r 1`, "+
			"while a quorum still exists to accept it) or destroy the instance outright",
		appliedServers, want.ServerReplicas, appliedServers, want.ServerReplicas)
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
