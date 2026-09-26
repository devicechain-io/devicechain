// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/kv"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// silentReplicaRig is an R3 cache bucket on a 3-node cluster whose routes can be silenced,
// and a Cache opened on it from a server that does NOT lead the bucket, so the leader can
// be taken away without taking the client's own server with it.
type silentReplicaRig struct {
	servers []*natsserver.Server
	faults  *dctest.RouteFaults
	leader  int
	cache   *Cache
	metrics *streamMetrics
	keys    []string
}

const silentRigKeys = 32

func silentRigValue(key string) string { return "value-of-" + key }

// replicatedCacheManager is a manager connected to one server of the cluster, configured to
// replicate at 3, with its metrics in a registry of its own.
func replicatedCacheManager(t *testing.T, srv *natsserver.Server) (*NatsManager, *prometheus.Registry) {
	t.Helper()
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "area"}
	ms.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	return &NatsManager{Microservice: ms, nc: nc, js: js, metrics: newStreamMetrics(ms)}, reg
}

func newSilentReplicaRig(t *testing.T) *silentReplicaRig {
	t.Helper()
	servers, faults := dctest.StartJetStreamClusterWithRouteFaults(t, 3)

	setup, _ := replicatedCacheManager(t, servers[0])
	writer, err := setup.NewCache(kv.BucketDeviceByToken, time.Hour)
	if err != nil {
		t.Fatalf("create the cache: %v", err)
	}
	stream := kvStreamPrefix + CacheBucketName("test", "area", kv.BucketDeviceByToken)
	waitForReplicated(t, setup.js, stream, 3)

	rig := &silentReplicaRig{servers: servers, faults: faults}
	for i := 0; i < silentRigKeys; i++ {
		key := fmt.Sprintf("tenant|device-%02d", i)
		rig.keys = append(rig.keys, key)
		retryWhileGroupSettles(t, "put "+key, func() error {
			return writer.Set(context.Background(), key, silentRigValue(key))
		})
	}
	// Every replica must hold every key before one of them is silenced: a follower that
	// has not caught up answers a direct get with not-found, which would read as a key
	// that exists coming back absent, and that is the one outcome the test forbids.
	info := waitForReplicated(t, setup.js, stream, 3)
	deadline := time.Now().Add(30 * time.Second)
	for {
		lagging := false
		for _, p := range info.Cluster.Replicas {
			if !p.Current || p.Lag != 0 {
				lagging = true
			}
		}
		if !lagging && info.State.Msgs == silentRigKeys {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the bucket's replicas never caught up: msgs=%d replicas=%+v", info.State.Msgs, info.Cluster.Replicas)
		}
		time.Sleep(100 * time.Millisecond)
		if info, err = setup.js.StreamInfo(stream); err != nil {
			t.Fatalf("stream info: %v", err)
		}
	}

	rig.leader = -1
	for i, s := range servers {
		if s.Name() == info.Cluster.Leader {
			rig.leader = i
		}
	}
	if rig.leader < 0 {
		t.Fatalf("no server is named %q, the bucket's leader", info.Cluster.Leader)
	}
	client := (rig.leader + 1) % len(servers)
	measuring, _ := replicatedCacheManager(t, servers[client])
	rig.metrics = measuring.metrics

	// Wait, on a plain handle, until every key reads back through the measuring server. A
	// follower starts answering direct gets only once it has caught up, and its interest
	// reaches the other servers over the routes after that; a read in that window comes
	// back "no responders". Doing the waiting through the Cache would open its breaker
	// before the test began.
	plain, err := jetstream.New(measuring.nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	bucket := CacheBucketName("test", "area", kv.BucketDeviceByToken)
	settled := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		handle, err := plain.KeyValue(ctx, bucket)
		if err != nil {
			return err
		}
		for round := 0; round < 3; round++ {
			for _, key := range rig.keys {
				if _, err := handle.Get(ctx, kvKey(key)); err != nil {
					return fmt.Errorf("read %q: %w", key, err)
				}
			}
		}
		return nil
	}
	for deadline := time.Now().Add(30 * time.Second); ; {
		err := settled()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the bucket never read back through server %d: %v", client, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if rig.cache, err = measuring.NewCache(kv.BucketDeviceByToken, time.Hour); err != nil {
		t.Fatalf("open the cache from server %d: %v", client, err)
	}

	// Warm up through the measuring cache: every key, three times over, so each replica
	// has had a chance to answer and every one of them answered with the value.
	for round := 0; round < 3; round++ {
		for _, key := range rig.keys {
			var got string
			found, err := rig.cache.Get(context.Background(), key, &got)
			if err != nil || !found || got != silentRigValue(key) {
				t.Fatalf("warm-up read of %q = (%v, %v, %q), want the stored value", key, found, err, got)
			}
		}
	}
	return rig
}

// readSummary is what a run of timed Gets came to. It is aggregated as the reads happen,
// not collected: a bypassed read takes microseconds, and a slice of millions of them is
// a memory problem rather than evidence.
type readSummary struct {
	mu      sync.Mutex
	reads   int
	errored int
	slowest time.Duration
	wrong   []string // reads that returned a wrong outcome; capped
}

func (s *readSummary) record(key string, took time.Duration, found bool, err error, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if took > s.slowest {
		s.slowest = took
	}
	var wrong string
	switch {
	case err != nil:
		s.errored++
		if found {
			wrong = fmt.Sprintf("read of %q returned found=true WITH an error (%v)", key, err)
		}
	case !found:
		wrong = fmt.Sprintf("read of %q reported the key absent with no error; it exists, so a skipped "+
			"or failed lookup was passed off as a miss", key)
	case value != silentRigValue(key):
		wrong = fmt.Sprintf("read of %q returned %q, want %q", key, value, silentRigValue(key))
	}
	if wrong != "" && len(s.wrong) < 10 {
		s.wrong = append(s.wrong, wrong)
	}
}

// readFor issues Gets from the given number of concurrent readers, each going round-robin
// over the keys with a millisecond between reads (standing in for the rest of a resolve),
// for the given time.
func (r *silentReplicaRig) readFor(d time.Duration, readers int) *readSummary {
	summary := &readSummary{}
	end := time.Now().Add(d)
	var wg sync.WaitGroup
	for w := 0; w < readers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; time.Now().Before(end); i++ {
				key := r.keys[i%len(r.keys)]
				var got string
				start := time.Now()
				found, err := r.cache.Get(context.Background(), key, &got)
				summary.record(key, time.Since(start), found, err, got)
				time.Sleep(time.Millisecond)
			}
		}(w)
	}
	wg.Wait()
	return summary
}

// assertReadsStayedFast checks the reads by value: none waited near a JetStream request
// timeout, and none reported a key that exists as absent.
func assertReadsStayedFast(t *testing.T, s *readSummary) {
	t.Helper()
	t.Logf("%d reads, %d errored, slowest %s", s.reads, s.errored, s.slowest)
	for _, w := range s.wrong {
		t.Error(w)
	}
	if s.slowest >= 1500*time.Millisecond {
		t.Errorf("the slowest cache read took %s; a read must give up well before a JetStream request "+
			"timeout (5 s), because each one it waits out holds a resolver that long", s.slowest)
	}
}

// A cache read must not wait out a JetStream request timeout when one replica of the
// bucket has gone silent.
//
// Every replica of a KV bucket answers direct gets, and a request is handed to one of them
// at random. A server that drops off the network without closing its connections stays in
// that draw until the other servers stop hearing its pings, about a minute, so a share of
// the reads go to it and are never answered. Each of those used to wait the full 5 s.
func TestCacheReadsStayFastWhenAReplicaGoesSilent(t *testing.T) {
	rig := newSilentReplicaRig(t)

	rig.faults.Silence(rig.leader)
	reads := rig.readFor(10*time.Second, 1)

	if held := rig.faults.Held(rig.leader); held == 0 {
		t.Fatal("the silence held back no bytes, so no traffic was flowing on the routes and the test proves nothing")
	}
	assertReadsStayedFast(t, reads)
	if reads.errored == 0 {
		t.Fatal("no read reached the silent replica, so the test proves nothing about one that does")
	}
	// On the old cache this was a handful: every read that went to the silent replica
	// waited 5 s. It is a throughput floor, not a latency one — most of the reads that
	// make it up are the bypassed ones, which is the point: while the cache is skipped,
	// its callers are not held.
	if reads.reads < 200 {
		t.Errorf("only %d reads completed in 10 s with one replica silent; the cache is stalling its callers", reads.reads)
	}
}

// A replica whose server is shut down cleanly does not stall a read either. It is not the
// fault above: a clean shutdown closes the server's connections, so the others drop its
// routes and its share of the reads at once. Kept as a check that the bound does not
// depend on which of the two ways a node is lost.
func TestCacheReadsStayFastWhenTheLeaderIsShutDown(t *testing.T) {
	rig := newSilentReplicaRig(t)

	rig.servers[rig.leader].Shutdown()
	assertReadsStayedFast(t, rig.readFor(5*time.Second, 1))
}

// logLines returns the captured JSON log lines whose message contains substr.
func logLines(t *testing.T, captured string, substr string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(captured, "\n") {
		if !strings.Contains(line, substr) {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("a captured log line is not JSON: %q", line)
		}
		out = append(out, entry)
	}
	return out
}

const (
	stoppedAnswering = "A key-value cache stopped answering"
	answeringAgain   = "A key-value cache is answering again"
)

// Several resolvers share one Cache, and the breaker has to stay open for all of them.
//
// When one reader times out and opens it, the others have reads in flight that were
// admitted a moment before and land on replicas that are fine. If any of those successes
// closed the breaker, it would close again at once and the readers would go back to
// waiting out the silent replica; the log would flap between the two lines on every
// failure. So the number of "stopped answering" lines is bounded by how often a probe
// can close the breaker: once at the start, and at most once per bypass window after it.
func TestCacheBreakerHoldsForConcurrentReaders(t *testing.T) {
	rig := newSilentReplicaRig(t)
	logs := captureLogs(t)

	rig.faults.Silence(rig.leader)
	reads := rig.readFor(10*time.Second, 5)

	assertReadsStayedFast(t, reads)
	if reads.errored == 0 {
		t.Fatal("no read reached the silent replica, so the test proves nothing about one that does")
	}
	warns := logLines(t, logs.String(), stoppedAnswering)
	infos := logLines(t, logs.String(), answeringAgain)
	t.Logf("%d %q lines, %d %q lines", len(warns), stoppedAnswering, len(infos), answeringAgain)
	if len(warns) == 0 {
		t.Fatal("the cache never reported that it stopped answering")
	}
	if maxWarns := 1 + int(10*time.Second/cacheBypassFor); len(warns) > maxWarns {
		t.Errorf("the cache reported %d times in 10 s that it stopped answering; the breaker can close at "+
			"most once per %s bypass, so more than %d means a read that was already in flight closed it",
			len(warns), cacheBypassFor, maxWarns)
	}
	if len(infos) > len(warns) {
		t.Errorf("%d recoveries logged against %d failures; a recovery is only ever logged for a cache "+
			"that was reported unavailable", len(infos), len(warns))
	}
}

// A cache that stops answering is reported once, in the log and in the metrics, and
// reported again when it answers.
func TestAnUnavailableCacheIsReportedAndRecovers(t *testing.T) {
	rig := newSilentReplicaRig(t)
	logs := captureLogs(t)
	const name = kv.BucketDeviceByToken

	if got := testutil.ToFloat64(rig.metrics.cacheUnavailable.WithLabelValues(name)); got != 0 {
		t.Fatalf("kv_cache_unavailable = %v on a healthy cache, want 0", got)
	}

	rig.faults.Silence(rig.leader)
	// Shorter than one bypass, so no probe runs: whatever a probe found, one silent stretch
	// is then exactly one "stopped answering" line.
	reads := rig.readFor(cacheBypassFor-time.Second, 1)
	if reads.errored == 0 {
		t.Fatal("no read reached the silent replica, so the test proves nothing about one that does")
	}

	if got := testutil.ToFloat64(rig.metrics.cacheUnavailable.WithLabelValues(name)); got != 1 {
		t.Errorf("kv_cache_unavailable = %v while the cache is being skipped, want 1", got)
	}
	if got := testutil.ToFloat64(rig.metrics.cacheFailures.WithLabelValues(name, "get", "timeout")); got < 1 {
		t.Errorf("kv_cache_failures_total{op=get,reason=timeout} = %v after reads timed out, want at least 1", got)
	}
	if got := testutil.ToFloat64(rig.metrics.cacheBypassed.WithLabelValues(name, "get")); got < 1 {
		t.Errorf("kv_cache_bypassed_total{op=get} = %v after reads were skipped, want at least 1", got)
	}
	warns := logLines(t, logs.String(), stoppedAnswering)
	if len(warns) != 1 {
		t.Fatalf("%d %q lines for one silent stretch read by one caller, want exactly 1:\n%s",
			len(warns), stoppedAnswering, logs.String())
	}
	if warns[0]["level"] != "warn" || warns[0]["cache"] != name || warns[0]["op"] != "get" {
		t.Errorf("the unavailable line is %v, want level=warn cache=%s op=get", warns[0], name)
	}

	rig.faults.Restore(rig.leader)
	deadline := time.Now().Add(30 * time.Second)
	for {
		var got string
		found, err := rig.cache.Get(context.Background(), rig.keys[0], &got)
		if err == nil && found && got == silentRigValue(rig.keys[0]) {
			break
		}
		if err == nil {
			t.Fatalf("read after the heal = (%v, %q), want the stored value", found, got)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the cache never answered again within 30 s of the heal (last error: %v)", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := testutil.ToFloat64(rig.metrics.cacheUnavailable.WithLabelValues(name)); got != 0 {
		t.Errorf("kv_cache_unavailable = %v after the cache answered again, want 0", got)
	}
	infos := logLines(t, logs.String(), answeringAgain)
	if len(infos) != 1 {
		t.Fatalf("%d %q lines after one recovery, want exactly 1", len(infos), answeringAgain)
	}
	if bypassed, _ := infos[0]["bypassed"].(float64); bypassed < 1 {
		t.Errorf("the recovery line reports bypassed=%v, want the reads skipped meanwhile (at least 1)", infos[0]["bypassed"])
	}
}
