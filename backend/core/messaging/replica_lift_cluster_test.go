// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/kv"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// freePorts reserves n DISTINCT ephemeral ports and releases them, so the cluster
// route URLs can be written before the servers that will listen on them start.
//
// Distinct is not a nicety. Reserving them one at a time and releasing each
// immediately lets the kernel hand the same port out twice, and a triple
// containing a duplicate guarantees one server cannot bind its cluster port —
// measured at 13 collisions in 3000 triples, which is a meaningful slice of an
// 18%-per-run failure rate. Holding all n listeners open until every port has been
// chosen makes a duplicate impossible.
//
// The release-to-bind race remains and cannot be designed away here, which is why
// newTestCluster RETRIES the whole construction rather than failing on the first
// server that does not come up.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	listeners := make([]net.Listener, 0, n)
	ports := make([]int, 0, n)
	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			for _, open := range listeners {
				open.Close()
			}
			t.Fatalf("reserving a port: %v", err)
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	for _, l := range listeners {
		l.Close()
	}
	return ports
}

// newTestCluster starts a 3-node JetStream cluster in-process and returns a
// NatsManager connected to it, plus cleanup.
//
// This exists so the replica lift can be asserted in CI rather than only on a real
// cluster. The lift is the load-bearing half of A0 — a fresh install replicates
// correctly whether or not the reconcile works, so only an EXISTING single-replica
// cluster being raised in place proves anything — and a check that can only run in
// a manual drill is a check that runs once.
func newTestCluster(t *testing.T) (*NatsManager, func()) {
	t.Helper()
	// Retry the whole construction. A server that never becomes ready is almost
	// always the release-to-bind race on an ephemeral port, not a product fault,
	// and failing on the first attempt made the package red roughly one run in six
	// — with a message ("clustered nats server 1 not ready") that reads like a
	// broken cluster, which is the worst kind of flake because it trains everyone
	// to re-run instead of investigate.
	const attempts = 3
	for attempt := 1; ; attempt++ {
		nmgr, cleanup, err := tryNewTestCluster(t)
		if err == nil {
			return nmgr, cleanup
		}
		if attempt == attempts {
			t.Fatalf("could not start a 3-node JetStream cluster in %d attempts: %v", attempts, err)
		}
		t.Logf("cluster attempt %d/%d failed (%v); retrying on fresh ports", attempt, attempts, err)
	}
}

// tryNewTestCluster is one attempt, returning an error rather than failing the
// test so newTestCluster can retry.
func tryNewTestCluster(t *testing.T) (*NatsManager, func(), error) {
	t.Helper()
	const size = 3
	clusterPorts := freePorts(t, size)
	routes := ""
	for _, p := range clusterPorts {
		routes += fmt.Sprintf("nats-route://127.0.0.1:%d,", p)
	}
	routes = routes[:len(routes)-1]

	servers := make([]*natsserver.Server, 0, size)
	shutdown := func() {
		for _, s := range servers {
			s.Shutdown()
		}
	}
	for i := 0; i < size; i++ {
		opts := &natsserver.Options{
			Host:       "127.0.0.1",
			Port:       -1,
			ServerName: fmt.Sprintf("n%d", i+1),
			JetStream:  true,
			StoreDir:   t.TempDir(),
			Cluster: natsserver.ClusterOpts{
				Name: "dctest",
				Host: "127.0.0.1",
				Port: clusterPorts[i],
			},
			Routes: natsserver.RoutesFromStr(routes),
		}
		srv, err := natsserver.NewServer(opts)
		if err != nil {
			shutdown()
			return nil, nil, fmt.Errorf("new clustered nats server %d: %w", i, err)
		}
		go srv.Start()
		servers = append(servers, srv)
	}
	for i, srv := range servers {
		if !srv.ReadyForConnections(15 * time.Second) {
			shutdown()
			return nil, nil, fmt.Errorf("clustered nats server %d not ready", i)
		}
	}
	// Wait for all three servers to have JOINED the JetStream meta group — not
	// merely for a leader to exist.
	//
	// The difference is the whole reason this helper is not a one-liner. A meta
	// leader is elected as soon as a quorum forms, and in the window before the
	// third peer joins, JetStream will happily place an R1 stream but rejects R3
	// with "no suitable peers for placement". A leader-only gate therefore lets the
	// tests start against a cluster that cannot yet do the one thing they exist to
	// assert, and the resulting failure reads as "an existing cluster was NOT
	// lifted" — indistinguishable from the product bug, which is the worst possible
	// flake because it trains everyone to re-run instead of investigate. Measured at
	// roughly one run in ten before this gate.
	deadline := time.Now().Add(30 * time.Second)
	for {
		ready := false
		for _, srv := range servers {
			if len(srv.JetStreamClusterPeers()) == size {
				ready = true
				break
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			shutdown()
			return nil, nil, fmt.Errorf("JetStream meta group never reached %d peers", size)
		}
		time.Sleep(100 * time.Millisecond)
	}

	nc, err := nats.Connect(servers[0].ClientURL())
	if err != nil {
		shutdown()
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		shutdown()
		return nil, nil, fmt.Errorf("jetstream: %w", err)
	}
	nmgr := &NatsManager{
		Microservice: &core.Microservice{InstanceId: "test", FunctionalArea: "area"},
		nc:           nc,
		js:           js,
	}
	// Assert the PRODUCTION predicate, not a restatement of it. An earlier version of
	// this harness set a cached `clustered` field by copying the expression out of
	// nats.go, which meant the real probe was never executed by any test — deleting
	// it from production code passed the whole package.
	if !nmgr.brokerIsClustered() {
		nc.Close()
		shutdown()
		t.Fatal("brokerIsClustered() is false against a real 3-node cluster; every replica factor " +
			"would be clamped to 1 and the whole workstream would be inert")
	}
	return nmgr, func() {
		nc.Close()
		shutdown()
	}, nil
}

// jsGroupNotServingYet reports whether err is one of the transients a JetStream
// group returns in the window between "this stream exists" and "this stream is
// serving requests".
//
// 🔑 THAT WINDOW IS THE ONE THING newTestCluster's READINESS GATE CANNOT COVER.
// The gate waits for the META group to reach three peers, which is necessary — an
// R3 stream cannot even be placed before it — but every stream and every KV bucket
// then forms its OWN RAFT group, elects its OWN leader, and has to have its subject
// interest propagated over the routes to whichever server the client happens to be
// attached to. Operations issued into that window fail in two distinct ways, and
// both were observed failing CI on `main`:
//
//   - ErrNoStreamResponse ("no response from stream") — the no-responders FAST
//     PATH, not a timeout. It comes back in milliseconds, which is why the failing
//     test took 0.9s rather than hitting any deadline.
//   - context deadline exceeded / ErrTimeout on AddConsumer — the JS API request
//     reaches the meta leader but the stream's own group has no leader to service
//     it yet.
//
// Neither is a product fault and neither is a slow machine per se; starving the box
// only widens the window. Reproduced locally with `taskset -c 0,1` at 2 failures in
// 6 runs, matching the CI shape exactly.
func jsGroupNotServingYet(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, nats.ErrNoStreamResponse) || errors.Is(err, nats.ErrNoResponders) ||
		errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// nats.go does not wrap every transport error into a typed identity, so match
	// the two remaining texts as a fallback rather than let an untyped one through.
	msg := err.Error()
	return strings.Contains(msg, "no response from stream") ||
		strings.Contains(msg, "no responders available")
}

// retryWhileGroupSettles runs op until it succeeds, until it fails for a reason
// that is NOT the settling window, or until the deadline.
//
// 🔴 THE FAIL-FAST BRANCH IS WHAT KEEPS THIS FROM BEING A FLAKE-HIDER. A blanket
// "retry until it works" around a JetStream call would swallow a genuine product
// regression — exactly the outcome this whole change exists to prevent, since a
// gate that cannot fail is worse than a gate that fails intermittently. So a
// non-transient error fails the test on the FIRST attempt with the original error,
// and a transient that never clears still fails when the deadline passes, naming
// how many attempts it took. Only the documented settling transients are retried.
func retryWhileGroupSettles(t *testing.T, what string, op func() error) {
	t.Helper()
	if err := settleRetry(op, 30*time.Second, 50*time.Millisecond); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// settleRetry is the decision logic behind retryWhileGroupSettles, split out so it
// can be tested in both failure directions — see TestSettleRetry*. A helper whose
// fail-fast branch is never exercised is indistinguishable from a blanket retry,
// which is the thing that must not ship here.
func settleRetry(op func() error, timeout, gap time.Duration) error {
	deadline := time.Now().Add(timeout)
	attempts := 0
	for {
		attempts++
		err := op()
		if err == nil {
			return nil
		}
		if !jsGroupNotServingYet(err) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the group never began serving — still %w after %d attempts over %s; "+
				"this is NOT the ordinary settling window, treat it as a real failure",
				err, attempts, timeout)
		}
		time.Sleep(gap)
	}
}

// waitForStreamLeader blocks until a stream's own RAFT group reports a leader,
// which is the minimum precondition for creating a consumer on it.
//
// Distinct from waitForReplicated, which additionally requires the full peer set to
// be current — the right gate before asserting on replication, but more than a
// consumer needs, and it cannot be used on a stream whose replica factor is still
// the thing under test.
func waitForStreamLeader(t *testing.T, js nats.JetStreamContext, stream string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := js.StreamInfo(stream)
		if err == nil && info.Cluster != nil && info.Cluster.Leader != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream %q never elected a leader within 30s (last err: %v)", stream, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForReplicated blocks until a stream reports the wanted replica count with a
// leader and every peer current, or fails the test.
//
// The wait is not test scaffolding papering over a race — it is the operation's
// actual shape, and it is worth stating plainly because it has an operational
// consequence. Raising a replica factor is a RAFT peer-set reconfiguration
// followed by the new peers catching up on the existing data. Until they have,
// the stream reports its new replica count while its peers are NOT current, and
// writes can fail outright: the first draft of this test wrote to the bucket
// immediately after the lift and got `nats: timeout`.
//
// So an in-place lift is a brief write-availability event, not a config edit.
// Rolling every service simultaneously through one (A0's Check C) should expect
// transient publish failures during the catch-up window, and the size of that
// window scales with how much data each stream holds — which on a real cluster
// with populated hot streams is far more than this test moves.
func waitForReplicated(t *testing.T, js nats.JetStreamContext, stream string, want int) *nats.StreamInfo {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last *nats.StreamInfo
	for time.Now().Before(deadline) {
		info, err := js.StreamInfo(stream)
		if err == nil {
			last = info
			if info.Config.Replicas == want && info.Cluster != nil && info.Cluster.Leader != "" &&
				len(info.Cluster.Replicas) == want-1 {
				allCurrent := true
				for _, p := range info.Cluster.Replicas {
					if !p.Current || p.Offline {
						allCurrent = false
						break
					}
				}
				if allCurrent {
					return info
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if last == nil {
		t.Fatalf("stream %q never became readable", stream)
	}
	t.Fatalf("stream %q did not become fully replicated at %d within the deadline: replicas=%d "+
		"leader=%q peers=%d", stream, want, last.Config.Replicas,
		func() string {
			if last.Cluster == nil {
				return ""
			}
			return last.Cluster.Leader
		}(),
		func() int {
			if last.Cluster == nil {
				return 0
			}
			return len(last.Cluster.Replicas)
		}())
	return nil
}

// TestExistingBucketIsLiftedToReplicatedOnUpgrade is A0's Check B, in miniature and
// in CI.
//
// The shape it guards is one this file has shipped twice before: a setting applied
// only at creation. A fresh install gets it and looks fine; every cluster that
// already existed keeps its original configuration forever, reports no error, and
// is indistinguishable from a correct one until the day the thing it was supposed
// to protect against actually happens. For replication that day is a node loss.
//
// So the bucket here is deliberately created R1 AND WRITTEN TO before the lift, and
// the assertions afterwards are not only "it is R3 now" but "it is the same bucket"
// — because a reconcile that satisfied the replica assertion by recreating the
// bucket would pass a naive check while destroying exactly what replication exists
// to preserve. For dc_leases specifically, a revision reset is a fence
// invalidation: Fence.RejectIfStale compares epochs, so a bucket whose revisions
// restart at 1 makes a legitimate new owner look stale and blocks the failover.
func TestExistingBucketIsLiftedToReplicatedOnUpgrade(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()

	// --- Before: an instance that is not configured for HA. ---
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 1

	const logical = "leases"
	bucket := "test_" + logical
	store, err := nmgr.KeyValueStore(logical, bucket, time.Hour)
	if err != nil {
		t.Fatalf("creating the bucket at one replica: %v", err)
	}
	// Even a single-replica stream in a cluster needs its RAFT group to elect a
	// leader before it accepts writes.
	//
	// 🔴 THIS WAIT IS NECESSARY BUT NOT SUFFICIENT, which is what the original
	// version of this comment got wrong. A leader in StreamInfo does not mean the
	// subject's interest has reached the server THIS client is attached to, and
	// until it has, a write comes back ErrNoStreamResponse on the no-responders
	// fast path — in milliseconds, nowhere near any deadline. The identical
	// construction at the bottom of this file failed exactly that way in CI.
	waitForReplicated(t, nmgr.js, kvStreamPrefix+bucket, 1)
	var lastRev uint64
	for i := 0; i < 8; i++ {
		key := kvKey(fmt.Sprintf("holder-%d", i))
		var rev uint64
		retryWhileGroupSettles(t, fmt.Sprintf("put %s", key), func() error {
			var err error
			rev, err = store.Put(key, []byte("owner"))
			return err
		})
		lastRev = rev
	}

	before, err := nmgr.js.StreamInfo(kvStreamPrefix + bucket)
	if err != nil {
		t.Fatalf("stream info before: %v", err)
	}
	if before.Config.Replicas != 1 {
		t.Fatalf("setup did not produce a single-replica bucket (got %d); this test would then "+
			"prove nothing about the lift", before.Config.Replicas)
	}
	if lastRev == 0 {
		t.Fatal("setup wrote no revisions; a revision reset would be undetectable")
	}
	msgsBefore := before.State.Msgs

	// --- The upgrade: the operator raises the replica factor and the pods roll. ---
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	if got := nmgr.effectiveStreamReplicas(); got != 3 {
		t.Fatalf("effectiveStreamReplicas() = %d on a real cluster, want 3", got)
	}
	// KeyValueStore on an existing bucket is what a restarting pod runs.
	if _, err := nmgr.KeyValueStore(logical, bucket, time.Hour); err != nil {
		t.Fatalf("reopening the bucket after the upgrade: %v", err)
	}

	// --- After: replicated, and still the same bucket. ---
	// Check the replica count first with a plain read, so a reconcile that did
	// nothing at all fails with the diagnostic that names the actual bug rather
	// than with a generic settle timeout.
	raw, err := nmgr.js.StreamInfo(kvStreamPrefix + bucket)
	if err != nil {
		t.Fatalf("stream info after: %v", err)
	}
	if raw.Config.Replicas != 3 {
		t.Fatalf("bucket still has %d replica(s) after the upgrade, want 3. An existing cluster "+
			"was NOT lifted — this is the fresh-install-only trap the reconcile exists to close",
			raw.Config.Replicas)
	}
	after := waitForReplicated(t, nmgr.js, kvStreamPrefix+bucket, 3)
	if after.State.Msgs != msgsBefore {
		t.Errorf("message count changed across the lift: %d -> %d; the bucket was re-incarnated, "+
			"not reconciled", msgsBefore, after.State.Msgs)
	}

	// The revision counter must continue, not restart. This is the assertion that
	// distinguishes a lift from a recreate, and for the lease bucket it is the
	// difference between a working failover and a fence that rejects its rightful
	// new owner.
	// The write is retried through the settling window, but the ASSERTION below is
	// not weakened by that: what distinguishes a lift from a recreate is the
	// revision NUMBER, not whether the first attempt happened to land. A lift is a
	// RAFT peer-set change followed by catch-up, so a transient refusal here is the
	// operation's real shape — see waitForReplicated's comment.
	var nextRev uint64
	retryWhileGroupSettles(t, "put after the lift", func() error {
		var err error
		nextRev, err = store.Put(kvKey("holder-after"), []byte("owner"))
		return err
	})
	if nextRev <= lastRev {
		t.Errorf("revision went backwards across the lift (%d -> %d): the bucket was recreated, "+
			"which for dc_leases is a fence invalidation — every standby's epoch comparison is "+
			"now against a counter that restarted", lastRev, nextRev)
	}

	// waitForReplicated already asserted the part that matters most about "genuinely
	// R3": a leader plus two peers that are current and not offline. A stream
	// reporting Replicas:3 with a stale peer is NOT replicated, and that is the most
	// likely way a merely-plumbed cluster passes a naive check.
}

// TestExistingStreamIsLiftedToReplicatedOnUpgrade is the same proof for message
// streams, which the original A0 specification omitted entirely — it named only KV
// buckets, while ensureStream had the identical create-time-only gap.
func TestExistingStreamIsLiftedToReplicatedOnUpgrade(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()

	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 1
	const suffix = "inbound-events"

	name, err := nmgr.ensureStream(suffix)
	if err != nil {
		t.Fatalf("ensureStream at one replica: %v", err)
	}
	waitForReplicated(t, nmgr.js, name, 1)
	subject := StreamSubject(nmgr.Microservice.InstanceId, suffix)
	for i := 0; i < 8; i++ {
		retryWhileGroupSettles(t, fmt.Sprintf("publish %d", i), func() error {
			_, err := nmgr.js.Publish(subject, []byte("payload"))
			return err
		})
	}
	before, err := nmgr.js.StreamInfo(name)
	if err != nil {
		t.Fatalf("stream info before: %v", err)
	}
	if before.Config.Replicas != 1 {
		t.Fatalf("setup did not produce a single-replica stream (got %d)", before.Config.Replicas)
	}
	msgsBefore, firstSeqBefore := before.State.Msgs, before.State.FirstSeq
	if msgsBefore == 0 {
		t.Fatal("setup stored no messages; message preservation would be undetectable")
	}

	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	if _, err := nmgr.ensureStream(suffix); err != nil {
		t.Fatalf("ensureStream after the upgrade: %v", err)
	}

	raw, err := nmgr.js.StreamInfo(name)
	if err != nil {
		t.Fatalf("stream info after: %v", err)
	}
	if raw.Config.Replicas != 3 {
		t.Fatalf("stream still has %d replica(s) after the upgrade, want 3", raw.Config.Replicas)
	}
	after := waitForReplicated(t, nmgr.js, name, 3)
	if after.State.Msgs != msgsBefore || after.State.FirstSeq != firstSeqBefore {
		t.Errorf("stream contents changed across the lift (msgs %d->%d, firstSeq %d->%d); it was "+
			"re-created rather than reconciled",
			msgsBefore, after.State.Msgs, firstSeqBefore, after.State.FirstSeq)
	}
}

// TestReconcileRefusesToDeReplicateOnDowngrade is the counterweight. A pod handed
// OLDER config — which is exactly what `helm rollback` does — must not quietly drop
// RAFT peers and remap every durable consumer on its way up.
func TestReconcileRefusesToDeReplicateOnDowngrade(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()

	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	const suffix = "inbound-events"
	name, err := nmgr.ensureStream(suffix)
	if err != nil {
		t.Fatalf("ensureStream at three replicas: %v", err)
	}
	// Wait for the peers to be placed BEFORE attempting the downgrade.
	//
	// Without this the test passes for the wrong reason roughly one run in
	// twenty-five: reconcileStreamReplicas warn-and-swallows an UpdateStream
	// failure, so while the new peers are still catching up the scale-down update
	// simply FAILS, and the stream stays at three replicas whether or not the
	// refusal exists. Measured directly — with the refusal deleted, 24 of 25 runs
	// caught it and one passed. A test that only sometimes notices the guard is
	// gone is worse than none, because the one green run is the one someone sees.
	waitForReplicated(t, nmgr.js, name, 3)

	// The rollback: older config, fewer replicas, same pod restart path.
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 1
	if _, err := nmgr.ensureStream(suffix); err != nil {
		t.Fatalf("ensureStream after the rollback: %v", err)
	}

	after, err := nmgr.js.StreamInfo(name)
	if err != nil {
		t.Fatalf("stream info after: %v", err)
	}
	if after.Config.Replicas != 3 {
		t.Errorf("stream was de-replicated to %d by a starting pod reading older config. A rollback "+
			"of an unrelated release must not drop RAFT peers and remap every durable consumer",
			after.Config.Replicas)
	}
}

// TestReplicationMetricsReportBrokerStateNotConfig is U3's guard, and the reason
// it runs against a real cluster is the peer count.
//
// desired-vs-actual can be faked by reading config twice. peersCurrent cannot: it
// requires a broker that actually has peers, and it is the term that distinguishes
// "replicated" from "labelled replicated". Every other assurance that an instance
// is HA — the rendered value, the tofu plan, the dcctl preflight — is made before
// anything is running and cannot notice that a stream drifted or that a peer never
// caught up. This is the one that can.
func TestReplicationMetricsReportBrokerStateNotConfig(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()
	nmgr.metrics = newStreamMetrics(nmgr.Microservice)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3

	const suffix = "inbound-events"
	name, err := nmgr.ensureStream(suffix)
	if err != nil {
		t.Fatalf("ensureStream: %v", err)
	}
	info := waitForReplicated(t, nmgr.js, name, 3)

	if got := currentPeers(info); got != 3 {
		t.Errorf("currentPeers = %d on a healthy 3-node stream, want 3 (leader plus two current peers)", got)
	}

	// A stream whose peers have not caught up must NOT count them. This is the
	// exact state a naive check misses: Replicas says 3, replication is not real.
	stale := *info
	clusterCopy := *info.Cluster
	clusterCopy.Replicas = []*nats.PeerInfo{
		{Name: "n2", Current: true},
		{Name: "n3", Current: false},
	}
	stale.Cluster = &clusterCopy
	if got := currentPeers(&stale); got != 2 {
		t.Errorf("currentPeers = %d with one lagging peer, want 2; a stale peer must not be counted "+
			"as replication", got)
	}

	offline := stale
	offlineCluster := clusterCopy
	offlineCluster.Replicas = []*nats.PeerInfo{
		{Name: "n2", Current: true, Offline: true},
		{Name: "n3", Current: true},
	}
	offline.Cluster = &offlineCluster
	if got := currentPeers(&offline); got != 2 {
		t.Errorf("currentPeers = %d with one offline peer, want 2", got)
	}

	// An unclustered broker reports no cluster block; the single server holding the
	// stream is one current peer, not zero.
	if got := currentPeers(&nats.StreamInfo{}); got != 1 {
		t.Errorf("currentPeers = %d with no cluster info, want 1", got)
	}

	// The sampler must not blow up on the two tracked sets, and buckets must be
	// sampled for replication.
	if _, err := nmgr.KeyValueStore("leases", "test_leases", time.Hour); err != nil {
		t.Fatalf("KeyValueStore: %v", err)
	}
	buckets := nmgr.trackedBuckets()
	if len(buckets) != 1 || buckets[0] != kvStreamPrefix+"test_leases" {
		t.Fatalf("trackedBuckets = %v, want exactly the leases bucket's backing stream", buckets)
	}
	if streams := nmgr.trackedStreams(); len(streams) != 1 {
		t.Errorf("trackedStreams = %v; buckets must not leak into the stream set, or a bounded "+
			"cache sitting near its ceiling by design fires the near-full alert", streams)
	}
	// Sampling the multi-stream + bucket path together, WITH an assertion — an
	// earlier version ended here with a bare call and no check, which proves only
	// that it does not panic and would pass on any values at all.
	nmgr.metrics.sample(nmgr.js, nmgr.trackedStreams(), buckets, nmgr.desiredStreamReplicas(), nmgr.brokerIsClustered())
	if got := testutil.ToFloat64(nmgr.metrics.brokerClustered); got != 1 {
		t.Errorf("brokerClustered = %v while sampling a real 3-node cluster, want 1: this is "+
			"the gauge that distinguishes a correct single-node install from a cluster "+
			"holding one copy of everything", got)
	}
}

// TestReplicaLiftIsNotBlockedByTheShrinkRefusal proves the ordering inside
// reconcileKvBucket, and it has to run on a real cluster to prove anything at all.
//
// An earlier version of this test ran on the unclustered harness with the replica
// factor unset, which made the replica reconcile inert on either side of the guard
// — restoring the original buggy ordering left the whole package passing. The bug
// it is meant to catch is specific and severe: the State-tier shrink refusal
// RETURNS EARLY, and dc_leases is a State bucket, so reconciling replicas after
// that guard would leave an over-ceiling fence substrate pinned to one node
// forever. Losing that node then blocks every standby's Acquire.
//
// So: a State bucket, deliberately driven over the ceiling it is about to be
// offered, on an instance that has just turned on HA. Both things must happen —
// the replicas go up, and the ceiling is refused.
func TestReplicaLiftIsNotBlockedByTheShrinkRefusal(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()

	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 1
	const logical = "leases"
	const bucket = "test_overfull_leases"
	store, err := nmgr.KeyValueStore(logical, bucket, time.Hour)
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	waitForReplicated(t, nmgr.js, kvStreamPrefix+bucket, 1)

	// Filling the bucket is SETUP, not the assertion — but the first write into a
	// bucket created moments ago can come back ErrNoStreamResponse while the
	// subject's interest is still propagating to the server this client is attached
	// to. That is what failed here in CI, in under a second. Retrying only that
	// transient keeps the setup honest; a real Put failure still fails immediately.
	payload := make([]byte, 4096)
	for i := 0; i < 32; i++ {
		key := kvKey(fmt.Sprintf("k%d", i))
		retryWhileGroupSettles(t, fmt.Sprintf("put %s", key), func() error {
			_, err := store.Put(key, payload)
			return err
		})
	}
	before, err := nmgr.js.StreamInfo(kvStreamPrefix + bucket)
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	tinyCeiling := int64(before.State.Bytes) / 2
	if tinyCeiling <= 0 {
		t.Fatalf("bucket stored %d bytes; the shrink guard would not engage", before.State.Bytes)
	}
	previousMaxBytes := before.Config.MaxBytes

	// The upgrade: HA turned on, and a ceiling this bucket is already over.
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	nmgr.reconcileKvBucket(bucket, tinyCeiling, kv.State)

	after := waitForReplicated(t, nmgr.js, kvStreamPrefix+bucket, 3)
	if after.Config.MaxBytes != previousMaxBytes {
		t.Errorf("MaxBytes changed to %d (was %d): the State-tier shrink refusal did not hold, and "+
			"JetStream will now refuse every write to this bucket until its entries age out",
			after.Config.MaxBytes, previousMaxBytes)
	}
}

// TestReplicationGaugesCarryTheRightNumbers reads the exported gauges back.
//
// Without this the metrics are asserted only by a does-not-panic smoke call, and
// two separate mutations pass: reporting `actual` in place of `desired`, and
// feeding the sampler effectiveStreamReplicas instead of desiredStreamReplicas (NOT caught here — this test supplies the argument itself rather than driving runStreamMetrics, so it restates the call site; that mutation is caught by TestSamplerReportsConfiguredNotClampedReplicas).
// The second is the dangerous one — it looks like a correctness fix, and it makes
// desired equal actual on exactly the unclustered instance whose mismatch the
// whole mechanism exists to surface.
func TestReplicationGaugesCarryTheRightNumbers(t *testing.T) {
	nmgr, cleanup := newTestManager(t)
	defer cleanup()
	nmgr.Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: uniqueArea("gauges")}
	nmgr.metrics = newStreamMetrics(nmgr.Microservice)
	// Unclustered broker plus a replicated configuration: the mismatch case.
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3

	if _, err := nmgr.KeyValueStore("leases", "test_gauges", time.Hour); err != nil {
		t.Fatalf("KeyValueStore: %v", err)
	}
	nmgr.metrics.sample(nmgr.js, nil, nmgr.trackedBuckets(), nmgr.desiredStreamReplicas(), nmgr.brokerIsClustered())

	name := kvStreamPrefix + "test_gauges"
	desired := testutil.ToFloat64(nmgr.metrics.replicasDesired.WithLabelValues(name))
	actual := testutil.ToFloat64(nmgr.metrics.replicasActual.WithLabelValues(name))
	peers := testutil.ToFloat64(nmgr.metrics.peersCurrent.WithLabelValues(name))

	if desired != 3 {
		t.Errorf("replicas_desired = %v, want 3 — the gauge must carry the CONFIGURED value, not the "+
			"clamped one, or an instance that is silently unreplicated reports itself as healthy", desired)
	}
	if actual != 1 {
		t.Errorf("replicas_actual = %v, want 1 (the clamp created it unreplicated)", actual)
	}
	if desired == actual {
		t.Error("desired equals actual on an unclustered instance configured for 3 replicas; the " +
			"mismatch signal this mechanism exists to raise is unreachable")
	}
	if peers != 1 {
		t.Errorf("peers_current = %v, want 1", peers)
	}
}

// A replica lift must not revert the bounds and subject reconcile that runs
// alongside it in the same ensureStream.
//
// The two reconciles are deliberately separate UpdateStream calls, so that a
// failed replica lift cannot veto a subject fix (a stream capturing the wrong
// subjects DROPS MESSAGES; replication is a durability improvement that can wait).
// But separate calls mean the second one writes whatever configuration it is
// handed — so handing it the PRE-update snapshot silently undoes the first,
// after that first update has already logged success.
//
// Every other lift test here changes only the replica factor, which is exactly
// why none of them saw this: with nothing else reconciling, the stale config and
// the fresh one are identical. The real rollout is the combination — raising
// streamReplicas is a values.yaml edit, and streamMaxBytes sits four lines away
// in the same block, so they get changed in the same release.
func TestReplicaLiftDoesNotRevertTheBoundsReconcile(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()

	cfgNats := &nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	cfgNats.StreamReplicas = 1
	cfgNats.StreamMaxBytes = 512 << 20
	const suffix = "inbound-events"

	name, err := nmgr.ensureStream(suffix)
	if err != nil {
		t.Fatalf("ensureStream at one replica: %v", err)
	}
	waitForReplicated(t, nmgr.js, name, 1)

	// Now change BOTH in one release, the way a real HA rollout does.
	cfgNats.StreamReplicas = 3
	cfgNats.StreamMaxBytes = 1 << 30
	if _, err := nmgr.ensureStream(suffix); err != nil {
		t.Fatalf("ensureStream after the upgrade: %v", err)
	}

	after := waitForReplicated(t, nmgr.js, name, 3)
	if after.Config.Replicas != 3 {
		t.Fatalf("stream has %d replica(s), want 3", after.Config.Replicas)
	}
	// The assertion that matters: the ceiling raised in the SAME call survived the
	// replica update that followed it.
	if want := int64(1 << 30); after.Config.MaxBytes != want {
		t.Errorf("MaxBytes = %d after the lift, want %d: the replica update was issued from "+
			"the pre-update config and wrote the old ceiling back over the bounds reconcile "+
			"that had already reported success", after.Config.MaxBytes, want)
	}
}

// brokerIsClustered must go FALSE when the cluster goes away, without a restart.
//
// This is the property the M1 regression got wrong and the only one worth
// pinning. The clustered-ness probe used to be evaluated once at connect and
// cached, which latched false forever on any pod that started before NATS was
// reachable — everything created at one replica on a healthy 3-node cluster. The
// fix was to read it live on every call.
//
// The sibling test in replica_clamp_test.go cannot see that. Its three cases (nil
// conn, closed conn, unclustered broker) all expect false, and
// nats.Conn.ConnectedClusterName() already returns "" for every one of them, so
// they are satisfied by the library rather than by this code — deleting the whole
// guard leaves them green. The state that DISCRIMINATES is a connection that WAS
// clustered and now is not, which needs a real cluster to take away.
func TestBrokerIsClusteredIsReadLiveNotCached(t *testing.T) {
	nmgr, cleanup := newTestCluster(t)
	defer cleanup()

	if !nmgr.brokerIsClustered() {
		t.Fatal("not clustered against a real 3-node cluster; the negative below would prove nothing")
	}

	// Take the cluster away underneath a manager that has already observed it.
	cleanup()

	deadline := time.Now().Add(15 * time.Second)
	for nmgr.brokerIsClustered() {
		if time.Now().After(deadline) {
			t.Fatal("brokerIsClustered() still reports true after every server was shut down: " +
				"the value is being CACHED rather than read live. A pod that observed a " +
				"cluster once would keep claiming replication is possible, and the clamp " +
				"would stop protecting anything")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
