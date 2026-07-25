// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
)

// freePort reserves an ephemeral port and immediately releases it, so a cluster
// route URL can be written before the server that will listen on it starts. There
// is an inherent race between release and bind; it is acceptable in a test and is
// the standard way to build a nats-server cluster in-process.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
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
	const size = 3
	clusterPorts := make([]int, size)
	for i := range clusterPorts {
		clusterPorts[i] = freePort(t)
	}
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
			t.Fatalf("new clustered nats server %d: %v", i, err)
		}
		go srv.Start()
		servers = append(servers, srv)
	}
	for i, srv := range servers {
		if !srv.ReadyForConnections(15 * time.Second) {
			shutdown()
			t.Fatalf("clustered nats server %d not ready", i)
		}
	}
	// Wait for the JetStream meta group to elect a leader; until it has, stream
	// creation fails with "no metadata leader" rather than doing anything useful.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if servers[0].JetStreamIsLeader() || servers[1].JetStreamIsLeader() || servers[2].JetStreamIsLeader() {
			break
		}
		if time.Now().After(deadline) {
			shutdown()
			t.Fatal("no JetStream metadata leader elected; the cluster never formed")
		}
		time.Sleep(100 * time.Millisecond)
	}

	nc, err := nats.Connect(servers[0].ClientURL())
	if err != nil {
		shutdown()
		t.Fatalf("connect: %v", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		shutdown()
		t.Fatalf("jetstream: %v", err)
	}
	nmgr := &NatsManager{
		Microservice: &core.Microservice{InstanceId: "test", FunctionalArea: "area"},
		nc:           nc,
		js:           js,
		clustered:    nc.ConnectedClusterName() != "",
	}
	if !nmgr.clustered {
		nc.Close()
		shutdown()
		t.Fatal("connected server does not report a cluster name; the clustered-ness probe " +
			"effectiveStreamReplicas depends on would clamp every replica factor to 1")
	}
	return nmgr, func() {
		nc.Close()
		shutdown()
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
	// leader before it accepts writes; without this the setup itself flakes with
	// "no response from stream".
	waitForReplicated(t, nmgr.js, kvStreamPrefix+bucket, 1)
	var lastRev uint64
	for i := 0; i < 8; i++ {
		rev, err := store.Put(kvKey(fmt.Sprintf("holder-%d", i)), []byte("owner"))
		if err != nil {
			t.Fatalf("put: %v", err)
		}
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
	nextRev, err := store.Put(kvKey("holder-after"), []byte("owner"))
	if err != nil {
		t.Fatalf("put after the lift: %v", err)
	}
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
		if _, err := nmgr.js.Publish(subject, []byte("payload")); err != nil {
			t.Fatalf("publish: %v", err)
		}
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
	nmgr.metrics.sample(nmgr.js, nmgr.trackedStreams(), buckets, nmgr.desiredStreamReplicas())
}
