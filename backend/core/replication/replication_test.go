// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package replication

import (
	"fmt"
	"strings"
	"testing"
)

// testExpectation is a small stand-in for a real instance's expectation. It is
// deliberately NOT built by messaging.ReplicationExpectation: this file tests the
// predicate, and coupling it to the naming would mean a naming change silently
// rewrote every case here into a test of something else. The naming is pinned
// separately, in messaging, against the runtime's own construction.
func testExpectation(replicas int) Expectation {
	return Expectation{
		Replicas:            replicas,
		Streams:             []string{"i_inbound-events", "i_resolved-events"},
		LeaseBucket:         "KV_i_dc_leases",
		StateBuckets:        []string{"KV_i_dc_locks", "KV_dc_refresh_tokens"},
		CacheBucketSuffixes: []string{"_device-by-token"},
		MqttStreams:         []string{"$MQTT_sess", "$MQTT_msgs"},
		Prefixes:            []string{"i_", "KV_i_", "KV_dc_"},
		RequirePods:         replicas,
	}
}

// healthy builds the snapshot a correctly replicated instance produces at the
// given factor: every required object present, a leader, factor-1 current peers,
// one broker pod per node.
func healthy(exp Expectation) Snapshot {
	var snap Snapshot
	names := append([]string{}, exp.Streams...)
	names = append(names, exp.StateBuckets...)
	names = append(names, exp.MqttStreams...)
	names = append(names, exp.LeaseBucket)
	for _, s := range exp.CacheBucketSuffixes {
		names = append(names, "KV_i_device-management"+s)
	}
	for _, n := range names {
		snap.Objects = append(snap.Objects, Object{
			Name: n, Replicas: exp.Replicas,
			Leader: leaderFor(exp.Replicas), Peers: currentPeers(exp.Replicas),
		})
	}
	snap.Consumers = []Consumer{{
		Stream: "i_inbound-events", Name: "i_event-sources_inbound-events",
		Leader: leaderFor(exp.Replicas), Peers: currentPeers(exp.Replicas),
	}}
	for i := 0; i < exp.RequirePods; i++ {
		snap.Pods = append(snap.Pods, Pod{
			Name: fmt.Sprintf("dc-nats-%d", i), Node: fmt.Sprintf("node-%d", i),
		})
	}
	return snap
}

// leaderFor and currentPeers reproduce what the broker reports at a factor: an
// unclustered R1 object has neither, and every higher factor has a leader plus
// factor-1 peers (Cluster.Replicas EXCLUDES the leader).
func leaderFor(replicas int) string {
	if replicas == 1 {
		return ""
	}
	return "n1"
}

func currentPeers(replicas int) []Peer {
	var out []Peer
	for i := 2; i <= replicas; i++ {
		out = append(out, Peer{Name: fmt.Sprintf("n%d", i), Current: true})
	}
	return out
}

// find returns the findings for a check label.
func find(rep Report, check string) []Finding {
	var out []Finding
	for _, f := range rep.Findings {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

func mustFail(t *testing.T, rep Report, check, substring string) {
	t.Helper()
	if rep.OK() {
		t.Fatalf("expected a %s failure containing %q, but the report PASSED:\n%s",
			check, substring, rep.Format())
	}
	for _, f := range find(rep, check) {
		if strings.Contains(f.Message, substring) {
			return
		}
	}
	t.Fatalf("expected a %s finding containing %q; got:\n%s", check, substring, rep.Format())
}

// --- the positive halves -----------------------------------------------------

func TestHealthyR3Passes(t *testing.T) {
	exp := testExpectation(3)
	rep := Verify(healthy(exp), exp)
	if !rep.OK() {
		t.Fatalf("a correctly replicated R3 instance must pass:\n%s", rep.Format())
	}
	if rep.Checked.Objects == 0 || rep.Checked.Consumers == 0 || rep.Checked.Pods == 0 {
		t.Fatalf("a pass over nothing is not a pass; counts were %+v", rep.Checked)
	}
}

// TestHealthyR1Passes is the other half of the negative control, and it is not a
// formality. A suite that fails everything would satisfy every "must fail" case
// below while being worthless — and worse, it would be switched off, taking the
// negative control with it. Passing a correct non-HA instance is what makes a
// failure mean something.
func TestHealthyR1Passes(t *testing.T) {
	exp := testExpectation(1)
	snap := healthy(exp)
	// A single-node broker reports no cluster at all: no leader, no peers. That is
	// correct R1, not a degraded R3, and must not be read as one.
	for i := range snap.Objects {
		if snap.Objects[i].Leader != "" || len(snap.Objects[i].Peers) != 0 {
			t.Fatalf("the R1 fixture is wrong: it carries cluster state a single-node "+
				"broker never reports (%+v)", snap.Objects[i])
		}
	}
	rep := Verify(snap, exp)
	if !rep.OK() {
		t.Fatalf("a correct single-replica instance must pass at R1:\n%s", rep.Format())
	}
}

// --- THE NEGATIVE CONTROL ----------------------------------------------------

// TestNegativeControlSingleNodeInstanceFailsAtR3 is the check on the check.
//
// The snapshot is exactly what the false-HA state produces and exactly what ADR-020
// A0 exists to catch: an instance whose objects are all single-replica, judged
// against the R3 it claims. If this suite ever passes that, every green run it has
// ever produced meant nothing — which is the same defect one level up.
//
// It asserts the checks that fire BY NAME rather than merely that something
// failed. "It failed" is compatible with the suite tripping over an unrelated
// vacuity guard and never examining replication at all.
func TestNegativeControlSingleNodeInstanceFailsAtR3(t *testing.T) {
	exp := testExpectation(3)
	// The observation comes from a single-node broker: R1 everywhere, no cluster
	// block, one pod. The EXPECTATION says R3, because that is what the operator
	// asked for and what the artifacts claim.
	snap := healthy(testExpectation(1))
	rep := Verify(snap, exp)

	if rep.OK() {
		t.Fatalf("THE NEGATIVE CONTROL FAILED: a wholly single-replica instance passed "+
			"an R3 check. Every green run of this suite is now meaningless.\n%s", rep.Format())
	}
	mustFail(t, rep, "A1", "is configured for 1 replica(s), want 3")
	mustFail(t, rep, "A2", "is configured for 1 replica(s), want 3") // dc_leases by name
	mustFail(t, rep, "A4", "is configured for 1 replica(s), want 3") // the $MQTT_* lever
	// The consumer fails on the leader term rather than the peer-count one: a
	// single-node broker reports no cluster at all for its consumer groups, so
	// there is no leader to have peers beside. Asserting the peer-count message
	// here would have been asserting a state this snapshot cannot produce.
	mustFail(t, rep, "A3", "no RAFT leader")
	mustFail(t, rep, "A5", "broker pod(s) observed, want 3") // placement
}

// --- the ways a PLUMBED cluster fakes replication ---------------------------

// TestStalePeerFails is the failure a naive check misses. Config.Replicas reads 3,
// there is a leader, the peer count is right — and one peer is still catching up,
// so the data exists in fewer places than the number says.
func TestStalePeerFails(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	snap.Objects[0].Peers[1].Current = false
	rep := Verify(snap, exp)
	mustFail(t, rep, "A1", "NOT CURRENT")
}

func TestOfflinePeerFails(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	snap.Objects[0].Peers[0].Offline = true
	rep := Verify(snap, exp)
	mustFail(t, rep, "A1", "OFFLINE")
}

// TestPeerCountShortFails covers the object that says R3 and runs a two-wide RAFT
// group — the shape a partially completed lift leaves behind.
func TestPeerCountShortFails(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	snap.Objects[0].Peers = snap.Objects[0].Peers[:1]
	rep := Verify(snap, exp)
	mustFail(t, rep, "A1", "peer(s) beside the leader, want 2")
}

func TestNoLeaderFails(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	snap.Objects[0].Leader = ""
	rep := Verify(snap, exp)
	mustFail(t, rep, "A1", "no RAFT leader")
}

// --- A2: the lease bucket is named, not swept -------------------------------

func TestMissingLeaseBucketIsItsOwnFinding(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	snap.Objects = dropObject(snap.Objects, exp.LeaseBucket)
	rep := Verify(snap, exp)
	mustFail(t, rep, "A2", "MISSING")
	if got := find(rep, "A2"); len(got) != 1 || !strings.Contains(got[0].Message, "ADR-070") {
		t.Fatalf("the lease bucket must fail as an ADR-070-named assertion, not as an "+
			"anonymous row of a loop; got %v", got)
	}
}

func TestUnderReplicatedLeaseBucketIsItsOwnFinding(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	setObject(t, snap.Objects, exp.LeaseBucket, func(o *Object) {
		o.Replicas = 1
		o.Leader = ""
		o.Peers = nil
	})
	rep := Verify(snap, exp)
	mustFail(t, rep, "A2", "want 3")
	// And it must NOT ALSO be reported by the generic sweep: one defect, one
	// finding, or a finding count stops meaning anything.
	for _, f := range find(rep, "A1") {
		if f.Object == exp.LeaseBucket {
			t.Fatalf("the lease bucket was reported twice (A2 and A1); dedup is broken")
		}
	}
}

// --- A4: the MQTT lever ------------------------------------------------------

// TestMissingMqttStreamsFail pins that an instance where no device has ever
// connected does NOT pass. The streams' replica factor is decided once, by the
// broker, at the first MQTT connect — so their absence means the lever was never
// pulled, which is not the same as it being correct.
func TestMissingMqttStreamsFail(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	for _, n := range exp.MqttStreams {
		snap.Objects = dropObject(snap.Objects, n)
	}
	rep := Verify(snap, exp)
	mustFail(t, rep, "A4", "MISSING")
	if len(find(rep, "A4")) != len(exp.MqttStreams) {
		t.Fatalf("every absent MQTT stream must be named; got %d finding(s) for %d stream(s)",
			len(find(rep, "A4")), len(exp.MqttStreams))
	}
}

// --- A3: consumer groups -----------------------------------------------------

// TestConsumerThatDidNotRemapFails is the assertion that turns "nats-server says
// consumer groups follow their stream" from a claim about upstream code into an
// observation about this topology.
func TestConsumerThatDidNotRemapFails(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	snap.Consumers[0].Peers = nil
	rep := Verify(snap, exp)
	mustFail(t, rep, "A3", "did not follow its stream to 3 replicas")
}

// --- A5: placement -----------------------------------------------------------

// TestCoLocatedPodsFail is the one-word-change failure: flipping the topology
// spread constraint to ScheduleAnyway lets all three servers land on one node.
// That state satisfies every other assertion in this file — three peers, R3, all
// current — and survives zero node losses.
func TestCoLocatedPodsFail(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	for i := range snap.Pods {
		snap.Pods[i].Node = "node-0"
	}
	rep := Verify(snap, exp)
	mustFail(t, rep, "A5", "distinct node(s)")
}

// --- vacuity: the failure mode that produced A0 -----------------------------

// TestEmptySnapshotFails is the guard against the shape this whole workstream is
// about: a check that examines nothing and reports success.
func TestEmptySnapshotFails(t *testing.T) {
	exp := testExpectation(3)
	rep := Verify(Snapshot{}, exp)
	mustFail(t, rep, "A1", "no JetStream objects were observed at all")
	mustFail(t, rep, "A3", "no durable consumers were observed")
}

// TestNoConsumersObservedFails covers the half-empty case: every stream present
// and correct, but the consumer sweep returned nothing. Without this the A3
// assertion is satisfied by an empty loop.
func TestNoConsumersObservedFails(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	snap.Consumers = nil
	rep := Verify(snap, exp)
	mustFail(t, rep, "A3", "no durable consumers were observed")
}

// TestPlacementRequiredButNotCollectedFails pins that an uncollected placement is
// a failure, not a skip. The alternative — treating an empty pod list as "not
// checked, therefore fine" — is how an axis silently leaves a suite.
func TestPlacementRequiredButNotCollectedFails(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	snap.Pods = nil
	rep := Verify(snap, exp)
	mustFail(t, rep, "A5", "0 broker pod(s) observed, want 3")
}

func TestNonsenseReplicaFactorIsRefused(t *testing.T) {
	exp := testExpectation(3)
	exp.Replicas = 0
	rep := Verify(healthy(testExpectation(3)), exp)
	mustFail(t, rep, "A0", "not a topology")
}

// --- the sweep: objects nobody thought to name ------------------------------

// TestUnnamedInScopeObjectIsStillChecked is what covers a stream added after this
// file was last edited. Without the prefix sweep the suite only ever checks what
// it already knew about, and a new stream is replicated or not with nobody
// watching.
func TestUnnamedInScopeObjectIsStillChecked(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	snap.Objects = append(snap.Objects, Object{
		Name: "i_a-stream-added-next-year", Replicas: 1,
	})
	rep := Verify(snap, exp)
	mustFail(t, rep, "A1", "in-scope object")
}

// TestOutOfScopeObjectIsIgnored is the counterweight: a broker may host more than
// one instance, and failing this instance's check over another's objects would
// make the suite unusable on a shared broker — the surest route to it being
// switched off.
func TestOutOfScopeObjectIsIgnored(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	snap.Objects = append(snap.Objects, Object{
		Name: "otherinstance_inbound-events", Replicas: 1,
	})
	rep := Verify(snap, exp)
	if !rep.OK() {
		t.Fatalf("another instance's under-replicated stream must not fail this "+
			"instance's check:\n%s", rep.Format())
	}
}

func TestMissingCacheBucketFails(t *testing.T) {
	exp := testExpectation(3)
	snap := healthy(exp)
	snap.Objects = dropObject(snap.Objects, "KV_i_device-management_device-by-token")
	rep := Verify(snap, exp)
	mustFail(t, rep, "A1", "no cache KV bucket with this suffix exists")
}

// --- the report itself -------------------------------------------------------

// TestFormatLeadsWithCounts pins that the counts are printed on a PASS. "PASS"
// over zero objects and "PASS" over forty are the same word, and a reader can only
// tell them apart if the numbers are on screen.
func TestFormatLeadsWithCounts(t *testing.T) {
	exp := testExpectation(3)
	out := Verify(healthy(exp), exp).Format()
	if !strings.Contains(out, "PASS") {
		t.Fatalf("expected a pass; got %s", out)
	}
	for _, want := range []string{"examined", "object(s)", "durable consumer(s)", "broker pod(s)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("a passing report must state what it examined; %q missing from:\n%s", want, out)
		}
	}
}

func dropObject(objs []Object, name string) []Object {
	out := objs[:0:0]
	for _, o := range objs {
		if o.Name != name {
			out = append(out, o)
		}
	}
	return out
}

func setObject(t *testing.T, objs []Object, name string, mutate func(*Object)) {
	t.Helper()
	for i := range objs {
		if objs[i].Name == name {
			mutate(&objs[i])
			return
		}
	}
	t.Fatalf("fixture has no object named %q, so the case below asserts nothing", name)
}
