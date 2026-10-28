// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Leader affinity, by value, on a real three-server cluster with R3 streams. The cluster
// fixture is unix-only, so these run in CI and in a Linux container, not on a Windows
// workstation.
//
// The publish tests measure route frames with the servers' own counters. Varz().InMsgs
// counts every message a server reads from any connection, routes and raft included. The
// writer's manager is attached to server A and the stream is led by B, so C holds neither a
// client nor the leader: C's delta is pure replication (append entries), and A's delta less
// C's is what A reads for the CLIENT's publishes. That difference is ≥ 2,000 per 2,000
// publishes when they enter at A and are forwarded (and the PubAcks forwarded back), and
// close to 0 when the writer publishes straight to B.

// affinityManager is a started manager attached to srv, R3 streams, its own registry, with
// each half of affinity on or off, and its background sampler stopped so the test drives
// every pass. oncreate may build readers.
func affinityManager(t *testing.T, srv *natsserver.Server, area string, consumers, publish bool,
	oncreate func(*NatsManager) error) *NatsManager {
	t.Helper()
	ms := testMicroservice(t, srv, area)
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	ms.InstanceConfiguration.Infrastructure.Nats.Affinity = config.NatsAffinityConfiguration{
		ConsumerLeaders: config.BoolFlag(consumers), PublishToLeader: config.BoolFlag(publish)}
	if oncreate == nil {
		oncreate = func(*NatsManager) error { return nil }
	}
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), oncreate)
	nmgr.RecordMaxDeliveries(recordNothing)
	ctx := context.Background()
	if err := nmgr.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := nmgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = nmgr.Stop(stopCtx)
	})
	stopBackgroundSampler(t, nmgr)
	return nmgr
}

// jsAPI sends a JetStream API request and fails the test on a broker error.
func jsAPI(t *testing.T, nc *nats.Conn, subject string, body any) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := nc.Request(subject, data, 5*time.Second)
	if err != nil {
		t.Fatalf("%s: %v", subject, err)
	}
	var resp struct {
		Error *struct {
			Description string `json:"description"`
		} `json:"error"`
	}
	_ = json.Unmarshal(msg.Data, &resp)
	if resp.Error != nil && !strings.Contains(resp.Error.Description, "already leader") {
		t.Fatalf("%s: %s", subject, resp.Error.Description)
	}
}

func preferred(server string) map[string]any {
	return map[string]any{"placement": map[string]string{"preferred": server}}
}

// moveStreamLeader elects server as stream's leader and waits until every peer is current.
func moveStreamLeader(t *testing.T, nmgr *NatsManager, stream, server string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := nmgr.js.StreamInfo(stream)
		if err == nil && leaderOfStream(info) == server && currentPeers(info) == info.Config.Replicas {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream %s never settled with %s leading (last: %v %v)", stream, server, leaderOfStream(info), err)
		}
		if err == nil && leaderOfStream(info) != server && leaderOfStream(info) != "" {
			jsAPI(t, nmgr.nc, "$JS.API.STREAM.LEADER.STEPDOWN."+stream, preferred(server))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitWriterTarget waits up to 15 s for the writer's stream target to name server. A timeout
// says WHERE the target and the stream's leader were, which tells a missed advisory (target
// stale, leader moved) from a dial that kept failing (target on the manager's own connection)
// from a leader that never settled. Callers move the leader with moveStreamLeader first, so
// the broker names it by then, and the loop's retries (affinityRetargetRetryMax apart at
// most) put the target on it well inside the wait; the bound itself is asserted by the
// TestWriterFollowsAnElection* tests.
func waitWriterTarget(t *testing.T, nmgr *NatsManager, ow *orderedWriter, stream, server string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for ow.target.Load().server != server {
		if time.Now().After(deadline) {
			leader, peers := "?", -1
			if info, err := nmgr.js.StreamInfo(stream); err == nil {
				leader, peers = leaderOfStream(info), currentPeers(info)
			}
			t.Fatalf("timed out waiting for the writer's target to be %s: target is %q (nil conn: the manager's own), "+
				"stream leader is %q with %d current peers", server, ow.target.Load().server,
				leader, peers)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// moveConsumerLeader elects server as the durable's leader and waits until every peer is current.
func moveConsumerLeader(t *testing.T, nmgr *NatsManager, d durableRef, server string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		ci, err := nmgr.js.ConsumerInfo(d.stream, d.durable)
		if err == nil && consumerLeader(ci) == server && consumerSettled(ci) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer %s never settled with %s leading (last: %v %v)", d.durable, server, consumerLeader(ci), err)
		}
		if err == nil && consumerLeader(ci) != server && consumerLeader(ci) != "" {
			jsAPI(t, nmgr.nc, fmt.Sprintf("$JS.API.CONSUMER.LEADER.STEPDOWN.%s.%s", d.stream, d.durable), preferred(server))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func consumerSettled(ci *nats.ConsumerInfo) bool {
	if consumerLeader(ci) == "" {
		return false
	}
	for _, p := range ci.Cluster.Replicas {
		if !p.Current || p.Offline {
			return false
		}
	}
	return true
}

// waitConsumerSettled waits until the durable is led and every peer is current, whoever leads.
func waitConsumerSettled(t *testing.T, nmgr *NatsManager, d durableRef) *nats.ConsumerInfo {
	t.Helper()
	var ci *nats.ConsumerInfo
	waitWithin(t, 30*time.Second, "the consumer to settle", func() bool {
		var err error
		ci, err = nmgr.js.ConsumerInfo(d.stream, d.durable)
		return err == nil && consumerSettled(ci)
	})
	return ci
}

func inMsgs(t *testing.T, srv *natsserver.Server) int64 {
	t.Helper()
	v, err := srv.Varz(nil)
	if err != nil {
		t.Fatal(err)
	}
	return v.InMsgs
}

// publishAcked publishes n messages through w and fails the test unless every one is acknowledged.
func publishAcked(t *testing.T, w OrderedWriter, prefix string, n int) {
	t.Helper()
	out := newOutcomes()
	for i := 0; i < n; i++ {
		w.Publish(orderedCtx(), Message{Value: []byte("e"), DedupID: fmt.Sprintf("%s:%d", prefix, i)}, out.done(i))
	}
	if got := out.waitCount(n, 60*time.Second); got != n {
		t.Fatalf("%d of %d publishes were reported", got, n)
	}
	for i := 0; i < n; i++ {
		if out.errs[i] != nil {
			t.Fatalf("publish %d failed: %v", i, out.errs[i])
		}
	}
}

// clientFramesAtA publishes n messages and returns A's in-message delta less C's (A's reads
// for the client's publishes; see the file comment) and B's delta.
func clientFramesAtA(t *testing.T, servers []*natsserver.Server, w OrderedWriter, prefix string, n int) (atA, atB int64) {
	t.Helper()
	a0, b0, c0 := inMsgs(t, servers[0]), inMsgs(t, servers[1]), inMsgs(t, servers[2])
	publishAcked(t, w, prefix, n)
	a1, b1, c1 := inMsgs(t, servers[0]), inMsgs(t, servers[1]), inMsgs(t, servers[2])
	return (a1 - a0) - (c1 - c0), b1 - b0
}

// publishLocal reads the gauge for the resolution writer newOrdered builds.
func publishLocal(nmgr *NatsManager, suffix string) float64 {
	return testutil.ToFloat64(nmgr.metrics.publishLeaderLocal.WithLabelValues(suffix, string(WriterResolution)))
}

// Publishes go straight to the stream leader, not through a route.
//
// The control arm comes first and is the same measurement with the writer half switched off:
// it must see ≥ 2,000 client frames at A, or the measurement cannot see a hop at all and the
// second arm's small number would prove nothing. The control arm also reads the gauge at 0
// (the writer publishes through A while B leads), which is the gauge's negative control.
//
// GUARD: on main, and with the writer half off, every publish enters at A and is forwarded:
// A's client frames are ≥ 2,000 (about 4,000: the publish in, the PubAck back over the route)
// and the second arm fails by that number.
func TestOrderedWriterPublishesWithoutARouteHop(t *testing.T) {
	const n = 2000
	servers := dctest.StartJetStreamCluster(t, 3)
	leader := servers[1].Name()
	stream := StreamName("test", streams.ResolvedEvents)

	// Control: affinity off.
	off := affinityManager(t, servers[0], uniqueArea("affinity-off"), false, false, nil)
	wOff := newOrdered(t, off, 512)
	waitUntilPublishable(t, off)
	moveStreamLeader(t, off, stream, leader)
	publishAcked(t, wOff, "warm-off", 10)
	atA, atB := clientFramesAtA(t, servers, wOff, "off", n)
	t.Logf("affinity off: client frames at A %d, in-messages at B %d", atA, atB)
	if atA < n {
		t.Fatalf("control: only %d client frames at A for %d publishes forwarded to B; the measurement cannot see a route hop", atA, n)
	}
	off.sampleNow(context.Background())
	if got := publishLocal(off, streams.ResolvedEvents); got != 0 {
		t.Fatalf("gauge negative control: jetstream_publish_leader_local reads %v for a writer attached to A while B leads", got)
	}
	wOff.Close()
	_ = off.Stop(context.Background())

	// Affinity on.
	on := affinityManager(t, servers[0], uniqueArea("affinity-on"), false, true, nil)
	wOn := newOrdered(t, on, 512)
	moveStreamLeader(t, on, stream, leader)
	ow := wOn.(*orderedWriter)
	waitWriterTarget(t, on, ow, stream, leader)
	publishAcked(t, wOn, "warm-on", 10) // the first submission moves the writer
	atA, atB = clientFramesAtA(t, servers, wOn, "on", n)
	t.Logf("affinity on: client frames at A %d, in-messages at B %d", atA, atB)
	if atB < n {
		t.Fatalf("server B (the leader) read %d messages for %d publishes; they did not reach it", atB, n)
	}
	if atA >= 100 {
		t.Fatalf("server A still read %d client frames for %d publishes: they entered at A and crossed a route to "+
			"the leader, want under 100", atA, n)
	}
	on.sampleNow(context.Background())
	if got := publishLocal(on, streams.ResolvedEvents); got != 1 {
		t.Fatalf("jetstream_publish_leader_local reads %v for a writer publishing to the leader", got)
	}
}

// moves sums jetstream_consumer_leader_moves_total over results.
func moves(nmgr *NatsManager, d durableRef) float64 {
	return testutil.ToFloat64(nmgr.metrics.leaderMoves.WithLabelValues(d.stream, d.durable, leaderMoveMoved)) +
		testutil.ToFloat64(nmgr.metrics.leaderMoves.WithLabelValues(d.stream, d.durable, leaderMoveFailed))
}

func consumerLocal(nmgr *NatsManager, d durableRef) float64 {
	return testutil.ToFloat64(nmgr.metrics.consumerLeaderLocal.WithLabelValues(d.stream, d.durable))
}

// readerOnResolved builds a resolved-events reader in oncreate, so the census sees it.
func readerOnResolved(n *NatsManager) error {
	_, err := n.NewReader(streams.ResolvedEvents)
	return err
}

// A reader's consumer leader is moved to the reader's server, and only after the hysteresis.
//
// The gauge is checked FIRST against a real miss: with the leader stepped to C it must read 0
// after one sample, while the reconciler is still held by the hysteresis (the leader is still
// C after two samples). Only then is the reconciler's move trusted.
//
// GUARD: on main nothing moves the leader, which stays on C (the server name is in the
// failure). A reconciler with no hysteresis moves it on the first sample. An inverted gauge
// reads 1 against the miss.
func TestConsumerLeaderFollowsItsReader(t *testing.T) {
	servers := dctest.StartJetStreamCluster(t, 3)
	a, c := servers[0].Name(), servers[2].Name()
	nmgr := affinityManager(t, servers[0], uniqueArea("affinity-reader"), true, false, readerOnResolved)
	d := nmgr.trackedDurables()[0]
	ctx := context.Background()

	moveConsumerLeader(t, nmgr, d, a)
	nmgr.sampleNow(ctx)
	if got := consumerLocal(nmgr, d); got != 1 {
		t.Fatalf("jetstream_consumer_leader_local reads %v with the leader on the reader's server", got)
	}

	moveConsumerLeader(t, nmgr, d, c)
	nmgr.sampleNow(ctx)
	if got := consumerLocal(nmgr, d); got != 0 {
		t.Fatalf("gauge negative control: jetstream_consumer_leader_local reads %v with the leader on %s and the reader on %s", got, c, a)
	}
	nmgr.sampleNow(ctx)
	if ci := waitConsumerSettled(t, nmgr, d); consumerLeader(ci) != c {
		t.Fatalf("after two samples the leader is on %s; the reconciler must wait for %d samples, so it should still be on %s",
			consumerLeader(ci), affinityHysteresis, c)
	}

	nmgr.sampleNow(ctx)
	waitWithin(t, 15*time.Second, "the consumer leader to reach the reader's server", func() bool {
		ci, err := nmgr.js.ConsumerInfo(d.stream, d.durable)
		return err == nil && consumerLeader(ci) == a
	})
	waitConsumerSettled(t, nmgr, d)
	nmgr.sampleNow(ctx)
	if got := consumerLocal(nmgr, d); got != 1 {
		t.Fatalf("jetstream_consumer_leader_local reads %v after the move", got)
	}
	if got := testutil.ToFloat64(nmgr.metrics.leaderMoves.WithLabelValues(d.stream, d.durable, leaderMoveMoved)); got != 1 {
		t.Fatalf("moves counted: %v, want 1", got)
	}
}

// With the consumer half off, which is the default, the gauge still reads the placement and
// nothing is moved, however many samples see the leader away.
//
// GUARD: a reconciler that ignores the setting steps the leader down on every instance that
// never asked for it (the shipped default), and the gauge would be the only witness.
func TestConsumerLeaderStaysWhenTheSettingIsOff(t *testing.T) {
	servers := dctest.StartJetStreamCluster(t, 3)
	c := servers[2].Name()
	nmgr := affinityManager(t, servers[0], uniqueArea("affinity-off-reader"), false, false, readerOnResolved)
	d := nmgr.trackedDurables()[0]
	ctx := context.Background()
	moveConsumerLeader(t, nmgr, d, c)
	for i := 0; i < affinityHysteresis+1; i++ {
		nmgr.sampleNow(ctx)
	}
	time.Sleep(time.Second)
	if got := consumerLocal(nmgr, d); got != 0 {
		t.Fatalf("jetstream_consumer_leader_local reads %v with the leader away and the setting off; the watcher stays on", got)
	}
	if ci := waitConsumerSettled(t, nmgr, d); consumerLeader(ci) != c {
		t.Fatalf("the leader moved with the reconciler off: it is on %s, was put on %s", consumerLeader(ci), c)
	}
	if got := moves(nmgr, d); got != 0 {
		t.Fatalf("%v step-downs asked for with the reconciler off", got)
	}
}

// With three replicas reading one durable, two on A and one on B, exactly ONE replica asks
// for the move, and the leader goes to A (the majority).
//
// GUARD: a reconciler that acts on every replica sends three step-downs for one placement
// (the total across replicas reads 3, not 1).
func TestOneReplicaMovesTheLeaderToTheMajority(t *testing.T) {
	servers := dctest.StartJetStreamCluster(t, 3)
	a, c := servers[0].Name(), servers[2].Name()
	area := uniqueArea("affinity-majority")
	replicas := []*NatsManager{
		affinityManager(t, servers[0], area, true, false, readerOnResolved),
		affinityManager(t, servers[0], area, true, false, readerOnResolved),
		affinityManager(t, servers[1], area, true, false, readerOnResolved),
	}
	d := replicas[0].trackedDurables()[0]
	ctx := context.Background()
	moveConsumerLeader(t, replicas[0], d, c)

	// The replicas sample at the same moment, as three pods' samplers can: driven one after
	// another, the first move would land before the others looked, and a reconciler acting on
	// every replica would go unseen.
	for i := 0; i < affinityHysteresis; i++ {
		var wg sync.WaitGroup
		for _, r := range replicas {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.sampleNow(ctx)
			}()
		}
		wg.Wait()
	}
	waitWithin(t, 15*time.Second, "the consumer leader to reach the majority's server", func() bool {
		ci, err := replicas[0].js.ConsumerInfo(d.stream, d.durable)
		return err == nil && consumerLeader(ci) == a
	})
	total := 0.0
	for _, r := range replicas {
		total += moves(r, d)
	}
	if total != 1 {
		t.Fatalf("%v step-downs were asked for across the three replicas; one replica must act", total)
	}
}

// The replica with the lowest id reads from the server that already leads the consumer, so it
// never samples the leader as remote and never runs a census. The two readers on the
// majority's server must still move the leader to themselves.
//
// GUARD: an actor elected among ALL readers is that minority replica, which has nothing to do,
// and the two majority replicas each defer to it: the leader stays off the majority's server
// for good, with no move asked and nothing logged.
func TestTheMajorityMovesTheLeaderWhenTheLowestIdIsBesideIt(t *testing.T) {
	servers := dctest.StartJetStreamCluster(t, 3)
	a, b := servers[0].Name(), servers[1].Name()
	area := uniqueArea("affinity-lowest")
	withID := func(id string) func(*NatsManager) error {
		return func(n *NatsManager) error {
			n.aff().id = id
			return readerOnResolved(n)
		}
	}
	replicas := []*NatsManager{
		affinityManager(t, servers[0], area, true, false, withID("z1")),
		affinityManager(t, servers[0], area, true, false, withID("z2")),
		affinityManager(t, servers[1], area, true, false, withID("a0")),
	}
	d := replicas[0].trackedDurables()[0]
	ctx := context.Background()
	moveConsumerLeader(t, replicas[0], d, b)
	for i := 0; i < affinityHysteresis; i++ {
		for _, r := range replicas {
			r.sampleNow(ctx)
		}
	}
	waitWithin(t, 15*time.Second, "the consumer leader to reach the majority's server", func() bool {
		ci, err := replicas[0].js.ConsumerInfo(d.stream, d.durable)
		return err == nil && consumerLeader(ci) == a
	})
	if got := moves(replicas[2], d); got != 0 {
		t.Fatalf("the replica beside the leader asked for %v step-downs; it never sees the leader as remote", got)
	}
}

// Two replicas on two servers have no majority: the leader is left where it is, and nothing
// is asked of the broker.
//
// GUARD: a reconciler that moves without a strict majority sends the leader to one reader's
// server (and on the next pass the other would move it back).
func TestSplitReadersLeaveTheLeaderAlone(t *testing.T) {
	servers := dctest.StartJetStreamCluster(t, 3)
	c := servers[2].Name()
	area := uniqueArea("affinity-split")
	replicas := []*NatsManager{
		affinityManager(t, servers[0], area, true, false, readerOnResolved),
		affinityManager(t, servers[1], area, true, false, readerOnResolved),
	}
	d := replicas[0].trackedDurables()[0]
	ctx := context.Background()
	moveConsumerLeader(t, replicas[0], d, c)
	for i := 0; i < affinityHysteresis+1; i++ {
		for _, r := range replicas {
			r.sampleNow(ctx)
		}
	}
	time.Sleep(time.Second)
	if ci := waitConsumerSettled(t, replicas[0], d); consumerLeader(ci) != c {
		t.Fatalf("the leader moved to %s with the readers split across two servers", consumerLeader(ci))
	}
	for _, r := range replicas {
		if got := moves(r, d); got != 0 {
			t.Fatalf("a replica asked for %v step-downs with no majority", got)
		}
	}
}

// A leader connection that closes sends its writers back to the manager's own connection at
// once, so publishing carries on (through a route) until the leader is dialled again.
//
// GUARD: a writer left on the closed connection fails every publish with "connection closed"
// until the next sample notices, 30 s later.
func TestWriterFallsBackWhenItsLeaderConnectionCloses(t *testing.T) {
	const n = 200
	servers := dctest.StartJetStreamCluster(t, 3)
	leader := servers[1].Name()
	stream := StreamName("test", streams.ResolvedEvents)
	nmgr := affinityManager(t, servers[0], uniqueArea("affinity-fallback"), false, true, nil)
	w := newOrdered(t, nmgr, 64)
	ow := w.(*orderedWriter)
	// The stream accepts a publish before it is moved, as the mid-window test does.
	waitUntilPublishable(t, nmgr)
	moveStreamLeader(t, nmgr, stream, leader)
	waitWriterTarget(t, nmgr, ow, stream, leader)
	publishAcked(t, w, "before", 10)
	lc := ow.conn.Load()
	if lc == nil {
		t.Fatal("the writer is not publishing on a leader connection")
	}

	lc.Close()
	out := newOutcomes()
	for i := 0; i < n; i++ {
		w.Publish(orderedCtx(), Message{Value: []byte("e"), DedupID: "after:" + strconv.Itoa(i)}, out.done(i))
	}
	if got := out.waitCount(n, 30*time.Second); got != n {
		t.Fatalf("%d of %d publishes were reported", got, n)
	}
	failed := 0
	for i := 0; i < n; i++ {
		if out.errs[i] != nil {
			failed++
		}
	}
	if failed != 0 {
		t.Fatalf("%d of %d publishes failed after the leader connection closed; the writer did not fall back", failed, n)
	}
}

// retryBudget is when the loop's (k+1)th pass over a stream comes, after k passes that did not
// settle its target: the first k delays of the product's own retry schedule.
func retryBudget(k int) time.Duration {
	return retryBudgetFrom(affinityRetargetRetry, k)
}

// retryBudgetFrom is retryBudget for a retry whose next wait is delay: one that earlier
// passes have already spent part of the backoff of.
func retryBudgetFrom(delay time.Duration, k int) time.Duration {
	start := time.Unix(1_000_000, 0)
	r, now := newRetargetRetry("", start), start
	r.delay = delay
	for i := 0; i < k && r.next(now); i++ {
		now = r.due
	}
	return now.Sub(start)
}

// dialRecorder is the affinity loop's dial address seam (leaderURLs) for one server: it
// records when each dial of that server BEGINS, refuses the first few, and reads the retry's
// delay as the first refusal is made. Dials of any other server are passed through.
type dialRecorder struct {
	server, stream string
	urls           map[string]string

	mu      sync.Mutex
	refuse  int
	began   []time.Time
	since   time.Time     // when the first refused dial began
	delay   time.Duration // the stream's retry delay then: the wait the next pass would make
	noRetry bool          // a refused dial began with no retry pending
}

func newDialRecorder(servers []*natsserver.Server, server, stream string) *dialRecorder {
	r := &dialRecorder{server: server, stream: stream, urls: map[string]string{}}
	for _, s := range servers {
		r.urls[s.Name()] = s.ClientURL()
	}
	return r
}

// install is an affinityManager oncreate.
func (r *dialRecorder) install(n *NatsManager) error {
	n.aff().leaderURLs = func(server string) []string {
		if server != r.server {
			return []string{r.urls[server]}
		}
		// The retry is read before r.mu is taken, so the two locks are never nested.
		var d time.Duration
		var have bool
		n.aff().mu.Lock()
		if rt := n.aff().retries[r.stream]; rt != nil {
			d, have = rt.delay, true
		}
		n.aff().mu.Unlock()
		r.mu.Lock()
		defer r.mu.Unlock()
		r.began = append(r.began, time.Now())
		if r.refuse > 0 {
			if r.since.IsZero() {
				r.since, r.delay, r.noRetry = time.Now(), d, !have
			}
			r.refuse--
			// Nothing listens on port 1: the dial is refused at once.
			return []string{"nats://127.0.0.1:1"}
		}
		return []string{r.urls[server]}
	}
	return nil
}

// arm starts counting from now and refuses the next n dials of the server.
func (r *dialRecorder) arm(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refuse, r.began, r.since, r.delay, r.noRetry = n, nil, time.Time{}, 0, false
}

func (r *dialRecorder) dials() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.began)
}

// dialBound is the longest a dial that is going to succeed may take. nats.go bounds each of the
// three steps of a connect (the TCP connect, the server's INFO, the PONG answering the
// CONNECT) by its Timeout, 2 s by default (see the note in affinity.dial): a starved runner
// that needs most of each is not a defect in the retry, and the dial is not the schedule.
// 4.4 s was measured for one in a run that shared its CPUs with three more.
const dialBound = 3 * nats.DefaultTimeout

// followedOnSchedule waits for the writer's stream target to name server, and checks the two
// things that make that late, apart:
//
//   - THE SCHEDULE: the dialNo'th dial of server began within limit of since, less the time the
//     loop spent waiting on its lookups. Both the waits between passes and the passes that did
//     not settle are the retry's: this is what the product promises. Only the loop's wait
//     for the broker to answer a lookup is taken out; it is the broker's.
//   - THE DIAL: the target names server within dialBound of that dial beginning. A dial that
//     takes long is a connect on a starved runner, which the retry schedule neither causes nor
//     controls.
//
// 🔴 THESE USED TO BE ONE WALL-CLOCK BOUND FROM since TO THE TARGET, and a dial that took
// 4.4 s on a starved runner (a successful connect: the third dial began on schedule, 0.75 s
// after the first refusal) failed it by 2.4 s with the schedule intact.
func followedOnSchedule(t *testing.T, nmgr *NatsManager, ow *orderedWriter, stream, server string,
	since time.Time, dialNo int, rec *dialRecorder, faults *faultedLookup, limit time.Duration) (began, followed time.Duration) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		leader := "?"
		if info, err := nmgr.js.StreamInfo(stream); err == nil {
			leader = leaderOfStream(info)
		}
		t.Fatalf("%s (the writer's target is %q; the stream's leader reads %q; lookups, as [start after "+
			"since+duration leader]:%s; dials began at %v)", fmt.Sprintf(format, args...), ow.target.Load().server,
			leader, faults.timeline(since), offsets(rec.dials(), since))
	}
	for len(rec.dials()) < dialNo {
		if looked := faults.lookingSince(since); time.Since(since)-looked > 2*limit {
			fail("dial %d of the new leader never began, %v after since", dialNo, time.Since(since))
		}
		time.Sleep(5 * time.Millisecond)
	}
	dialAt := rec.dials()[dialNo-1]
	began = dialAt.Sub(since)
	if looked := faults.lookingSince(since); began-looked > limit {
		fail("dial %d began %v after since, of which %v was the loop waiting on its lookups; the bound on the rest is %v",
			dialNo, began, looked, limit)
	}
	for ow.target.Load().server != server {
		if time.Since(dialAt) > dialBound {
			fail("the target was not on %s %v after dial %d began; the bound is %v", server, time.Since(dialAt), dialNo, dialBound)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return began, time.Since(since)
}

// offsets renders when each time was, relative to since.
func offsets(at []time.Time, since time.Time) []time.Duration {
	out := make([]time.Duration, len(at))
	for i, a := range at {
		out[i] = a.Sub(since).Round(time.Millisecond)
	}
	return out
}

// faultedLookup is the affinity loop's stream info lookup with the answers right after an
// election replaced: once armed, the next lookups are answered from faults, in order, and then
// by the broker again.
type faultedLookup struct {
	mu     sync.Mutex
	faults []func(*nats.StreamInfo) (*nats.StreamInfo, error)
	first  time.Time // when the first fault was served
	served int
	spans  []lookupSpan // every lookup the loop made, answered or not
}

// lookupSpan is one lookup, from the loop's request to its answer.
type lookupSpan struct {
	start, end time.Time
	leader     string // what the loop was told ("" for none or an error)
}

// install is an affinityManager oncreate: it runs before the loop starts, which is the only
// reader of the seam.
func (f *faultedLookup) install(n *NatsManager) error {
	n.aff().lookup = func(ctx context.Context, stream string) (*nats.StreamInfo, error) {
		begun := time.Now()
		info, err := n.js.StreamInfo(stream, nats.Context(ctx))
		f.mu.Lock()
		defer f.mu.Unlock()
		span := lookupSpan{start: begun, end: time.Now()}
		if err != nil || len(f.faults) == 0 {
			if err == nil {
				span.leader = leaderOfStream(info)
			}
			f.spans = append(f.spans, span)
			return info, err
		}
		fault := f.faults[0]
		f.faults = f.faults[1:]
		if f.served == 0 {
			f.first = time.Now()
		}
		f.served++
		info, err = fault(info)
		if err == nil {
			span.leader = leaderOfStream(info)
		}
		f.spans = append(f.spans, span)
		return info, err
	}
	return nil
}

func (f *faultedLookup) arm(faults ...func(*nats.StreamInfo) (*nats.StreamInfo, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults, f.served, f.first = faults, 0, time.Time{}
}

// spent reports how many faults were served, and when the first was.
func (f *faultedLookup) spent() (int, time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.served, f.first
}

// lookingSince is how long the loop spent waiting for the broker's answer to its lookups
// from since on: time the retry schedule does not control.
func (f *faultedLookup) lookingSince(since time.Time) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total time.Duration
	for _, sp := range f.spans {
		if sp.end.After(since) {
			from := sp.start
			if from.Before(since) {
				from = since
			}
			total += sp.end.Sub(from)
		}
	}
	return total
}

// timeline renders the lookups from since on, for a failure to say where the time went.
func (f *faultedLookup) timeline(since time.Time) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, sp := range f.spans {
		if sp.end.After(since) {
			fmt.Fprintf(&b, " [%v+%v leader %q]", sp.start.Sub(since).Round(time.Millisecond),
				sp.end.Sub(sp.start).Round(time.Millisecond), sp.leader)
		}
	}
	return b.String()
}

// unanswered is a lookup no server answered in time.
func unanswered(*nats.StreamInfo) (*nats.StreamInfo, error) { return nil, context.DeadlineExceeded }

// answeredBy is a lookup answered from a view of the group that names leader ("" for none).
func answeredBy(leader string) func(*nats.StreamInfo) (*nats.StreamInfo, error) {
	return func(info *nats.StreamInfo) (*nats.StreamInfo, error) {
		out := *info
		cl := *info.Cluster
		cl.Leader = leader
		out.Cluster = &cl
		return &out, nil
	}
}

// A writer follows an election whose first lookups do not name the new leader: one goes
// unanswered, one reads the group with no leader, one reads the old leader. The loop asks
// again, and the target is on the new leader within the retry schedule's first three delays
// and one more of its cap.
//
// The faults are counted as SERVED before the bound is read: a run whose lookups all came
// after the faults were withdrawn would follow at once and prove nothing.
//
// GUARD: on main a pass that cannot name the leader keeps the old target and nothing asks
// again until the sampler's next pass (stopped here), so the target stays on the old leader.
func TestWriterFollowsAnElectionWhoseFirstLookupsFail(t *testing.T) {
	servers := dctest.StartJetStreamCluster(t, 3)
	first, second := servers[1].Name(), servers[2].Name()
	stream := StreamName("test", streams.ResolvedEvents)
	faults := &faultedLookup{}
	rec := newDialRecorder(servers, second, stream)
	nmgr := affinityManager(t, servers[0], uniqueArea("affinity-relookup"), false, true, func(n *NatsManager) error {
		if err := faults.install(n); err != nil {
			return err
		}
		return rec.install(n)
	})
	ow := newOrdered(t, nmgr, 64).(*orderedWriter)
	waitUntilPublishable(t, nmgr)
	moveStreamLeader(t, nmgr, stream, first)
	waitWriterTarget(t, nmgr, ow, stream, first)

	rec.arm(0)
	faults.arm(unanswered, answeredBy(""), answeredBy(first))
	moveStreamLeader(t, nmgr, stream, second)
	waitWithin(t, 30*time.Second, "the faulted lookups to be served", func() bool {
		n, _ := faults.spent()
		return n == 3
	})
	_, since := faults.spent()
	limit := retryBudget(3) + affinityRetargetRetryMax
	began, took := followedOnSchedule(t, nmgr, ow, stream, second, since, 1, rec, faults, limit)
	t.Logf("dialled %v after the first of 3 failed lookups (bound %v), followed after %v", began, limit, took)
}

// A writer follows an election whose new leader cannot be dialled at first: the loop dials
// again, the third dial begins within the retry schedule's next two delays and one more of its
// cap, and the target is on the new leader once that dial has connected.
//
// 🔴 THE OLD BOUND WAS ONE WALL-CLOCK FIGURE (2.75 s from the first refusal to the target) AND IT
// FAILED IN CI AT 5.18 s FOR TWO REASONS THAT HAVE NOTHING TO DO WITH WHAT IT GUARDS. Both
// were reproduced here, and the check now separates them from the schedule:
//
//  1. THE DIAL TOOK LONG. The third dial began on schedule (0.75 s after the first refusal) and
//     the connect itself took 4.4 s on a starved runner (5.195 s end to end, three other
//     containers sharing the CPUs). The schedule decides when a dial begins, never how long a
//     connect takes: that is bounded separately (dialBound).
//  2. THE BACKOFF WAS ALREADY SPENT. A dial is made only once a lookup names the new leader, and
//     right after an election the lookups before that one (a leaderless group, an answer from a
//     member that still names the old leader) are unsettled passes of the same retry: each has
//     already waited and doubled the delay. The bound assumed the retry was fresh at the first
//     refusal, which holds only when the first lookup after the advisory already names the new
//     leader (4.01 s, deterministic, in the "after stale lookups" arm). It is now read from the
//     retry's own delay at the first refusal.
//
// GUARD: on main a failed dial sends the writers to the manager's own connection and nothing
// dials again until the sampler's next pass (stopped here), so the third dial never begins.
func TestWriterFollowsAnElectionWhoseLeaderCannotBeDialledAtFirst(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stale int
	}{
		{"prompt", 0},
		{"after stale lookups", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			servers := dctest.StartJetStreamCluster(t, 3)
			first, second := servers[1].Name(), servers[2].Name()
			stream := StreamName("test", streams.ResolvedEvents)
			faults := &faultedLookup{}
			rec := newDialRecorder(servers, second, stream)
			nmgr := affinityManager(t, servers[0], uniqueArea("affinity-redial"), false, true, func(n *NatsManager) error {
				if err := faults.install(n); err != nil {
					return err
				}
				return rec.install(n)
			})
			ow := newOrdered(t, nmgr, 64).(*orderedWriter)
			waitUntilPublishable(t, nmgr)
			moveStreamLeader(t, nmgr, stream, first)
			waitWriterTarget(t, nmgr, ow, stream, first)

			rec.arm(2)
			stale := make([]func(*nats.StreamInfo) (*nats.StreamInfo, error), tc.stale)
			for i := range stale {
				stale[i] = answeredBy(first)
			}
			faults.arm(stale...)
			moveStreamLeader(t, nmgr, stream, second)
			waitWithin(t, 30*time.Second, "the refused dials to be made", func() bool {
				rec.mu.Lock()
				defer rec.mu.Unlock()
				return rec.refuse == 0
			})
			rec.mu.Lock()
			since, d0, noRetry := rec.since, rec.delay, rec.noRetry
			rec.mu.Unlock()
			if noRetry {
				t.Fatal("the first dial was refused with no retry pending: the loop dialled for no trigger")
			}
			if served, _ := faults.spent(); served != tc.stale {
				t.Fatalf("%d stale lookups were served, want %d: the arm did not measure what it names", served, tc.stale)
			}
			limit := retryBudgetFrom(d0, 2) + affinityRetargetRetryMax
			began, took := followedOnSchedule(t, nmgr, ow, stream, second, since, 3, rec, faults, limit)
			t.Logf("third dial began %v after the first of 2 refused dials (retry delay then %v, bound %v), "+
				"followed after %v", began, d0, limit, took)
		})
	}
}

// A stream leader elected while a window is in flight: every acknowledged publish is in the
// stream exactly once, in submission order, and the writer ends up on the new leader.
//
// HOW LONG "ends up" MAY TAKE is the writer's own schedule, not a wall-clock constant. An
// election strands the publishes in flight; they settle only at their deadline (publishWait),
// which leaves the writer in a failure episode, and an episode closes only when a publish
// succeeds. follow changes connection only outside an episode and with an empty window, so a
// probe submitted during the episode blocks for a free slot, publishes on the OLD connection,
// and the swap happens on the NEXT submission. A swap attempt whose window does not settle in
// affinitySwapSettle is held off for affinitySwapRetry. The worst case after the last report is
// therefore the episode closing, one failed attempt with its hold-off, and one that succeeds.
// The test used to allow 5 s from the last report and checked the clock after a probe that had
// itself blocked on the episode, so a loaded runner reached the deadline with the target already
// on the new leader and no episode open, one probe short of the swap. What is asserted is
// unchanged: the writer reaches the new leader, and every acknowledged publish is in the stream
// once and in order. Only the bound comes from the design's constants.
func TestOrderedWriterSurvivesLeaderMoveMidWindow(t *testing.T) {
	const n = 4000
	servers := dctest.StartJetStreamCluster(t, 3)
	first, second := servers[1].Name(), servers[2].Name()
	stream := StreamName("test", streams.ResolvedEvents)
	nmgr := affinityManager(t, servers[0], uniqueArea("affinity-move"), false, true, nil)
	w := newOrdered(t, nmgr, 512)
	ow := w.(*orderedWriter)
	waitUntilPublishable(t, nmgr)
	moveStreamLeader(t, nmgr, stream, first)
	waitWriterTarget(t, nmgr, ow, stream, first)

	out := newOutcomes()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			w.Publish(orderedCtx(), Message{Value: []byte("e"), DedupID: "mid:" + strconv.Itoa(i)}, out.done(i))
		}
	}()
	waitWithin(t, 30*time.Second, "a window in flight", func() bool { return out.count() >= n/4 })
	moveStreamLeader(t, nmgr, stream, second)
	moved := time.Now()
	wg.Wait()
	if got := out.waitCount(n, 60*time.Second); got != n {
		t.Fatalf("%d of %d publishes were reported", got, n)
	}
	onSecond := func() bool {
		w.Publish(orderedCtx(), Message{Value: []byte("probe")}, func(error) {})
		lc := ow.conn.Load()
		return lc != nil && lc.ConnectedServerName() == second
	}
	followBound := 2*affinitySwapSettle + affinitySwapRetry + 5*time.Second
	for deadline := time.Now().Add(followBound); !onSecond(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for the writer to publish on the new leader %s: its stream target is %q, "+
				"it is in a failure episode: %v", followBound, second, ow.target.Load().server, ow.inEpisode.Load())
		}
	}
	t.Logf("writer on the new leader %s after the election", time.Since(moved))

	// Every acknowledged publish once, in submission order.
	info, err := nmgr.js.StreamInfo(stream)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	last := -1
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq; seq++ {
		raw, err := nmgr.js.GetMsg(stream, seq)
		if err != nil {
			continue
		}
		id := raw.Header.Get(nats.MsgIdHdr)
		if !strings.HasPrefix(id, "mid:") {
			continue
		}
		i, _ := strconv.Atoi(strings.TrimPrefix(id, "mid:"))
		if seen[i] {
			t.Fatalf("publish %d is in the stream twice", i)
		}
		if i < last {
			t.Fatalf("publish %d is stored after publish %d: the stream's order is not the submission order", i, last)
		}
		seen[i], last = true, i
	}
	acked := 0
	for i := 0; i < n; i++ {
		if out.errs[i] == nil {
			acked++
			if !seen[i] {
				t.Fatalf("publish %d was acknowledged and is not in the stream", i)
			}
		}
	}
	t.Logf("%d of %d acknowledged across the election", acked, n)
}
