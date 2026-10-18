// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// drainCluster is a three-node cluster whose routes can be silenced, carrying a fail-closed
// store and its durables at three replicas.
type drainCluster struct {
	t       *testing.T
	servers []*natsserver.Server
	opts    []*natsserver.Options
	faults  *dctest.RouteFaults
	nc      *nats.Conn
	js      nats.JetStreamContext
}

func startDrainCluster(t *testing.T) *drainCluster {
	t.Helper()
	opts := make([]*natsserver.Options, 3)
	servers, faults := dctest.StartJetStreamClusterConfigured(t, 3, func(i int, o *natsserver.Options) { opts[i] = o })
	nc, err := nats.Connect(servers[0].ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream(nats.MaxWait(10 * time.Second))
	require.NoError(t, err)
	return &drainCluster{t: t, servers: servers, opts: opts, faults: faults, nc: nc, js: js}
}

// stepDownTo moves the leader named by the API subject to the server preferred, and reports
// whether the broker confirmed the move.
func (dc *drainCluster) stepDownTo(subject, preferred string) bool {
	body, _ := json.Marshal(map[string]any{"placement": map[string]any{"preferred": preferred}})
	msg, err := dc.nc.Request(subject, body, 5*time.Second)
	if err != nil {
		return false
	}
	var resp struct {
		Error   *jsAPIError `json:"error"`
		Success bool        `json:"success"`
	}
	return json.Unmarshal(msg.Data, &resp) == nil && resp.Error == nil && resp.Success
}

func (dc *drainCluster) streamLeader() string {
	si, err := dc.js.StreamInfo(MqttMessageStore)
	if err != nil || si.Cluster == nil {
		return ""
	}
	return si.Cluster.Leader
}

func (dc *drainCluster) consumerLeader(name string) string {
	ci, err := dc.js.ConsumerInfo(MqttMessageStore, name)
	if err != nil || ci.Cluster == nil {
		return ""
	}
	return ci.Cluster.Leader
}

// keepOff moves the stream's and every platform durable's leader off server s, onto server
// keep. s must not be the meta leader: a meta step-down needs the system account.
func (dc *drainCluster) keepOff(s, keep int) {
	dc.t.Helper()
	off, on := dc.servers[s].Name(), dc.servers[keep].Name()
	require.Eventually(dc.t, func() bool {
		if l := dc.metaLeader(); l == off || l == "" {
			return false
		}
		if l := dc.streamLeader(); l == off || l == "" {
			dc.stepDownTo("$JS.API.STREAM.LEADER.STEPDOWN."+MqttMessageStore, on)
			return false
		}
		for _, d := range MqttFailClosedDurables(guardInstance) {
			if l := dc.consumerLeader(d.Name); l == off || l == "" {
				dc.stepDownTo("$JS.API.CONSUMER.LEADER.STEPDOWN."+MqttMessageStore+"."+d.Name, on)
				return false
			}
		}
		return true
	}, 60*time.Second, 250*time.Millisecond, "no leader could be moved off %s", off)
}

// quietServer picks the server to silence: not the one the test's client is connected to, and
// not the meta leader. keep is the other one.
func (dc *drainCluster) quietServer() (s, keep int) {
	dc.t.Helper()
	var meta string
	require.Eventually(dc.t, func() bool { meta = dc.metaLeader(); return meta != "" }, 30*time.Second, 100*time.Millisecond)
	if dc.servers[2].Name() == meta {
		return 1, 2
	}
	return 2, 1
}

func (dc *drainCluster) metaLeader() string {
	for _, s := range dc.servers {
		if s.JetStreamIsLeader() {
			return s.Name()
		}
	}
	return ""
}

// publish stores n events, asynchronously, and returns the store sequences the broker
// acknowledged: what the gateway's PUBACK to a device reports.
func (dc *drainCluster) publish(from, n int) map[uint64]bool {
	dc.t.Helper()
	acked := map[uint64]bool{}
	futures := make([]nats.PubAckFuture, 0, 4096)
	flush := func() {
		select {
		case <-dc.js.PublishAsyncComplete():
		case <-time.After(60 * time.Second):
		}
		for _, f := range futures {
			select {
			case pa := <-f.Ok():
				acked[pa.Sequence] = true
			case <-f.Err():
			default:
			}
		}
		futures = futures[:0]
	}
	for i := 0; i < n; i++ {
		subj := fmt.Sprintf("%s%s.%s.devices.d%d.events", mqttMsgsSubjectPrefix, guardInstance, guardTenant, (from+i)%500)
		f, err := dc.js.PublishAsync(subj, []byte(fmt.Sprintf("m%d", from+i)))
		if err == nil {
			futures = append(futures, f)
		}
		if len(futures) == cap(futures) {
			flush()
		}
	}
	flush()
	return acked
}

// manager is a fail-closed manager over the cluster at three replicas, its store bound well
// above the backlogs these tests build, reading the broker through js.
func (dc *drainCluster) manager(on bool, js nats.JetStreamContext, clock *fcClock) *NatsManager {
	dc.t.Helper()
	m := failClosedManager(dc.t, dc.nc, js, guardInstance, on)
	n := &m.Microservice.InstanceConfiguration.Infrastructure.Nats
	n.StreamReplicas = 3
	n.StreamMaxBytes = 1 << 30
	n.StreamMaxBytesCold = 1 << 30
	m.mqttClock = clock.now
	require.Eventually(dc.t, m.brokerIsClustered, 10*time.Second, 50*time.Millisecond, "premise: the manager sees a cluster")
	return m
}

// moveLeaders moves the store's leader and the events durable's leader, each confirmed.
func (dc *drainCluster) moveLeaders(durable string) {
	dc.t.Helper()
	for _, subj := range []string{
		"$JS.API.STREAM.LEADER.STEPDOWN." + MqttMessageStore,
		"$JS.API.CONSUMER.LEADER.STEPDOWN." + MqttMessageStore + "." + durable,
	} {
		require.Eventuallyf(dc.t, func() bool {
			msg, err := dc.nc.Request(subj, nil, 5*time.Second)
			return err == nil && !strings.Contains(string(msg.Data), `"error"`)
		}, 60*time.Second, 250*time.Millisecond, "%s was never confirmed", subj)
	}
}

// awaitSettled waits until the store and every platform durable have a leader and every
// replica current, which a pass needs to write the release and to finish.
func (dc *drainCluster) awaitSettled() {
	dc.t.Helper()
	require.Eventually(dc.t, func() bool {
		si, err := dc.js.StreamInfo(MqttMessageStore)
		if err != nil || unsettledGroup(si.Cluster, 3, true) != "" {
			return false
		}
		for _, d := range MqttFailClosedDurables(guardInstance) {
			ci, err := dc.js.ConsumerInfo(MqttMessageStore, d.Name)
			if err != nil || unsettledGroup(ci.Cluster, 3, true) != "" {
				return false
			}
		}
		return true
	}, 2*time.Minute, 250*time.Millisecond, "the cluster never settled")
}

// falseZeroJS answers ConsumerInfo for one durable with what a consumer leader whose copy of
// the store is behind answers (nats-server 2.15, consumer.checkNumPending): nothing pending and
// nothing awaiting an ack, with its real ack floor and delivered position. That answer is read
// from the server's code; neither three nats-server processes (the zeros an earlier chaos run
// printed were timed-out ConsumerInfo requests) nor the in-process cluster opened the window
// on its own (step-downs every 5 s under 8k/s with a reader behind; a server
// restarted into a 400k backlog with the durable's leader moved onto it as it caught up; a
// killed consumer leader: the count stayed right in each), so the window is put in here, at
// the one boundary that reads it, while everything else is the real replicated store going
// through real leader moves and a real restart.
type falseZeroJS struct {
	nats.JetStreamContext
	durable string
	armed   atomic.Bool
}

func (j *falseZeroJS) ConsumerInfo(stream, name string, opts ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	ci, err := j.JetStreamContext.ConsumerInfo(stream, name, opts...)
	if err == nil && stream == MqttMessageStore && name == j.durable && j.armed.Load() {
		cp := *ci
		cp.NumPending, cp.NumAckPending = 0, 0
		return &cp, nil
	}
	return ci, err
}

// drainReader reads one durable and records the store sequence of every message it acked.
type drainReader struct {
	sub  *nats.Subscription
	read map[uint64]bool
}

func newDrainReader(t *testing.T, js nats.JetStreamContext, d MqttFailClosedDurable) *drainReader {
	t.Helper()
	sub, err := js.PullSubscribe(d.Filter, d.Name, nats.Bind(MqttMessageStore, d.Name))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return &drainReader{sub: sub, read: map[uint64]bool{}}
}

// readN acks up to n more messages, waiting at most within.
func (r *drainReader) readN(n int, within time.Duration) {
	deadline := time.Now().Add(within)
	for got := 0; got < n && time.Now().Before(deadline); {
		msgs, _ := r.sub.Fetch(min(256, n-got), nats.MaxWait(time.Second))
		for _, m := range msgs {
			md, err := m.Metadata()
			if err != nil || m.AckSync() != nil {
				continue
			}
			if !r.read[md.Sequence.Stream] {
				r.read[md.Sequence.Stream] = true
				got++
			}
		}
	}
}

// readAll acks until every acknowledged sequence has been read, waiting at most within.
func (r *drainReader) readAll(acked map[uint64]bool, within time.Duration) {
	deadline := time.Now().Add(within)
	for r.unread(acked) > 0 && time.Now().Before(deadline) {
		r.readN(r.unread(acked), time.Until(deadline))
	}
}

func (r *drainReader) unread(acked map[uint64]bool) int {
	n := 0
	for seq := range acked {
		if !r.read[seq] {
			n++
		}
	}
	return n
}

// preFixDrained is the rule the finish decided "drained" by before it read the store: nothing
// pending and nothing awaiting an ack, or acked past the cut.
func preFixDrained(ci *nats.ConsumerInfo, drainTo uint64) bool {
	empty := ci.NumPending == 0 && ci.NumAckPending == 0
	return empty || ci.AckFloor.Stream >= drainTo
}

// 🔴 A RELEASED STORE'S DURABLES ARE NOT REMOVED ON A ZERO PENDING COUNT THE STORE CONTRADICTS.
// A replicated store holds a backlog its events durable has not been handed (40k of 60k
// acknowledged by the store, which is what a device's PUBACK reports), a server is restarted
// into the backlog, and the store's leader and the durable's leader both move. The store is
// released and its grace runs out while the durable's leader reports what a leader with a copy
// that is behind reports: 0 pending, 0 awaiting an ack.
//
// The control applies the rule the finish used to apply to that answer, and does what the
// finish then did: it deletes the durables, and with no consumer left interest retention
// removes the store copies, so every PUBACKed message not yet read is gone. With the fix the
// finish reads the store, sees the backlog below the cut, and keeps the durables (and the
// backlog gauge is withdrawn rather than reading 0); once the reader has read everything it
// finishes, with the count still lying, and nothing PUBACKed was lost.
//
// GUARD: kills a finish that decides "drained" from the durable's pending count.
func TestAFinishNeverTrustsAZeroPendingCountOverABacklog(t *testing.T) {
	for _, tc := range []struct {
		name   string
		preFix bool
	}{
		{"control: the pending count decides", true},
		{"the store decides", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fastMqttStoreLookup(t)
			dc := startDrainCluster(t)
			clock := newFcClock()
			on := dc.manager(true, dc.js, clock)
			_, err := on.ReconcileMqttFailClosed(context.Background())
			require.NoError(t, err)
			ev := fcDurable(t, guardInstance, MqttFailClosedEvents)
			r := newDrainReader(t, dc.js, ev)

			const published, readFirst = 60_000, 20_000
			acked := dc.publish(0, published)
			require.Len(t, acked, published, "premise: every publish was stored")
			r.readN(readFirst, 2*time.Minute)
			require.Len(t, r.read, readFirst, "premise: the reader read part of the backlog")

			// Real faults while the backlog is there: a server restarted into it, and both
			// leaders moved.
			s, keep := dc.quietServer()
			dc.keepOff(s, keep)
			_, err = dc.faults.Restart(s, dc.opts[s])
			require.NoError(t, err)
			dc.moveLeaders(ev.Name)
			dc.awaitSettled()

			fz := &falseZeroJS{JetStreamContext: dc.js, durable: ev.Name}
			off := dc.manager(false, fz, clock)
			clock.advance(mqttFailClosedLeaseTTL)
			rep, err := off.checkMqttFailClosed(context.Background(), nil)
			require.NoError(t, err)
			require.True(t, rep.released, "premise: the store was released")
			clock.advance(mqttFailClosedReleaseGrace)

			fz.armed.Store(true)
			real, err := dc.js.ConsumerInfo(MqttMessageStore, ev.Name)
			require.NoError(t, err)
			require.EqualValues(t, published-readFirst, real.NumPending, "premise: the backlog is real")
			lied, err := fz.ConsumerInfo(MqttMessageStore, ev.Name)
			require.NoError(t, err)
			require.Zero(t, lied.NumPending, "premise: the durable's leader reports nothing pending")

			// The first pass after the grace records the cut, which the control needs.
			m := fcMetrics(guardInstance)
			rep, err = off.checkMqttFailClosed(context.Background(), m)
			require.NoError(t, err)
			si, err := dc.js.StreamInfo(MqttMessageStore)
			require.NoError(t, err)
			cut, ok := si.Config.Metadata[mqttFailClosedDrainToKey]
			require.True(t, ok, "premise: the pass recorded the drain cut")
			drainTo, err := strconv.ParseUint(cut, 10, 64)
			require.NoError(t, err)

			if tc.preFix {
				require.False(t, rep.finished, "premise: the fixed finish refused; the control is the old rule alone")
				for _, d := range MqttFailClosedDurables(guardInstance) {
					ci, err := fz.ConsumerInfo(MqttMessageStore, d.Name)
					require.NoError(t, err)
					require.Truef(t, preFixDrained(ci, drainTo), "control: the old rule takes %s for drained", d.Name)
				}
				for _, d := range MqttFailClosedDurables(guardInstance) {
					require.NoError(t, dc.js.DeleteConsumer(MqttMessageStore, d.Name))
				}
				require.Eventually(t, func() bool {
					si, err := dc.js.StreamInfo(MqttMessageStore)
					return err == nil && si.State.Msgs == 0
				}, 60*time.Second, 250*time.Millisecond, "control: with no consumer left, the store removes its copies")
				lost := r.unread(acked)
				t.Logf("control: the old rule removed the durables over a backlog it was told was empty; %d of %d "+
					"PUBACKed messages were never read and are no longer in the store", lost, published)
				require.Equal(t, published-readFirst, lost, "control: every PUBACKed message not yet read is lost")
				return
			}

			for i := 0; i < 5; i++ {
				require.Falsef(t, rep.finished, "pass %d removed the durables over a backlog of %d", i, published-readFirst)
				for _, d := range MqttFailClosedDurables(guardInstance) {
					_, err := dc.js.ConsumerInfo(MqttMessageStore, d.Name)
					require.NoErrorf(t, err, "pass %d: the durable %s stays", i, d.Name)
				}
				rep, err = off.checkMqttFailClosed(context.Background(), m)
				require.NoError(t, err)
			}
			require.False(t, rep.finished)
			// The gauge: the events durable's backlog is withdrawn, not exported as the 0 its
			// leader reports; the other two, which really hold nothing, read 0.
			require.Equal(t, len(MqttFailClosedDurables(guardInstance))-1, testutil.CollectAndCount(m.durablePending),
				"the events durable's backlog was exported from a count the store contradicts")
			for _, d := range MqttFailClosedDurables(guardInstance) {
				if d.Kind != MqttFailClosedEvents {
					require.Zero(t, testutil.ToFloat64(m.durablePending.WithLabelValues(d.Name)))
				}
			}

			r.readAll(acked, 3*time.Minute)
			require.Zero(t, r.unread(acked), "the reader read the whole backlog")
			for i := 0; i < 60 && !rep.finished; i++ {
				time.Sleep(500 * time.Millisecond)
				rep, err = off.checkMqttFailClosed(context.Background(), m)
				require.NoError(t, err)
			}
			require.True(t, rep.finished, "a drained store was never finished: the count kept lying, and the store "+
				"should have been enough")
			t.Logf("%d PUBACKed, 0 lost; the finish waited out a backlog of %d its durable's leader reported as empty",
				published, published-readFirst)
		})
	}
}

// A drained store is not finished while one of its replicas is down, and is once it is back.
// The finish takes the store's answer only from a cluster that can stand behind it: a leader,
// every replica online and current. Here the reader has read everything, so the store itself
// says "drained", and a follower is shut down: no pass finishes. The control is the same
// store once the follower is restarted and has caught up, which finishes.
//
// GUARD: kills a finish that removes the durables while the store is missing a replica.
func TestAFinishWaitsForEveryReplicaOfTheStore(t *testing.T) {
	fastMqttStoreLookup(t)
	dc := startDrainCluster(t)
	clock := newFcClock()
	on := dc.manager(true, dc.js, clock)
	_, err := on.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	ev := fcDurable(t, guardInstance, MqttFailClosedEvents)
	r := newDrainReader(t, dc.js, ev)
	acked := dc.publish(0, 5_000)
	r.readAll(acked, time.Minute)
	require.Zero(t, r.unread(acked), "premise: everything was read")

	s, keep := dc.quietServer()
	dc.keepOff(s, keep)
	off := dc.manager(false, dc.js, clock)
	clock.advance(mqttFailClosedLeaseTTL)
	rep, err := off.checkMqttFailClosed(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, rep.released, "premise: the store was released")
	clock.advance(mqttFailClosedReleaseGrace)

	dc.servers[s].Shutdown()
	require.Eventually(t, func() bool {
		si, err := dc.js.StreamInfo(MqttMessageStore)
		return err == nil && unsettledGroup(si.Cluster, 3, true) != ""
	}, 60*time.Second, 250*time.Millisecond, "premise: the store reports the replica gone")
	for i := 0; i < 3; i++ {
		rep, err = off.checkMqttFailClosed(context.Background(), nil)
		require.NoError(t, err)
		require.Falsef(t, rep.finished, "pass %d finished the release with a replica of the store down", i)
	}

	_, err = dc.faults.Restart(s, dc.opts[s])
	require.NoError(t, err)
	for i := 0; i < 120 && !rep.finished; i++ {
		time.Sleep(500 * time.Millisecond)
		rep, err = off.checkMqttFailClosed(context.Background(), nil)
		require.NoError(t, err)
	}
	require.True(t, rep.finished, "control: with every replica back, the drained store finishes")
}

// A released REPLICATED store finishes while devices keep publishing into it. Under steady
// traffic a follower is often an entry or two behind for an instant, and a finish that wants
// every replica current must not be starved by that (the single-server version is
// TestAReleasedStoreFinishesUnderSteadyLoad).
//
// GUARD: kills a settle check that never passes under load on a cluster.
func TestAReleasedReplicatedStoreFinishesUnderSteadyLoad(t *testing.T) {
	fastMqttStoreLookup(t)
	dc := startDrainCluster(t)
	clock := newFcClock()
	on := dc.manager(true, dc.js, clock)
	_, err := on.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	off := dc.manager(false, dc.js, clock)
	clock.advance(mqttFailClosedLeaseTTL)
	rep, err := off.checkMqttFailClosed(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, rep.released)
	clock.advance(mqttFailClosedReleaseGrace)

	ev := fcDurable(t, guardInstance, MqttFailClosedEvents)
	sub, err := dc.js.PullSubscribe(ev.Filter, ev.Name, nats.Bind(MqttMessageStore, ev.Name))
	require.NoError(t, err)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var read, published atomic.Int64
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			msgs, _ := sub.Fetch(256, nats.MaxWait(200*time.Millisecond))
			for _, m := range msgs {
				_ = m.Ack()
				read.Add(1)
			}
		}
	}()
	go func() {
		defer wg.Done()
		pnc, err := nats.Connect(dc.servers[0].ClientURL())
		if err != nil {
			return
		}
		defer pnc.Close()
		pjs, err := pnc.JetStream(nats.PublishAsyncMaxPending(512))
		if err != nil {
			return
		}
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			for j := 0; j < 20; j++ { // ~2,000/s
				i++
				subj := fmt.Sprintf("%s%s.%s.devices.d%d.events", mqttMsgsSubjectPrefix, guardInstance, guardTenant, i%200)
				if _, err := pjs.PublishAsync(subj, []byte("x")); err == nil {
					published.Add(1)
				}
			}
		}
	}()
	defer func() { close(stop); wg.Wait(); _ = sub.Unsubscribe() }()
	require.Eventually(t, func() bool { return published.Load() > 10_000 }, time.Minute, 100*time.Millisecond,
		"premise: the traffic is steady before the first pass")

	for i := 0; i < 60 && !rep.finished; i++ {
		time.Sleep(500 * time.Millisecond)
		rep, err = off.checkMqttFailClosed(context.Background(), nil)
		require.NoError(t, err)
	}
	t.Logf("published %d, read %d during the check", published.Load(), read.Load())
	require.True(t, rep.finished, "a released replicated store under steady traffic never finished")
}
