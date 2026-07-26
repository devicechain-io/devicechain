// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// haState is the minimum State the two renderers need, mirroring compactState.
func haState(haOn bool) *State {
	return &State{
		Instance:      "dctest",
		Profile:       "default",
		KubeContext:   "kind-dctest",
		ImageRegistry: DefaultImageRegistry,
		ImageVersion:  "v0.0.0-test",
		HA:            haOn,
		Values: map[string]string{
			"ingressHost":    "localhost",
			"secretsRootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
			// The broker-auth material a real bootstrap has minted by the Helm step,
			// for the SAME reason compactState carries it: the HA replica factor is
			// merged into the same nats map, and with the map empty at merge time,
			// merging and assigning are indistinguishable. Without these, mutating
			// the merge in helm.go to an assignment passes every test in this file —
			// the HA half would be protected only incidentally, by the compact
			// fixture, and only for as long as the compact block stays above it.
			"natsTlsEnabled":        "true",
			"natsCA":                "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
			"natsCalloutIssuerSeed": "SUAtestseedvaluefortestingonly",
			"natsServicePassword":   "test-service-password",
		},
	}
}

// The HA replica factor must be MERGED into the nats values, not assigned over
// them. Assigning drops whichever block was written first — either the broker
// credentials (a service that cannot connect at all) or the replica factor (a
// silently unreplicated instance).
func TestHaValuesMergeIntoTheNatsBlock(t *testing.T) {
	n := renderFromState(t, haState(true)).Infrastructure.Nats

	if n.StreamReplicas < 2 {
		t.Errorf("streamReplicas = %d with --ha: the HA block did not survive the merge",
			n.StreamReplicas)
	}
	if n.Auth.User == "" || !n.Tls.Enabled {
		t.Error("the broker credentials/TLS were lost: the HA block was ASSIGNED over the " +
			"nats map rather than merged into it, so this instance cannot reach the broker")
	}
	if n.Hostname == "" || n.Port == 0 {
		t.Error("the broker coordinates are gone: the nats block was replaced wholesale")
	}
}

// THE test for A0.
//
// The false-HA trap is not that either lever is wrong. It is that they are two
// levers, in two tools, that nothing compares — so an instance can carry a 3-node
// NATS cluster and single-replica streams, cost three times the compute, report
// three healthy peers, and survive zero node failures. Every other test here
// checks a value; this one checks that the two values cannot disagree.
//
// Both sides are read from the PRODUCTION renderers, never restated: the server
// count out of infraVars (what the tofu apply is actually given) and the replica
// factor out of the rendered chart config (what the services actually read). A
// version of this that compared haEnabled.ServerReplicas against
// haEnabled.StreamReplicas would pass forever while proving nothing, because both
// halves would be the same struct field being read twice.
func TestHaTopologyIsCoherentAcrossTools(t *testing.T) {
	for _, tc := range []struct {
		name string
		on   bool
	}{{"ha", true}, {"single-node", false}} {
		t.Run(tc.name, func(t *testing.T) {
			st := haState(tc.on)

			servers := serverCountFromInfraVars(t, st)
			streams := int(renderFromState(t, st).Infrastructure.Nats.StreamReplicas)

			// The invariant, in the direction that matters: an instance may not ask
			// for more replicas than it has servers to hold them.
			if streams > servers {
				t.Errorf("the chart configures streamReplicas=%d against the %d NATS "+
					"server(s) OpenTofu is told to provision: a stream cannot be "+
					"replicated wider than its cluster, so this instance would fail "+
					"stream creation or silently clamp to 1", streams, servers)
			}
			// And the direction that makes the toggle mean something: servers without
			// replicated streams IS the false-HA state, so paying for a cluster and
			// leaving the data on one node must not be reachable either.
			if servers > 1 && streams <= 1 {
				t.Errorf("OpenTofu is told to provision %d NATS servers while the chart "+
					"leaves streamReplicas=%d: that is the false-HA state A0 exists to "+
					"close — three servers, one copy of the data, zero node failures "+
					"survived", servers, streams)
			}
			if tc.on && servers < 3 {
				t.Errorf("--ha provisioned %d servers, want at least 3", servers)
			}
			if !tc.on && (servers != 1 || streams != 1) {
				t.Errorf("without --ha: servers=%d streams=%d, want 1 and 1", servers, streams)
			}
		})
	}
}

// Both halves must be stated on every path, not only when --ha is set.
//
// A conditional renderer would leave one narrow route back to the disagreement:
// re-running bootstrap WITHOUT --ha against an instance that had it. The tofu
// state remembers the 3-node cluster and the chart's default streamReplicas is 1,
// so an omitted value on either side means "leave whatever is there" — and the
// two ends of that are a cluster nobody asked for and data nobody replicated.
func TestBothHaLeversAreAlwaysStated(t *testing.T) {
	for _, on := range []bool{true, false} {
		st := haState(on)

		var sawCount, sawFlag bool
		for _, v := range infraVars(st) {
			sawCount = sawCount || strings.HasPrefix(v, "nats_cluster_replicas=")
			sawFlag = sawFlag || strings.HasPrefix(v, "ha=")
		}
		if !sawCount || !sawFlag {
			t.Errorf("--ha=%t passed ha=%v nats_cluster_replicas=%v to OpenTofu; both "+
				"must always be stated so an instance that had HA cannot keep half of it",
				on, sawFlag, sawCount)
		}

		// Read the value dcctl STATES, out of helmValues, rather than the value the
		// chart RENDERS.
		//
		// The rendered config cannot answer this question. values.yaml already
		// carries `streamReplicas: 1`, so a render produces 1 whether dcctl said
		// anything or not — an earlier version of this assertion checked the
		// rendered number and passed cleanly after the merge was made conditional
		// on st.HA, which is the exact regression the doc comment above forbids.
		// The chart default and dcctl's silence are indistinguishable downstream;
		// they are only distinguishable here.
		if _, ok := statedStreamReplicas(helmValues(st)); !ok {
			t.Errorf("--ha=%t: helmValues states no streamReplicas, leaving it to the "+
				"chart default. An instance that HAD replication keeps whatever was "+
				"there, which is how the two halves drift apart again", on)
		}
	}
}

// statedStreamReplicas digs the replica factor out of the values map dcctl hands
// Helm, reporting whether it was stated at all.
func statedStreamReplicas(vals map[string]interface{}) (int, bool) {
	step := func(m map[string]interface{}, key string) map[string]interface{} {
		next, _ := m[key].(map[string]interface{})
		return next
	}
	nats := step(step(step(step(vals, "instance"), "config"), "infrastructure"), "nats")
	if nats == nil {
		return 0, false
	}
	v, ok := nats["streamReplicas"].(int)
	return v, ok
}

// The server count must be odd. An even RAFT cluster tolerates no more failures
// than the odd size below it while costing an extra server and a wider quorum on
// every write, which is why OpenTofu refuses even values outright — dcctl must
// never be the thing that hands it one.
func TestHaServerCountIsOdd(t *testing.T) {
	for _, on := range []bool{true, false} {
		if n := serverCountFromInfraVars(t, haState(on)); n%2 == 0 {
			t.Errorf("--ha=%t asks OpenTofu for %d NATS servers, which is even: "+
				"nats_cluster_replicas rejects it, so the apply fails", on, n)
		}
	}
}

// serverCountFromInfraVars extracts nats_cluster_replicas from the vars the infra
// apply is actually given.
func serverCountFromInfraVars(t *testing.T, st *State) int {
	t.Helper()
	const key = "nats_cluster_replicas="
	for _, v := range infraVars(st) {
		if strings.HasPrefix(v, key) {
			n, err := strconv.Atoi(strings.TrimPrefix(v, key))
			if err != nil {
				t.Fatalf("infraVars passed an unparseable %q: %v", v, err)
			}
			return n
		}
	}
	t.Fatal("infraVars passed no nats_cluster_replicas: the OpenTofu half of the HA " +
		"topology is not being set at all, so the assertions below would measure nothing")
	return 0
}

// The broker-capability preflight must refuse an install whose replica factor the
// applied broker cannot host — and must NOT refuse the cases that are fine.
//
// The distinction that matters here is what it reads. It compares against the
// server count the INFRASTRUCTURE reported, not against haTopology, so it is
// still meaningful on an instance whose infra was applied by something other than
// this dcctl (a tfvars override, a hand-edited state, an older binary). Checking
// our own struct against itself would be the assertion that can never fail.
func TestBrokerCapabilityPreflight(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ha        bool
		applied   string // the nats_cluster_replicas output, "" = absent
		wantError bool
	}{
		{"ha onto a 3-server broker", true, "3", false},
		{"ha onto a 5-server broker", true, "5", false},
		{"ha onto a single-server broker", true, "1", true},
		{"ha onto a 2-server broker", true, "2", true},
		// Absent means the infrastructure predates the output — a legitimate
		// upgrade path. Refusing there would break instances that are fine.
		{"ha with no reported count", true, "", false},
		// Without --ha there is nothing to be wider than.
		{"no ha onto a single-server broker", false, "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := haState(tc.ha)
			if tc.applied != "" {
				st.Values[natsClusterReplicasKey] = tc.applied
			}
			err := checkBrokerHostsReplication(st)
			if tc.wantError && err == nil {
				t.Fatal("the preflight accepted an install the broker cannot host: the " +
					"services would fail stream creation after helm has already " +
					"reported the release installed")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("the preflight refused a valid install: %v", err)
			}
			if err == nil {
				return
			}
			// The message must name BOTH levers. An operator who is told only that
			// "replication failed" has to discover on their own that the fix lives in
			// a different tool from the flag they typed.
			for _, want := range []string{"nats_cluster_replicas", "streamReplicas"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not name %q, so it does not tell the "+
						"operator where the other half of the toggle lives: %v", want, err)
				}
			}
		})
	}
}

// The node-capacity guard must refuse a cluster that cannot place the servers one
// per node, and it must count SCHEDULABLE nodes rather than trusting a name.
//
// The spread constraint is hard (DoNotSchedule), so a short cluster leaves the
// surplus Pending forever — which reaches an operator as a ten-minute helm
// timeout on pods that will never become ready, long after the volumes exist.
func TestHaNodeCapacityGuard(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ha        bool
		nodes     []corev1.Node
		wantError bool
	}{
		{"three ready nodes", true, nodes(3, 0, 0), false},
		{"one node", true, nodes(1, 0, 0), true},
		{"two nodes", true, nodes(2, 0, 0), true},
		// A cordoned or NoSchedule-tainted node cannot host a server, so counting it
		// would let this pass and leave the scheduler to fail instead — moving the
		// failure back to exactly where this exists to move it from.
		{"three nodes, one cordoned", true, nodes(2, 1, 0), true},
		{"three nodes, one tainted NoSchedule", true, nodes(2, 0, 1), true},
		{"five nodes", true, nodes(5, 0, 0), false},
		// Nothing to check without --ha.
		{"single node without ha", false, nodes(1, 0, 0), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := schedulableShortfall(haFor(tc.ha), tc.nodes)
			if tc.wantError && got == nil {
				t.Fatal("the guard accepted a cluster that cannot place the servers one " +
					"per node: the surplus sits Pending until the helm wait times out")
			}
			if !tc.wantError && got != nil {
				t.Fatalf("the guard refused a cluster that can host the topology: %v", got)
			}
		})
	}
}

// nodes builds a node list: `ready` plain, `cordoned` unschedulable, and
// `tainted` carrying a NoSchedule taint.
func nodes(ready, cordoned, tainted int) []corev1.Node {
	var out []corev1.Node
	add := func(n int, mutate func(*corev1.Node)) {
		for i := 0; i < n; i++ {
			node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n"}}
			if mutate != nil {
				mutate(&node)
			}
			out = append(out, node)
		}
	}
	add(ready, nil)
	add(cordoned, func(n *corev1.Node) { n.Spec.Unschedulable = true })
	add(tainted, func(n *corev1.Node) {
		n.Spec.Taints = []corev1.Taint{{Key: "k", Effect: corev1.TaintEffectNoSchedule}}
	})
	return out
}

// The counting rule above is only worth testing if the step actually runs it.
//
// This is the seam that a table-driven test of a pure function cannot see, and it
// is the one that has bitten before: a correct check nothing calls looks exactly
// like a correct check. Pointing the HA path at a kube-context that does not
// exist must fail while REACHING FOR THE CLUSTER — proving stepInfraApply
// consulted the guard — rather than sailing past into the tofu apply.
// DryRun is deliberately set: the guard runs on a dry run (it is read-only, and
// an unschedulable topology is exactly what a dry run should report), so this
// exercises the wiring WITHOUT the un-guarded path being able to shell out to
// tofu and apply infrastructure. A version of this test that relied on the real
// apply failing would, the moment someone deleted the guard, have CI run a live
// `tofu apply` against a nonexistent context to prove a point.
func TestInfraApplyConsultsTheNodeGuard(t *testing.T) {
	st := haState(true)
	st.KubeContext = "definitely-not-a-real-kube-context"
	st.DryRun = true

	err := stepInfraApply(t.Context(), st)
	if err == nil {
		t.Fatal("stepInfraApply proceeded with --ha without consulting the node-capacity " +
			"guard: an undersized cluster would be discovered as a helm timeout instead")
	}
	if !strings.Contains(err.Error(), "--ha topology") {
		t.Fatalf("stepInfraApply failed for some other reason than the HA guard, so this "+
			"proves nothing about the wiring: %v", err)
	}
}

// ...and it must NOT run it when there is nothing to check, or every single-node
// bring-up would need cluster access before the infra apply that establishes it.
func TestInfraApplySkipsTheNodeGuardWithoutHa(t *testing.T) {
	st := haState(false)
	st.KubeContext = "definitely-not-a-real-kube-context"
	st.DryRun = true

	if err := stepInfraApply(t.Context(), st); err != nil {
		t.Fatalf("a non-HA bootstrap tripped the node guard: %v", err)
	}
}

// The broker-capability preflight is only worth having if the step runs it.
//
// Its sibling checkHaNodeCapacity got this seam test; this one did not, and the
// guard could be deleted from stepHelmInstall with the whole suite green —
// TestBrokerCapabilityPreflight calls the pure function directly and never goes
// through the step. A correct check nothing calls looks exactly like a correct
// check.
//
// DryRun is set so that removing the guard makes the step return cleanly rather
// than attempting a real Helm install against a cluster that is not there.
func TestHelmInstallConsultsTheBrokerPreflight(t *testing.T) {
	st := haState(true)
	st.DryRun = true
	st.Values["namespace"] = "dctest"
	// What applyInfra would have read back from a single-server broker.
	st.Values[natsClusterReplicasKey] = "1"

	err := stepHelmInstall(t.Context(), st)
	if err == nil {
		t.Fatal("stepHelmInstall proceeded with a replica factor the applied broker cannot " +
			"host: the services would fail stream creation after helm reported success")
	}
	for _, want := range []string{"nats_cluster_replicas", "streamReplicas"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}
