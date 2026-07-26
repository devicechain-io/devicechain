// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/replication"
	nats "github.com/nats-io/nats.go"
)

// This file is the half of the ADR-020 A0 replication check that a pure test
// cannot reach.
//
// replication.Verify is exercised exhaustively against synthetic snapshots in its
// own package, including the negative control. What that CANNOT establish is that
// replication.Collect builds a snapshot out of the broker rather than out of the
// expectation it was handed. A collector that copied Expectation.Replicas into
// every Object would pass every test in that package and every happy-path check
// on a real cluster, and would report PASS on the exact false-HA state the whole
// workstream exists to catch.
//
// So the assertions here are about the READ, not the predicate: an object created
// at R1 on a genuinely clustered broker must come back as R1, sitting next to
// objects created at R3 that come back as R3. It lives in messaging rather than
// replication because that is where the in-process 3-node cluster harness is.

// replicationTestExpectation names exactly the objects the case creates. It is
// built by hand rather than by ReplicationExpectation because an in-process
// cluster hosts no MQTT gateway and none of the 16 instance streams — an
// expectation demanding those would fail for reasons that have nothing to do with
// what is under test.
func replicationTestExpectation(t *testing.T, replicas int, streams []string) replication.Expectation {
	t.Helper()
	return replication.Expectation{
		Replicas: replicas,
		Streams:  streams,
		Prefixes: []string{"repl_"},
		// Placement is a Kubernetes fact; an in-process cluster has no pods, and
		// requiring them here would assert something this harness cannot observe.
		RequirePods: 0,
	}
}

// TestCollectReadsReplicasFromTheBrokerNotTheExpectation is the load-bearing case.
func TestCollectReadsReplicasFromTheBrokerNotTheExpectation(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()

	mustAddStream(t, nmgr.js, "repl_three", 3)
	mustAddStream(t, nmgr.js, "repl_one", 1)
	mustAddDurable(t, nmgr.js, "repl_three", "repl_three_durable")
	waitForReplicated(t, nmgr.js, "repl_three", 3)

	exp := replicationTestExpectation(t, 3, []string{"repl_three", "repl_one"})
	snap, err := replication.Collect(nmgr.js, exp)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	three := objectNamed(t, snap, "repl_three")
	one := objectNamed(t, snap, "repl_one")
	if three.Replicas != 3 || one.Replicas != 1 {
		t.Fatalf("Collect must report each object's OWN replica factor as the broker "+
			"holds it; got repl_three=%d repl_one=%d. Identical values here would mean "+
			"the collector is echoing the expectation, which would make every check "+
			"in this package vacuous", three.Replicas, one.Replicas)
	}
	// The peer terms are the other thing a synthetic snapshot cannot establish:
	// Cluster.Replicas EXCLUDES the leader, so a correct R3 read has TWO peers. A
	// collector that included the leader would report three, and the verifier's
	// "want replicas-1" term would then fail every healthy cluster — or, if someone
	// "fixed" it by comparing against replicas, would pass an R2 group.
	if three.Leader == "" {
		t.Fatalf("an R3 stream on a 3-node cluster must report a leader; got %+v", three)
	}
	if len(three.Peers) != 2 {
		t.Fatalf("Cluster.Replicas excludes the leader, so a healthy R3 stream has 2 "+
			"peers; got %d (%+v)", len(three.Peers), three.Peers)
	}
	for _, p := range three.Peers {
		if !p.Current || p.Offline {
			t.Fatalf("peer %q on a settled cluster should be current and online; got %+v", p.Name, p)
		}
	}
}

// TestVerifyFailsTheUnliftedObjectOnARealCluster runs the whole pipeline —
// collect from a live 3-node broker, then verify — over a broker that is
// genuinely clustered and genuinely holds an R1 object.
//
// This is the false-HA state itself, not a model of it: three healthy peers, a
// real RAFT meta group, and a stream that survives no node loss. It is the
// closest CI can get to the drill, and it is what stops the drill being the only
// thing standing between that state and a green "HA" claim.
func TestVerifyFailsTheUnliftedObjectOnARealCluster(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()

	mustAddStream(t, nmgr.js, "repl_lifted", 3)
	mustAddStream(t, nmgr.js, "repl_unlifted", 1)
	mustAddDurable(t, nmgr.js, "repl_lifted", "repl_lifted_durable")
	waitForReplicated(t, nmgr.js, "repl_lifted", 3)

	exp := replicationTestExpectation(t, 3, []string{"repl_lifted", "repl_unlifted"})
	snap, err := replication.Collect(nmgr.js, exp)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	rep := replication.Verify(snap, exp)
	if rep.OK() {
		t.Fatalf("a real clustered broker holding a single-replica stream passed an R3 "+
			"check — this is the false-HA state itself:\n%s", rep.Format())
	}
	if !strings.Contains(rep.Format(), "repl_unlifted") {
		t.Fatalf("the report must NAME the object that is not replicated; got:\n%s", rep.Format())
	}
	// And the lifted stream must NOT be reported: a check that fails everything
	// once anything is wrong cannot tell an operator what to fix, and would pass
	// the negative control for the wrong reason.
	for _, f := range rep.Findings {
		if f.Object == "repl_lifted" {
			t.Fatalf("a correctly replicated stream was reported as a failure: %s", f)
		}
	}
}

// TestCollectPassesAFullyReplicatedCluster is the positive control for the
// collector: everything created at R3 must come back clean. Without it, a
// collector that reported every object as broken would satisfy the case above.
func TestCollectPassesAFullyReplicatedCluster(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()

	names := []string{"repl_a", "repl_b"}
	for _, n := range names {
		mustAddStream(t, nmgr.js, n, 3)
		mustAddDurable(t, nmgr.js, n, n+"_durable")
	}
	for _, n := range names {
		waitForReplicated(t, nmgr.js, n, 3)
	}
	// The consumer groups remap onto the stream's peers asynchronously, so give
	// them the same settle the streams get rather than racing them.
	waitForConsumersReplicated(t, nmgr.js, names, 3)

	exp := replicationTestExpectation(t, 3, names)
	snap, err := replication.Collect(nmgr.js, exp)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	rep := replication.Verify(snap, exp)
	if !rep.OK() {
		t.Fatalf("a fully replicated cluster must pass:\n%s", rep.Format())
	}
	if rep.Checked.Consumers == 0 {
		t.Fatal("the consumer axis examined nothing, so its pass means nothing")
	}
}

// TestCollectCountsConsumersPerStream pins that consumers are collected against
// the stream they belong to. A collector that dropped them would leave A3 with an
// empty set, and Verify's vacuity guard is the only thing that would notice.
func TestCollectCountsConsumersPerStream(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()

	mustAddStream(t, nmgr.js, "repl_c", 3)
	mustAddDurable(t, nmgr.js, "repl_c", "repl_c_one")
	mustAddDurable(t, nmgr.js, "repl_c", "repl_c_two")

	exp := replicationTestExpectation(t, 3, []string{"repl_c"})
	snap, err := replication.Collect(nmgr.js, exp)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range snap.Consumers {
		if c.Stream != "repl_c" {
			t.Fatalf("consumer %q attributed to stream %q", c.Name, c.Stream)
		}
		seen[c.Name] = true
	}
	if !seen["repl_c_one"] || !seen["repl_c_two"] {
		t.Fatalf("both durables must be collected; got %v", seen)
	}
}

func mustAddStream(t *testing.T, js nats.JetStreamContext, name string, replicas int) {
	t.Helper()
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     name,
		Subjects: []string{name + ".>"},
		Storage:  nats.FileStorage,
		Replicas: replicas,
	}); err != nil {
		t.Fatalf("adding stream %s at R%d: %v", name, replicas, err)
	}
}

func mustAddDurable(t *testing.T, js nats.JetStreamContext, stream, durable string) {
	t.Helper()
	if _, err := js.AddConsumer(stream, &nats.ConsumerConfig{
		Durable:   durable,
		AckPolicy: nats.AckExplicitPolicy,
	}); err != nil {
		t.Fatalf("adding durable %s on %s: %v", durable, stream, err)
	}
}

func objectNamed(t *testing.T, snap replication.Snapshot, name string) replication.Object {
	t.Helper()
	for _, o := range snap.Objects {
		if o.Name == name {
			return o
		}
	}
	t.Fatalf("Collect did not return %q; it collected %d object(s). An object the "+
		"expectation NAMES going missing from the snapshot means the check silently "+
		"stopped covering it", name, len(snap.Objects))
	return replication.Object{}
}

// waitForConsumersReplicated blocks until every durable on the named streams has
// settled onto a full peer set.
//
// A consumer group is created on the stream's leader and remapped onto the
// stream's peers afterwards, so reading it immediately catches a legitimately
// transient state. Waiting is not papering over a race: the assertion is that the
// group ARRIVES at the stream's width, and "eventually" is the honest form of it.
func waitForConsumersReplicated(t *testing.T, js nats.JetStreamContext, streams []string, want int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		settled := true
		for _, s := range streams {
			for ci := range js.ConsumersInfo(s) {
				if ci == nil || ci.Cluster == nil || len(ci.Cluster.Replicas) != want-1 {
					settled = false
					continue
				}
				for _, p := range ci.Cluster.Replicas {
					if !p.Current || p.Offline {
						settled = false
					}
				}
			}
		}
		if settled {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer groups on %v never reached %d replicas with all peers "+
				"current — JetStream is documented to remap them onto the stream's "+
				"peers, and this is the assertion that it does so in our topology", streams, want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
