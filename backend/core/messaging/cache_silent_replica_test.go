// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
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
	js      nats.JetStreamContext // a setup connection, for reading the bucket's state
	stream  string                // the bucket's backing stream
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
	return newSilentReplicaRigSeededThrough(t, func(store kvPutter) kvPutter { return store })
}

// kvPutter is the one call the rig seeds its bucket with.
type kvPutter interface {
	Put(key string, value []byte) (uint64, error)
}

// newSilentReplicaRigSeededThrough is newSilentReplicaRig with the bucket handle the keys
// are seeded through passed to wrap first, so a test can put a fault between the rig and
// the bucket and see the rig's setup meet it. Every rig test seeds through the plain
// handle unchanged.
func newSilentReplicaRigSeededThrough(t *testing.T, wrap func(kvPutter) kvPutter) *silentReplicaRig {
	t.Helper()
	servers, faults := dctest.StartJetStreamClusterWithRouteFaults(t, 3)

	setup, _ := replicatedCacheManager(t, servers[0])
	bucket := CacheBucketName("test", "area", kv.BucketDeviceByToken)
	// The first create places a new R3 group, and meets the same settling window every
	// other create on these clusters is retried through; a refused placement is not in
	// that window and still fails here at once. It is the call NewCache makes first, so
	// the bucket is the one a Cache would have opened.
	var store nats.KeyValue
	retryWhileGroupSettles(t, "create the bucket", func() error {
		var err error
		store, err = setup.KeyValueStore(kv.BucketDeviceByToken, bucket, time.Hour)
		return err
	})
	stream := kvStreamPrefix + bucket
	waitForReplicated(t, setup.js, stream, 3)

	rig := &silentReplicaRig{servers: servers, faults: faults, js: setup.js, stream: stream}
	for i := 0; i < silentRigKeys; i++ {
		rig.keys = append(rig.keys, fmt.Sprintf("tenant|device-%02d", i))
	}
	if err := seedSilentRig(wrap(store), rig.keys); err != nil {
		t.Fatal(err)
	}
	// Every replica must hold every key before one of them is silenced: a follower that
	// has not caught up answers a direct get with not-found, which would read as a key
	// that exists coming back absent, and that is the one outcome the test forbids.
	info := waitForReplicated(t, setup.js, stream, 3)
	var err error
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

	// The in-process tier is off: this rig measures how reads reach a silent REPLICA, and
	// with the tier on the warm-up below would leave every key in memory, so no faulted
	// read would ever reach the bucket.
	if rig.cache, err = measuring.NewCache(kv.BucketDeviceByToken, time.Hour, WithoutLocalCache()); err != nil {
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

// seedSilentRig writes each key to the rig's bucket, retrying through the settling
// window, with the bytes and the key encoding a Cache would have stored, so the measuring
// Cache reads them back as its own. A change to how a Cache encodes fails the rig's
// warm-up read loudly rather than passing silently.
//
// 🔴 THE KEYS ARE SEEDED THROUGH A PLAIN HANDLE, NOT THROUGH A CACHE. The group is still
// settling here, and a Cache that meets one settling timeout (or a no-responders answer)
// opens its breaker: every call for the next 5 s then returns ErrCacheUnavailable without
// a round trip. That is a product answer, not a settling transient, so the retry gives up
// on it and the rig fails on the first write that met the window, before the test has
// asserted anything. A plain handle has no breaker, so a put that met the window is
// simply retried. Teaching the retry to wait out ErrCacheUnavailable instead would hide
// a breaker that opens on a healthy cluster; TestSettleRetryDoesNotRetryABypassedCache
// pins that it does not.
func seedSilentRig(store kvPutter, keys []string) error {
	for _, key := range keys {
		data, err := json.Marshal(silentRigValue(key))
		if err != nil {
			return fmt.Errorf("encode %q: %w", key, err)
		}
		if err := settleRetry(func() error {
			_, err := store.Put(kvKey(key), data)
			return err
		}, 30*time.Second, 50*time.Millisecond); err != nil {
			return fmt.Errorf("put %s: %w", key, err)
		}
	}
	return nil
}

// settlingBucket is a bucket whose first put meets the settling window and fails with
// transient; every later put lands.
type settlingBucket struct {
	transient error
	puts      int
	stored    map[string][]byte
}

func (b *settlingBucket) Put(key string, value []byte) (uint64, error) {
	b.puts++
	if b.puts == 1 {
		return 0, b.transient
	}
	b.stored[key] = value
	return uint64(b.puts), nil
}

// TestTheSilentRigSeedsThroughTheSettlingWindow is the rig's setup failure, without the
// cluster: one settling transient on the first seeding write. Seeded through a Cache, that
// transient opened the Cache's breaker, the retry was answered "messaging: cache
// unavailable", and the rig failed before any of its tests had asserted anything (CI saw
// it on the first key, 1.3 s into the test, which is the no-responders fast path). Seeded
// as it is now, the write is retried and every key holds what a Cache would have stored.
func TestTheSilentRigSeedsThroughTheSettlingWindow(t *testing.T) {
	transients := []struct {
		name string
		err  error
	}{
		// What a put meets while the group settles: no responders yet, answered fast,
		// in the legacy handle's and the jetstream handle's spelling; or a timeout.
		{"no stream response", nats.ErrNoStreamResponse},
		{"no stream response (jetstream)", jetstream.ErrNoStreamResponse},
		{"no responders", nats.ErrNoResponders},
		{"request timeout", nats.ErrTimeout},
		{"context deadline", context.DeadlineExceeded},
	}
	for _, tc := range transients {
		t.Run(tc.name, func(t *testing.T) {
			keys := []string{"tenant|device-00", "tenant|device-01", "tenant|device-02"}
			bucket := &settlingBucket{transient: tc.err, stored: map[string][]byte{}}

			if err := seedSilentRig(bucket, keys); err != nil {
				t.Fatalf("seeding through one settling transient = %v, want it retried and nil", err)
			}
			if bucket.puts != len(keys)+1 {
				t.Fatalf("puts = %d, want %d: the first key's write retried once, every other written once",
					bucket.puts, len(keys)+1)
			}
			if len(bucket.stored) != len(keys) {
				t.Fatalf("stored %d keys, want %d", len(bucket.stored), len(keys))
			}
			for _, key := range keys {
				// What the measuring Cache reads back: its own key encoding, and the JSON of
				// the value.
				var got string
				if err := json.Unmarshal(bucket.stored[kvKey(key)], &got); err != nil || got != silentRigValue(key) {
					t.Fatalf("stored under %q = (%q, %v), want %q",
						kvKey(key), bucket.stored[kvKey(key)], err, silentRigValue(key))
				}
			}
		})
	}
}

// TestTheSilentRigSeedingFailsFastOnARealError is the counterweight to the test above:
// seedSilentRig retries only the settling window. A put that fails for any other reason
// fails the rig on that put, naming the cause, rather than being retried until it works
// or skipped and left for the warm-up read to trip over.
func TestTheSilentRigSeedingFailsFastOnARealError(t *testing.T) {
	boom := errors.New("boom")
	bucket := &settlingBucket{transient: boom, stored: map[string][]byte{}}

	err := seedSilentRig(bucket, []string{"tenant|device-00", "tenant|device-01"})

	if !errors.Is(err, boom) {
		t.Fatalf("seeding through a put that failed for a real reason = %v, want an error wrapping %v", err, boom)
	}
	if bucket.puts != 1 {
		t.Fatalf("puts = %d, want 1: a real error is not retried, and no key is written after it", bucket.puts)
	}
	if len(bucket.stored) != 0 {
		t.Fatalf("stored %d keys, want none: the rig stops at the first real failure", len(bucket.stored))
	}
}

// firstPutTimesOut stands between the rig and its bucket and answers the first put with a
// request timeout, as a put into a group that is still settling can be answered, without
// passing it on. Every later put reaches the bucket.
type firstPutTimesOut struct {
	store    kvPutter
	injected int
	puts     int
}

func (f *firstPutTimesOut) Put(key string, value []byte) (uint64, error) {
	f.puts++
	if f.injected == 0 {
		f.injected++
		return 0, nats.ErrTimeout
	}
	return f.store.Put(key, value)
}

// TestTheSilentRigComesUpThroughASettlingTimeout is the rig's setup failure on the real
// cluster: one seeding put meets a settling timeout. Seeded through a Cache, that timeout
// opened the Cache's breaker and the rig failed with "messaging: cache unavailable" before
// any test had asserted anything. The rig must come up, and the fault must have been met:
// a rig that seeds some other way than through the handle it is given never meets it, and
// fails here.
func TestTheSilentRigComesUpThroughASettlingTimeout(t *testing.T) {
	var fault *firstPutTimesOut
	rig := newSilentReplicaRigSeededThrough(t, func(store kvPutter) kvPutter {
		fault = &firstPutTimesOut{store: store}
		return fault
	})

	if fault == nil || fault.injected != 1 {
		t.Fatal("the rig never seeded through the handle it was given, so the settling timeout was never met")
	}
	if fault.puts != silentRigKeys+1 {
		t.Fatalf("seeding puts = %d, want %d: the timed-out put retried once, every other key written once",
			fault.puts, silentRigKeys+1)
	}
	// The rig's warm-up has read every key back through the measuring Cache; one more read
	// shows that Cache's breaker is closed as the tests begin.
	var got string
	found, err := rig.cache.Get(context.Background(), rig.keys[0], &got)
	if err != nil || !found || got != silentRigValue(rig.keys[0]) {
		t.Fatalf("read of %q after setup = (%v, %v, %q), want the stored value", rig.keys[0], found, err, got)
	}
}

// leaderAtFault is the recorded leader, checked again just before a fault is injected.
// The rig chose which server to fault, and which one the client uses, by the leader it
// found during setup; if the leadership has moved since, faulting that server would
// test a healthy leader, and the failure would read as the cache's. So it fails as the
// rig's instead.
func (r *silentReplicaRig) leaderAtFault(t *testing.T) int {
	t.Helper()
	info, err := r.js.StreamInfo(r.stream)
	if err != nil {
		t.Fatalf("rig: reading the bucket's leader before the fault: %v", err)
	}
	if want := r.servers[r.leader].Name(); info.Cluster == nil || info.Cluster.Leader != want {
		got := ""
		if info.Cluster != nil {
			got = info.Cluster.Leader
		}
		t.Fatalf("rig: the bucket's leader moved from %s to %q during setup, so the fault would not reach "+
			"the leader; this is the rig's failure, not the cache's", want, got)
	}
	return r.leader
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

	rig.faults.Silence(rig.leaderAtFault(t))
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

// A cache WRITE must not wait out a JetStream request timeout when the bucket's leader has
// gone silent either, and it matters more than for a read: any replica can answer a read,
// but only the leader can accept a put, so with the leader silent every write goes to it.
// A timed-out write opens the breaker like a timed-out read does.
func TestCacheWriteIsBoundedWhenTheLeaderGoesSilent(t *testing.T) {
	rig := newSilentReplicaRig(t)

	rig.faults.Silence(rig.leaderAtFault(t))
	start := time.Now()
	err := rig.cache.Set(context.Background(), rig.keys[0], silentRigValue(rig.keys[0]))
	took := time.Since(start)

	if held := rig.faults.Held(rig.leader); held == 0 {
		t.Fatal("the silence held back no bytes, so the write never went to the leader and the test proves nothing")
	}
	if err == nil {
		t.Fatal("a write to a silent leader succeeded; the silence did not take effect")
	}
	if took >= 1500*time.Millisecond {
		t.Fatalf("a write to a silent leader took %s (%v); it must give up well before a JetStream request "+
			"timeout (5 s), because each one it waits out holds its caller that long", took, err)
	}
	var got string
	if _, err := rig.cache.Get(context.Background(), rig.keys[0], &got); !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("the read after a timed-out write = %v, want ErrCacheUnavailable: the write's timeout "+
			"must open the breaker", err)
	}
}

// A replica whose server is shut down cleanly does not stall a read either. It is not the
// fault above: a clean shutdown closes the server's connections, so the others drop its
// routes and its share of the reads at once. Kept as a check that the bound does not
// depend on which of the two ways a node is lost.
func TestCacheReadsStayFastWhenTheLeaderIsShutDown(t *testing.T) {
	rig := newSilentReplicaRig(t)

	rig.servers[rig.leaderAtFault(t)].Shutdown()
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

	rig.faults.Silence(rig.leaderAtFault(t))
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

	rig.faults.Silence(rig.leaderAtFault(t))
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
