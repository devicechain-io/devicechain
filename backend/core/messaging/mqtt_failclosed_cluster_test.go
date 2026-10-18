// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package messaging

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// On a cluster the store and its durables take the configured replica count: a fresh store
// is created with it, and a store the gateway made at one replica is raised to it in place.
// (On a single server the count is 1 whether or not it is set, so the single-server tests
// cannot see it.)
//
// GUARD: kills a create without the replica count, and a conform that does not raise it.
func TestTheFailClosedStoreTakesTheConfiguredReplicasOnACluster(t *testing.T) {
	servers := dctest.StartJetStreamCluster(t, 3)
	nc, err := nats.Connect(servers[0].ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	nmgr := failClosedManager(t, nc, js, guardInstance, true)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	require.Eventually(t, func() bool { return nmgr.brokerIsClustered() }, 10*time.Second, 50*time.Millisecond,
		"premise: the manager sees a cluster")

	state, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, state.Replicas)
	si, err := js.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	require.Equal(t, 3, si.Config.Replicas, "a fresh store is created at the configured replicas")
	for _, d := range MqttFailClosedDurables(guardInstance) {
		ci, err := js.ConsumerInfo(MqttMessageStore, d.Name)
		require.NoError(t, err)
		require.Equalf(t, 3, ci.Config.Replicas, "the %s durable's replicas", d.Kind)
	}

	// A store the gateway made first, at one replica.
	require.NoError(t, js.DeleteStream(MqttMessageStore))
	gw, err := js.AddStream(guardStoreConfig(nats.DiscardOld, 0, 1))
	require.NoError(t, err)
	// Through the retrying entry point: a durable created at three replicas straight after
	// the store's own scale-up can be refused for "insufficient resources" until the new
	// peers have caught up, which the startup retry schedule is there to absorb.
	_, err = nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	si, err = js.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	require.Equal(t, 3, si.Config.Replicas, "a one-replica store is raised in place")
	require.Equal(t, gw.Created, si.Created, "raised in place, not recreated")
}

// insufficientJS refuses, as a cluster whose new peers have not caught up does, the next
// replica raise of the store and the next consumer creates or updates, with "insufficient
// resources" (JSErrCodeInsufficientResourcesErr). The real cluster gives that answer only in a
// narrow window after a raise; this makes it happen when the test needs it.
type insufficientJS struct {
	nats.JetStreamContext
	mu            sync.Mutex
	streamRaises  int
	consumerCalls int
}

var errInsufficientForTest = &nats.APIError{Code: 503, ErrorCode: nats.JSErrCodeInsufficientResourcesErr,
	Description: "insufficient resources"}

func (j *insufficientJS) take(n *int) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if *n > 0 {
		*n--
		return true
	}
	return false
}

func (j *insufficientJS) UpdateStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	if cfg.Name == MqttMessageStore && cfg.Replicas > 1 && j.take(&j.streamRaises) {
		return nil, errInsufficientForTest
	}
	return j.JetStreamContext.UpdateStream(cfg, opts...)
}

func (j *insufficientJS) AddConsumer(stream string, cfg *nats.ConsumerConfig, opts ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	if stream == MqttMessageStore && j.take(&j.consumerCalls) {
		return nil, errInsufficientForTest
	}
	return j.JetStreamContext.AddConsumer(stream, cfg, opts...)
}

func (j *insufficientJS) UpdateConsumer(stream string, cfg *nats.ConsumerConfig, opts ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	if stream == MqttMessageStore && j.take(&j.consumerCalls) {
		return nil, errInsufficientForTest
	}
	return j.JetStreamContext.UpdateConsumer(stream, cfg, opts...)
}

// 🔑 L2: "insufficient resources" around a replica raise is transient, and is retried rather
// than failing a start or paging. A raise of the store the cluster refuses leaves it
// fail-closed at the count it has and is tried again by the next pass; a durable create or
// raise it refuses is retried inside the pass; and none of it counts as the store having been
// changed out of band. Once the raise goes through, the durables follow the store's count.
//
// GUARD: kills a start that fails on the raise (a planned replica change would crash-loop
// event-sources), a pass that reports a durable it could create on a retry as missing (it
// would page MqttFailClosedDurableMissing), a raise counted on the repair metric (it would page
// MqttFailClosedStoreNotAsDeclared), and durables left on fewer peers than their store.
func TestInsufficientResourcesAroundAReplicaRaiseIsRetriedNotFailed(t *testing.T) {
	servers := dctest.StartJetStreamCluster(t, 3)
	nc, err := nats.Connect(servers[0].ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	real, err := nc.JetStream()
	require.NoError(t, err)
	js := &insufficientJS{JetStreamContext: real, streamRaises: 1}
	_, err = real.AddStream(guardStoreConfig(nats.DiscardOld, 0, 1))
	require.NoError(t, err)

	nmgr := failClosedManager(t, nc, js, guardInstance, true)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	require.Eventually(t, func() bool { return nmgr.brokerIsClustered() }, 10*time.Second, 50*time.Millisecond)
	_, err = nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err, "a raise the cluster refused for resources failed the start")
	si, err := real.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	require.Equal(t, nats.DiscardNew, si.Config.Discard, "the store is fail-closed at the count it has")
	require.Equal(t, 1, si.Config.Replicas, "premise: the raise was refused")

	// The next pass raises the store; the durables' raises are refused twice, then go through.
	js.mu.Lock()
	js.consumerCalls = 2
	js.mu.Unlock()
	// The real cluster refuses a durable's raise too ("exceeds parent stream") until it has
	// applied the store's, so a pass may still leave one pending: the next pass picks it up.
	m := fcMetrics(guardInstance)
	pending := true
	for i := 0; i < 5 && pending; i++ {
		rep, err := nmgr.checkMqttFailClosed(context.Background(), m)
		require.NoErrorf(t, err, "pass %d failed on a refusal it should have left for later", i)
		pending = rep.pendingReplicas
	}
	require.False(t, pending, "the raise was never completed by a later pass")
	si, err = real.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	require.Equal(t, 3, si.Config.Replicas, "the next pass did not raise the store")
	for _, d := range MqttFailClosedDurables(guardInstance) {
		ci, err := real.ConsumerInfo(MqttMessageStore, d.Name)
		require.NoError(t, err)
		require.Equalf(t, 3, ci.Config.Replicas, "the %s durable is on fewer peers than its store", d.Kind)
	}

	// A durable gone and its create refused twice: created on a retry, present, not absent.
	events := fcDurable(t, guardInstance, MqttFailClosedEvents)
	require.NoError(t, real.DeleteConsumer(MqttMessageStore, events.Name))
	js.mu.Lock()
	js.consumerCalls = 2
	js.mu.Unlock()
	rep, err := nmgr.checkMqttFailClosed(context.Background(), m)
	require.NoError(t, err)
	require.Empty(t, rep.absentDurables, "a durable created on a retry was reported missing and not creatable")
	require.EqualValues(t, 1, testutil.ToFloat64(m.durablePresent.WithLabelValues(events.Name)))
	for _, s := range mqttFailClosedSettings {
		require.EqualValuesf(t, 0, testutil.ToFloat64(m.storeRepaired.WithLabelValues(s)), "the raise counted %s", s)
	}
}

// 🔑 THE GKE ROUND-13 FAILURE MODE, end to end through the platform's own reader. Under load a
// replicated stream's leader can step down on its own ("Leader falling behind"), and for the
// seconds before a new one is elected the gateway's direct copy of each device publish, sent
// to the capture stream with no reply, is dropped while the device is PUBACKed on the store.
// With the fail-closed store declared by the reconcile, read by NewMqttMessageStoreReader,
// the PUBACK waits on the store itself: here the leaders of BOTH the message store and the
// capture stream are stepped down repeatedly while devices publish, and every message a device
// was PUBACKed is read from the store's events durable by the platform reader. Duplicates are
// allowed (a store in flight at the move times out, the connection closes, the device re-sends);
// loss is not.
//
// GUARD: kills a reconcile whose durable does not follow the store across a leader move (it
// would be R1 on a node that lost leadership, or missing), a reader that gives up on a durable
// whose leader moved, and a nats-server whose store acknowledges what a new leader then lacks.
func TestALeaderMoveUnderLoadLosesNoPubackedMessageThroughThePlatformReader(t *testing.T) {
	mqttPorts := []int{dctest.FreeTCPPort(t), dctest.FreeTCPPort(t), dctest.FreeTCPPort(t)}
	servers, _ := dctest.StartJetStreamClusterConfigured(t, 3, func(i int, o *natsserver.Options) {
		o.MQTT = natsserver.MQTTOpts{Host: "127.0.0.1", Port: mqttPorts[i], StreamReplicas: 3}
	})
	nc, err := nats.Connect(servers[0].ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	nmgr := failClosedManager(t, nc, js, guardInstance, true)
	n := &nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	n.StreamReplicas = 3
	n.StreamMaxBytes, n.StreamMaxBytesCold = 64<<20, 64<<20
	require.Eventually(t, func() bool { return nmgr.brokerIsClustered() }, 10*time.Second, 50*time.Millisecond)
	_, err = nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	// The capture stream exists, as in the service, so its leader can be moved too.
	_, err = nmgr.NewReader(streams.DeviceEventsCapture)
	require.NoError(t, err)
	r, err := fcEventsReader(t, nmgr)
	require.NoError(t, err)
	t.Cleanup(r.Close)

	var mu sync.Mutex
	seen := map[string]int{}
	deliveries := newDeliveryLog()
	ctx, cancel := context.WithCancel(context.Background())
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for ctx.Err() == nil {
			msg, err := r.ReadMessage(ctx)
			if err != nil {
				continue
			}
			mu.Lock()
			seen[string(msg.Value)]++
			mu.Unlock()
			deliveries.add(msg.StreamSeq, string(msg.Value))
			_ = msg.Ack()
		}
	}()
	t.Cleanup(func() { cancel(); <-readDone })

	var moves atomic.Int64
	timeline := newMoveLog()
	stepDown := func(stream string) {
		msg, err := nc.Request("$JS.API.STREAM.LEADER.STEPDOWN."+stream, nil, 3*time.Second)
		if err == nil && !bytes.Contains(msg.Data, []byte(`"error"`)) {
			moves.Add(1)
			timeline.add(stream)
		}
	}
	// The leaders are stepped down until the test has CONFIRMED enough moves, not for a fixed
	// time: a step-down is refused while a new leader is still being elected, so a fixed window
	// sometimes confirmed one. The devices keep publishing (another round each) until that
	// many moves have happened, so the load spans all of them.
	const requiredMoves = 6
	stopMoves := make(chan struct{})
	var mover sync.WaitGroup
	mover.Add(1)
	go func() {
		defer mover.Done()
		capture := StreamName(guardInstance, streams.DeviceEventsCapture)
		for i := 0; ; i++ {
			select {
			case <-stopMoves:
				return
			case <-time.After(200 * time.Millisecond):
				if i%2 == 0 {
					stepDown(MqttMessageStore)
				} else {
					stepDown(capture)
				}
			}
		}
	}()

	const devices, perDevice = 6, 200
	loadDeadline := time.Now().Add(90 * time.Second)
	var (
		acked   []string
		resumed []string // completed with no PUBACK: not acknowledged, so not owed
		wg      sync.WaitGroup
		okRound = make([]int, devices) // rounds each device finished whole, by device
		lastErr = make([]error, devices)
	)
	for d := 0; d < devices; d++ {
		wg.Add(1)
		go func(d int) {
			defer wg.Done()
			dev := fmt.Sprintf("load%d", d)
			// A round that fails under the moves (a connect that kept being refused, a publish
			// answered late) is tried again with fresh payloads, until the deadline. What it
			// did get PUBACKed still counts: the device was told those were stored.
			for round := 0; round == 0 || (moves.Load() < requiredMoves+2 && time.Now().Before(loadDeadline)); round++ {
				got, err := publishThroughResult(t, fmt.Sprintf("tcp://127.0.0.1:%d", mqttPorts[d%3]), dev,
					guardTopic(guardInstance, guardTenant, dev), fmt.Sprintf("%s-r%d", dev, round), perDevice)
				mu.Lock()
				acked = append(acked, got.pubacked...)
				resumed = append(resumed, got.resumed...)
				if err == nil {
					okRound[d]++
				}
				lastErr[d] = err
				mu.Unlock()
				if err != nil && !time.Now().Before(loadDeadline) {
					return
				}
			}
		}(d)
	}
	wg.Wait()
	close(stopMoves)
	mover.Wait()
	for d := range okRound {
		require.Positive(t, okRound[d], "device load%d never finished a round: %v", d, lastErr[d])
	}
	require.GreaterOrEqual(t, moves.Load(), int64(requiredMoves), "the leaders were hardly moved, so little was tested")
	mu.Lock()
	require.NotEmpty(t, acked)
	mu.Unlock()

	lost := func() []string {
		mu.Lock()
		defer mu.Unlock()
		var out []string
		for _, a := range acked {
			if seen[a] == 0 {
				out = append(out, a)
			}
		}
		return out
	}
	// A message the durable delivered and the reader then dropped at a move (a fetch answered
	// by a leader that stepped down, a buffer a new term discarded) is redelivered only once its
	// AckWait has run out, so the bound is past it: what is still unread then is not late.
	loadEnd := timeline.since()
	deadline := time.Now().Add(AckWait + 30*time.Second)
	for len(lost()) > 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if unread := lost(); len(unread) > 0 {
		t.Logf("load ended at %s", loadEnd)
		absent := unreadReport(t, js, fcDurable(t, guardInstance, MqttFailClosedEvents).Name,
			mqttMsgsSubjectPrefix+">", unread, timeline)
		if si, err := js.StreamInfo(MqttMessageStore); err == nil {
			deliveries.report(t, si.State.LastSeq, unread)
		}
		t.Fatalf("%d PUBACKed messages never reached the platform reader across leader moves, %d of them absent from the store",
			len(unread), len(absent))
	}
	mu.Lock()
	dups := 0
	for _, c := range seen {
		dups += c - 1
	}
	unacked := 0
	for _, r := range resumed {
		if seen[r] == 0 {
			unacked++
		}
	}
	t.Logf("%d PUBACKed, 0 lost, %d duplicate deliveries across %d leader moves; %d publishes completed with no PUBACK, %d of them never read",
		len(acked), dups, moves.Load(), len(resumed), unacked)
	mu.Unlock()
}
